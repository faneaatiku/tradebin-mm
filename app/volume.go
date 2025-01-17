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

type volumeOrderConfig interface {
	GetPriceStepDec() *sdk.Dec
}

type volumeConfig interface {
	GetMin() int64
	GetMax() int64
	GetTradeInterval() int
	GetExtraMin() int64
	GetExtraMax() int64
	GetExtraEvery() int64
	GetStrategy() string
	GetHoldBackSeconds() int
}

type volumeStrategy interface {
	GetMinAmount() *sdk.Int
	GetMaxAmount() *sdk.Int
	GetRemainingAmount() *sdk.Int
	SetRemainingAmount(amount *sdk.Int)
	GetOrderType() string
	LastRunAt() *time.Time
	SetLastRunAt(*time.Time)
	GetExtraMinVolume() *sdk.Int
	GetExtraMaxVolume() *sdk.Int
	IncrementTradesCount()
	GetTradesCount() int64
	GetType() string
}

type locker interface {
	Lock(key string)
	Unlock(key string)
}

type Volume struct {
	cfg          volumeConfig
	orderConfig  volumeOrderConfig
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
	orderConfig volumeOrderConfig,
) (*Volume, error) {
	if cfg == nil || marketConfig == nil || balanceProvider == nil || addressProvider == nil || ordersProvider == nil || orderSubmitter == nil || l == nil || locker == nil || orderConfig == nil {
		return nil, internal.NewInvalidDependenciesErr("NewVolumeMaker")
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
		orderConfig:     orderConfig,
	}, nil
}

func (v *Volume) MakeVolume() error {
	l := v.l.WithField("func", "MakeVolume")
	if v.cfg.GetMax() == 0 {
		l.Info("volume maker is stopped due to volume max setting set to 0 or below")
	}

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

	allowedInterval := time.Now().Add(-time.Duration(v.cfg.GetTradeInterval()) * time.Second)
	if strategy.LastRunAt().After(allowedInterval) {
		l.Debug("it's not the time to make volume yet")

		return nil
	}

	history, err := v.ordersProvider.GetLastMarketOrder(balances.MarketId)
	if err != nil {
		return fmt.Errorf("failed to get last market order: %w", err)
	}

	if history != nil {
		histDate := time.Unix(history.ExecutedAt, 0)
		if histDate.After(allowedInterval) {
			l.WithField("hist_date", histDate).Info("market has been active in the last minutes. Will NOT create volume")

			return nil
		}

		//if a foreign address is the last trader hold back for X duration taken from config
		if history.Maker != v.addressProvider.GetAddress().String() || history.Taker != v.addressProvider.GetAddress().String() {
			holdBackUntil := histDate.Add(time.Duration(v.cfg.GetHoldBackSeconds()) * time.Second)
			if holdBackUntil.After(time.Now()) {
				l.WithField("hist_date", histDate).Infof("found foreign order holding back until: %s", holdBackUntil)

				return nil
			}
		}
	}

	order, orderAmount, msgs := v.makeOrder(strategy, buys, sells)
	if order == nil || !orderAmount.IsPositive() || len(msgs) == 0 {
		return nil
	}

	l.WithField("order_msg", order).Info("order message created")

	err = v.orderSubmitter.AddOrders(msgs)
	if err != nil {
		//destroy the strategy maybe it's out of balance and it should change
		v.strategy = nil

		return fmt.Errorf("failed to submit order: %w", err)
	}
	l.WithField("order_msg", order).Debug("order message submitted")

	v.ackOrder(orderAmount)
	l.WithField("order_msg", order).Debug("order acknowledged")

	l.WithField("strategy", strategy).Info("finished making volume")

	return nil
}

func (v *Volume) ackOrder(orderAmount sdk.Int) {
	now := time.Now()
	v.strategy.SetLastRunAt(&now)
	remaining := v.strategy.GetRemainingAmount().Sub(orderAmount)
	v.strategy.SetRemainingAmount(&remaining)
	v.strategy.IncrementTradesCount()
}

func (v *Volume) makeOrder(strategy volumeStrategy, buys, sells []tradebinTypes.AggregatedOrder) (msg *tradebinTypes.MsgCreateOrder, orderAmount sdk.Int, msgs []*tradebinTypes.MsgCreateOrder) {
	var bookOrder tradebinTypes.AggregatedOrder
	if strategy.GetOrderType() == tradebinTypes.OrderTypeBuy {
		bookOrder = sells[0]
	} else {
		bookOrder = buys[0]
	}

	if v.isCarouselStrategy(strategy.GetType()) {
		return v.makeCarouselOrder(strategy, &bookOrder)
	}

	return v.makeSpreadOrder(strategy, buys, sells)
}

