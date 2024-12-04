package app

import (
	"fmt"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
	"time"
	"tradebin-mm/app/data_provider"
	"tradebin-mm/app/dto"
	"tradebin-mm/app/internal"
)

const (
	volumeLock = "volume:make_volume"

	volumeMul = 5
)

type volumeConfig interface {
	GetMin() int64
	GetMax() int64
	GetTradeInterval() int
}

type volumeStrategy interface {
	GetMinAmount() *sdk.Int
	GetMaxAmount() *sdk.Int
	GetRemainingAmount() *sdk.Int
	SetRemainingAmount(amount *sdk.Int)
	GetOrderType() string
	LastRunAt() *time.Time
	SetLastRunAt(*time.Time)
}

type locker interface {
	Lock(key string)
	Unlock(key string)
}

type Volume struct {
	cfg          volumeConfig
	marketConfig data_provider.MarketProvider

	strategy        volumeStrategy
	locker          locker
	ordersProvider  ordersProvider
	addressProvider addressProvider
	balanceProvider balanceProvider
	orderSubmitter  orderSubmitter

	l logrus.FieldLogger
}

func NewVolumeMaker(
	cfg volumeConfig,
	l logrus.FieldLogger,
	marketConfig data_provider.MarketProvider,
	balanceProvider balanceProvider,
	addressProvider addressProvider,
	ordersProvider ordersProvider,
	orderSubmitter orderSubmitter,
	locker locker,
) (*Volume, error) {
	if cfg == nil || marketConfig == nil || balanceProvider == nil || addressProvider == nil || ordersProvider == nil || orderSubmitter == nil || l == nil || locker == nil {
		return nil, internal.NewInvalidDependenciesErr("NewOrdersFiller")
	}

	return &Volume{
		cfg:             cfg,
		marketConfig:    marketConfig,
		balanceProvider: balanceProvider,
		addressProvider: addressProvider,
		ordersProvider:  ordersProvider,
		orderSubmitter:  orderSubmitter,
		l:               l,
		locker:          locker,
	}, nil
}

func (v *Volume) MakeVolume() error {
	l := v.l.WithField("func", "MakeVolume")
	v.locker.Lock(volumeLock)
	defer v.locker.Unlock(volumeLock)
	l.Debugf("lock [%s] acquired", volumeLock)

	balances, err := v.balanceProvider.GetMarketBalance(v.addressProvider.GetAddress().String(), v.marketConfig)
	if err != nil {

		return fmt.Errorf("failed to get balances: %w", err)
	}

	l.WithField("balances", balances).Debugf("balances fetched")

	buys, sells, err := v.ordersProvider.GetActiveOrders(balances)
	if err != nil {
		return fmt.Errorf("failed to get active orders: %w", err)
	}

	strategy, err := v.getStrategy(balances, buys, sells)
	if err != nil {

		return fmt.Errorf("failed to get strategy: %w", err)
	}

	if strategy.LastRunAt().After(time.Now().Add(-time.Duration(v.cfg.GetTradeInterval()) * time.Second)) {
		l.Debug("it's not the time to make volume yet")

		return nil
	}

	var bookOrder tradebinTypes.AggregatedOrder
	if strategy.GetOrderType() == tradebinTypes.OrderTypeBuy {
		bookOrder = sells[0]
	} else {
		bookOrder = buys[0]
	}
	l.WithField("book_order", bookOrder).Debugf("will fill order of type %s", bookOrder.OrderType)

	order, orderAmount := v.makeOrder(strategy, &bookOrder)
	l.WithField("order_msg", order).Debug("order message created")

	err = v.orderSubmitter.AddOrders([]*tradebinTypes.MsgCreateOrder{order})
	if err != nil {
		//destroy the strategy maybe it's out of balance and it should change
		v.strategy = nil

		return fmt.Errorf("failed to submit order: %w", err)
	}
	l.WithField("order_msg", order).Debug("order message submitted")

	v.ackOrder(orderAmount)
	l.WithField("order_msg", order).Debug("order acknowledged")

	l.WithField("strategy", strategy).Debug("finished making volume")

	return nil
}