func (v *Volume) makeCarouselOrder(strategy volumeStrategy, bookOrder *tradebinTypes.AggregatedOrder) (msg *tradebinTypes.MsgCreateOrder, orderAmount sdk.Int, msgs []*tradebinTypes.MsgCreateOrder) {
	l := v.l.WithField("func", "makeCarouselOrder")
	msgs = []*tradebinTypes.MsgCreateOrder{}
	extraAmount := sdk.ZeroInt()
	var extraMsg *tradebinTypes.MsgCreateOrder
	if strategy.GetExtraMaxVolume().IsPositive() &&
		strategy.GetTradesCount()%v.cfg.GetExtraEvery() == 0 &&
		strategy.GetTradesCount() > 0 {
		l.Debug("extra volume is required. adding a new order to fill immediately")
		extraAmount = internal.MustRandomInt(strategy.GetExtraMinVolume(), strategy.GetExtraMaxVolume())
		if extraAmount.IsPositive() {
			extraMsg = tradebinTypes.NewMsgCreateOrder(
				v.addressProvider.GetAddress().String(),
				bookOrder.GetOrderType(),
				extraAmount.String(),
				bookOrder.Price,
				v.marketConfig.GetMarketId(),
			)

			l.WithField("extra_order_msg", extraMsg).Debug("extra order message created")

			msgs = append(msgs, extraMsg)
		}
	}

	orderAmount = internal.MustRandomInt(strategy.GetMinAmount(), strategy.GetMaxAmount())
	if !extraAmount.IsPositive() && orderAmount.GT(*strategy.GetRemainingAmount()) {
		l.Debug("order amount is greater than remaining amount")
		orderAmount = *strategy.GetRemainingAmount()
	}
	bookAmount, _ := sdk.NewIntFromString(bookOrder.Amount)

	if orderAmount.GT(bookAmount) {
		l.Debug("order amount is greater than book amount")
		orderAmount = bookAmount
	}

	msg = tradebinTypes.NewMsgCreateOrder(
		v.addressProvider.GetAddress().String(),
		strategy.GetOrderType(),
		orderAmount.Add(extraAmount).String(),
		bookOrder.Price,
		v.marketConfig.GetMarketId(),
	)

	msgs = append(msgs, msg)

	return msg, orderAmount, msgs
}

func (v *Volume) makeSpreadOrder(strategy volumeStrategy, buys, sells []tradebinTypes.AggregatedOrder) (msg *tradebinTypes.MsgCreateOrder, orderAmount sdk.Int, msgs []*tradebinTypes.MsgCreateOrder) {
	l := v.l.WithField("func", "makeSpreadOrder")
	bookBuyPrice := sdk.MustNewDecFromStr(buys[0].Price)
	bookSellPrice := sdk.MustNewDecFromStr(sells[0].Price)

	var priceDec sdk.Dec
	if strategy.GetOrderType() == tradebinTypes.OrderTypeBuy {
		priceDec = bookSellPrice.Sub(*v.orderConfig.GetPriceStepDec())
	} else {
		priceDec = bookBuyPrice.Add(*v.orderConfig.GetPriceStepDec())
	}

	if !priceDec.IsPositive() || priceDec.LT(bookBuyPrice) || priceDec.GT(bookSellPrice) {
		l.
			WithField("priceDec", priceDec.String()).
			WithField("bookBuyPrice", bookBuyPrice.String()).
			WithField("bookSellPrice", bookSellPrice.String()).
			Errorf("resulted price for spread order not possible")

		return
	}

	price := priceDec.String()

	msgs = []*tradebinTypes.MsgCreateOrder{}
	extraAmount := sdk.ZeroInt()
	if strategy.GetExtraMaxVolume().IsPositive() &&
		strategy.GetTradesCount()%v.cfg.GetExtraEvery() == 0 &&
		strategy.GetTradesCount() > 0 {
		l.Debug("extra volume is required. adding a new order to fill immediately")
		extraAmount = internal.MustRandomInt(strategy.GetExtraMinVolume(), strategy.GetExtraMaxVolume())
	}

	orderAmount = internal.MustRandomInt(strategy.GetMinAmount(), strategy.GetMaxAmount())
	if orderAmount.GT(*strategy.GetRemainingAmount()) {
		l.Debug("order amount is greater than remaining amount")
		orderAmount = *strategy.GetRemainingAmount()
	}

	msg = tradebinTypes.NewMsgCreateOrder(
		v.addressProvider.GetAddress().String(),
		strategy.GetOrderType(),
		orderAmount.Add(extraAmount).String(),
		price,
		v.marketConfig.GetMarketId(),
	)

	msgs = append(msgs, msg)

	secondMsg := tradebinTypes.NewMsgCreateOrder(
		v.addressProvider.GetAddress().String(),
		tradebinTypes.TheOtherOrderType(strategy.GetOrderType()),
		orderAmount.Add(extraAmount).String(),
		price,
		v.marketConfig.GetMarketId(),
	)

	msgs = append(msgs, secondMsg)

	return msg, orderAmount, msgs
}