func (v *Volume) ackOrder(orderAmount sdk.Int) {
	now := time.Now()
	v.strategy.SetLastRunAt(&now)
	remaining := v.strategy.GetRemainingAmount().Sub(orderAmount)
	v.strategy.SetRemainingAmount(&remaining)
}

func (v *Volume) makeOrder(strategy volumeStrategy, bookOrder *tradebinTypes.AggregatedOrder) (msg *tradebinTypes.MsgCreateOrder, orderAmount sdk.Int) {
	orderAmount = internal.MustRandomInt(strategy.GetMinAmount(), strategy.GetMaxAmount())
	if orderAmount.GT(*strategy.GetRemainingAmount()) {
		orderAmount = *strategy.GetRemainingAmount()
	}
	bookAmount, _ := sdk.NewIntFromString(bookOrder.Amount)

	if orderAmount.GT(bookAmount) {
		orderAmount = bookAmount
	}

	msg = tradebinTypes.NewMsgCreateOrder(
		v.addressProvider.GetAddress().String(),
		strategy.GetOrderType(),
		orderAmount.String(),
		bookOrder.Price,
		v.marketConfig.GetMarketId(),
	)

	return msg, orderAmount
}

// getStrategy returns the appropriate volumeStrategy to use on the next trade
// If the strategy does not exist it creates a new one
// If orders needed for this strategy are missing from the order book it creates a new one
// If the remaining amount of this trade is lower than the required minimum then it creates a new strategy
func (v *Volume) getStrategy(balances *dto.MarketBalance, buys, sells []tradebinTypes.AggregatedOrder) (volumeStrategy, error) {
	myBuys, mySells, err := v.ordersProvider.GetAddressActiveOrders(balances.MarketId, v.addressProvider.GetAddress().String(), 1000)
	if err != nil {
		return nil, err
	}

	if v.strategy == nil {
		v.strategy, err = v.newStrategy(balances, myBuys, mySells, buys, sells)

		return v.strategy, err
	}

	if v.strategy.GetMinAmount().GT(*v.strategy.GetRemainingAmount()) {
		v.strategy, err = v.newStrategy(balances, myBuys, mySells, buys, sells)

		return v.strategy, err
	}

	if v.strategy.GetOrderType() == tradebinTypes.OrderTypeBuy && len(buys) == 0 {
		v.strategy, err = v.newStrategy(balances, myBuys, mySells, buys, sells)

		return v.strategy, err
	}

	if v.strategy.GetOrderType() == tradebinTypes.OrderTypeSell && len(sells) == 0 {
		v.strategy, err = v.newStrategy(balances, myBuys, mySells, buys, sells)

		return v.strategy, err
	}

	return v.strategy, nil
}

func (v *Volume) newStrategy(marketBalance *dto.MarketBalance, myBuys, mySells []tradebinTypes.Order, buys, sells []tradebinTypes.AggregatedOrder) (volumeStrategy, error) {
	strategy := v.makeStrategy(marketBalance, myBuys, mySells, buys, sells)
	if strategy == nil {
		return nil, fmt.Errorf("could not make strategy")
	}

	return strategy, nil
}