// getStrategy returns the appropriate volumeStrategy to use on the next trade
// If the strategy does not exist it creates a new one
// If orders needed for this strategy are missing from the order book it creates a new one
// If the remaining amount of this trade is lower than the required minimum then it creates a new strategy
func (v *Volume) getStrategy(balances *dto.MarketBalance, buys, sells []tradebinTypes.AggregatedOrder) (volumeStrategy, error) {
	l := v.l.WithField("func", "getStrategy")
	myBuys, mySells, err := v.ordersProvider.GetAddressActiveOrders(balances.MarketId, v.addressProvider.GetAddress().String(), 1000)
	if err != nil {
		return nil, err
	}

	if v.strategy == nil {
		l.Debug("no volume strategy found yet. creating new one")
		v.strategy, err = v.newStrategy(balances, myBuys, mySells, buys, sells)

		return v.strategy, err
	}

	if v.strategy.GetMinAmount().GT(*v.strategy.GetRemainingAmount()) {
		l.Debug("strategy remaining amount is too low. creating new one")
		v.strategy, err = v.newStrategy(balances, myBuys, mySells, buys, sells)

		return v.strategy, err
	}

	if !v.isCarouselStrategy(v.cfg.GetStrategy()) {
		//for spread strategy we can trade no matter who owns the sell/buy book order
		//because spread strategy buys and sells from its own orders within the spread

		return v.strategy, nil
	}

	if v.strategy.GetOrderType() == tradebinTypes.OrderTypeBuy {
		if len(sells) == 0 {
			l.Debug("can not use strategy due to missing buy orders. creating new one")
			v.strategy, err = v.newStrategy(balances, myBuys, mySells, buys, sells)

			return v.strategy, err
		}

		if !v.isMyOrder(mySells, sells[0]) {
			l.Debug("can not use buy strategy because we do not own the first sell. creating new one")
			v.strategy, err = v.newStrategy(balances, myBuys, mySells, buys, sells)

			return v.strategy, err
		}
	}

	if v.strategy.GetOrderType() == tradebinTypes.OrderTypeSell {
		if len(buys) == 0 {
			l.Debug("can not use strategy due to missing buy orders. creating new one")
			v.strategy, err = v.newStrategy(balances, myBuys, mySells, buys, sells)

			return v.strategy, err
		}

		if !v.isMyOrder(myBuys, buys[0]) {
			l.Debug("can not use sell strategy because we do not own the first buy. creating new one")
			v.strategy, err = v.newStrategy(balances, myBuys, mySells, buys, sells)

			return v.strategy, err
		}
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

	if v.isCarouselStrategy(v.cfg.GetStrategy()) {
		//only carousel strategy buys/sells to self orders, otherwise we trade in spread and do not care who's order it is

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
		if my.Price == order.Price && my.OrderType == order.OrderType && my.Amount == order.Amount {
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

	extraMin := sdk.NewInt(v.cfg.GetExtraMin())
	extraMax := sdk.NewInt(v.cfg.GetExtraMax())

	var tradesCount int64
	if v.strategy != nil {
		tradesCount = v.strategy.GetTradesCount()
	}

	return &dto.VolumeStrategy{
		MinVolume:       &minVolume,
		MaxVolume:       &maxVolume,
		RemainingVolume: &remaining,
		OrderType:       orderType,
		LastRun:         &time.Time{},
		ExtraMinVolume:  &extraMin,
		ExtraMaxVolume:  &extraMax,
		TradesCount:     tradesCount,
		Type:            v.cfg.GetStrategy(),
	}
}

func (v *Volume) isCarouselStrategy(strategyType string) bool {
	return strategyType == dto.CarouselType
}