// makeStrategy - tries to create the appropriate strategy for the next trade
func (v *Volume) makeStrategy(marketBalance *dto.MarketBalance, myBuys, mySells []tradebinTypes.Order, buys, sells []tradebinTypes.AggregatedOrder) volumeStrategy {
	l := v.l.WithField("func", "makeStrategy")
	canSell := v.canTryMakeStrategy(marketBalance.BaseBalance, buys)
	canBuy := v.canTryMakeStrategy(marketBalance.QuoteBalance, sells)
	if !canSell && !canBuy {
		l.Errorf("can not create neither buy or sell strategy")

		return nil
	} else if !canSell {
		l.Info("can not sell, trying to make buy strategy")

		return v.makeBaseStrategy(tradebinTypes.OrderTypeBuy, mySells, sells)
	} else if !canBuy {
		l.Info("can not buy, trying to make sell strategy")

		return v.makeBaseStrategy(tradebinTypes.OrderTypeSell, myBuys, buys)
	}

	//it's safe to assume both sells and buys are not empty slices because of canBuy/canSell mentioned above
	ownsFirstSell := v.isMyOrder(mySells, sells[0])
	ownsFirstBuy := v.isMyOrder(myBuys, buys[0])
	if !ownsFirstSell && ownsFirstBuy {
		//if we own only the buy use a sell strategy in order to sell to us

		return v.makeBaseStrategy(tradebinTypes.OrderTypeSell, myBuys, buys)
	} else if ownsFirstSell && !ownsFirstBuy {
		//if we own only the first sell use a buy strategy in order to buy from us

		return v.makeBaseStrategy(tradebinTypes.OrderTypeBuy, mySells, sells)
	} else if !ownsFirstSell {
		//if we don't own any of the buy/sells in the order book then fill the one with the lowest amount
		sellAmt, _ := sdk.NewIntFromString(sells[0].Amount)
		buyAmt, _ := sdk.NewIntFromString(buys[0].Amount)
		if sellAmt.LT(buyAmt) {

			return v.makeBaseStrategy(tradebinTypes.OrderTypeBuy, mySells, sells)
		}

		return v.makeBaseStrategy(tradebinTypes.OrderTypeSell, myBuys, buys)
	}

	l.Debug("can sell and buy.")
	strategyOrderType := tradebinTypes.OrderTypeBuy
	strategyMyOrders := mySells
	strategyMarketOrders := sells
	history, err := v.ordersProvider.GetLastMarketOrder(marketBalance.MarketId)
	if err != nil {
		l.WithError(err).Error("can not get last market order")

		return v.makeBaseStrategy(strategyOrderType, strategyMyOrders, strategyMarketOrders)
	}

	if history != nil && history.OrderType == tradebinTypes.OrderTypeSell {
		l.Debug("last history order is buy")
		strategyOrderType = tradebinTypes.OrderTypeSell
		strategyMyOrders = myBuys
		strategyMarketOrders = buys
	}

	return v.makeBaseStrategy(strategyOrderType, strategyMyOrders, strategyMarketOrders)
}

func (v *Volume) canTryMakeStrategy(balance *sdk.Coin, marketOrders []tradebinTypes.AggregatedOrder) bool {
	l := v.l.WithField("func", "canTryMakeStrategy")
	if !balance.IsPositive() {
		l.Debug("balance is negative so this strategy can not be used")

		return false
	}

	return len(marketOrders) > 0
}

func (v *Volume) isMyOrder(myOrders []tradebinTypes.Order, order tradebinTypes.AggregatedOrder) bool {
	for _, my := range myOrders {
		if my.Price == order.Price && my.OrderType == order.OrderType {
			return true
		}
	}

	return false
}

func (v *Volume) makeBaseStrategy(orderType string, myOrders []tradebinTypes.Order, marketOrders []tradebinTypes.AggregatedOrder) volumeStrategy {
	l := v.l.WithField("func", "makeBaseStrategy").WithField("orderType", orderType)
	if len(myOrders) == 0 {
		l.Info("no marketOrders found in myOrders marketOrders")

		return nil
	}

	if len(marketOrders) == 0 {
		l.Info("no marketOrders found in aggregated orders")

		return nil
	}

	minVolume := sdk.NewInt(v.cfg.GetMin())
	maxVolume := sdk.NewInt(v.cfg.GetMax())

	remainingMin := minVolume.MulRaw(volumeMul)
	remainingMax := maxVolume.MulRaw(volumeMul)
	remaining := internal.MustRandomInt(&remainingMin, &remainingMax)

	return &dto.VolumeStrategy{
		MinVolume:       &minVolume,
		MaxVolume:       &maxVolume,
		RemainingVolume: &remaining,
		OrderType:       orderType,
		LastRun:         &time.Time{},
	}
}
