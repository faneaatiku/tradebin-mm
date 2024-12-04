package app

import (
	"fmt"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
	"tradebin-mm/app/data_provider"
	"tradebin-mm/app/dto"
	"tradebin-mm/app/internal"
)

const (
	cancelOrdersDelta = 5
)

type balanceProvider interface {
	GetAddressBalancesForMarket(address string, marketId data_provider.MarketProvider) (*dto.MarketBalance, error)
	GetMarketBalance(address string, marketCfg data_provider.MarketProvider) (*dto.MarketBalance, error)
}

type addressProvider interface {
	GetAddress() types.AccAddress
}

type ordersProvider interface {
	GetActiveOrders(mb *dto.MarketBalance) (buys, sells []tradebinTypes.AggregatedOrder, err error)
	GetAddressActiveOrders(marketId, address string, limit int) (buys, sells []tradebinTypes.Order, err error)
	GetLastMarketOrder(marketId string) (*tradebinTypes.HistoryOrder, error)
}

type orderSubmitter interface {
	CancelOrders([]*tradebinTypes.MsgCancelOrder) error
	AddOrders([]*tradebinTypes.MsgCreateOrder) error
}

type ordersConfig interface {
	GetBuyNo() int
	GetSellNo() int
	GetStartPriceDec() *types.Dec
	GetPriceStepDec() *types.Dec
	GetOrderMinAmount() *types.Int
	GetOrderMaxAmount() *types.Int
}

type Orders struct {
	ordersConfig ordersConfig
	marketConfig data_provider.MarketProvider

	balanceProvider balanceProvider
	addressProvider addressProvider
	ordersProvider  ordersProvider
	orderSubmitter  orderSubmitter

	l logrus.FieldLogger
}

func NewOrdersFiller(
	l logrus.FieldLogger,
	ordersConfig ordersConfig,
	marketConfig data_provider.MarketProvider,
	balanceProvider balanceProvider,
	addressProvider addressProvider,
	ordersProvider ordersProvider,
	orderSubmitter orderSubmitter,
) (*Orders, error) {
	if ordersConfig == nil || marketConfig == nil || balanceProvider == nil || addressProvider == nil || ordersProvider == nil || orderSubmitter == nil {
		return nil, internal.NewInvalidDependenciesErr("NewOrdersFiller")
	}

	return &Orders{
		ordersConfig:    ordersConfig,
		marketConfig:    marketConfig,
		balanceProvider: balanceProvider,
		addressProvider: addressProvider,
		ordersProvider:  ordersProvider,
		orderSubmitter:  orderSubmitter,
		l:               l.WithField("service", "OrdersFiller"),
	}, nil
}

func (v *Orders) FillOrderBook() error {
	balances, err := v.balanceProvider.GetMarketBalance(v.addressProvider.GetAddress().String(), v.marketConfig)
	if err != nil {
		return fmt.Errorf("failed to get balances: %v", err)
	}

	requiredOrders := v.ordersConfig.GetBuyNo() + v.ordersConfig.GetSellNo()
	myBuys, mySells, err := v.ordersProvider.GetAddressActiveOrders(balances.MarketId, v.addressProvider.GetAddress().String(), requiredOrders*2)
	if err != nil {
		return fmt.Errorf("failed to get address active orders: %v", err)
	}

	myOrdersCount := len(myBuys) + len(mySells)
	if myOrdersCount > (requiredOrders + cancelOrdersDelta) {
		v.l.Info("too many orders placed, cancelling some")

		err = v.cancelExtraOrders(myBuys, mySells)
		if err != nil {
			v.l.WithError(err).Errorf("failed to cancel extra orders")
		}
	}

	buys, sells, err := v.ordersProvider.GetActiveOrders(balances)
	if err != nil {
		return err
	}

	bBuy, sSell := v.getSpread(buys, sells)
	startPrice := v.getStartPrice(v.marketConfig.GetMarketId(), bBuy, sSell)

	err = v.fillOrders(sells, startPrice, tradebinTypes.OrderTypeSell, v.ordersConfig.GetSellNo(), v.buildPricesMap(mySells))
	if err != nil {
		return fmt.Errorf("failed to fill sell orders: %v", err)
	}

	step := v.ordersConfig.GetPriceStepDec()
	buyStartPrice := startPrice.Sub(*step)
	err = v.fillOrders(buys, &buyStartPrice, tradebinTypes.OrderTypeBuy, v.ordersConfig.GetBuyNo(), v.buildPricesMap(myBuys))
	if err != nil {
		return fmt.Errorf("failed to fill sell orders: %v", err)
	}

	return nil
}

func (v *Orders) buildPricesMap(orders []tradebinTypes.Order) map[string]struct{} {
	prices := make(map[string]struct{})
	for _, order := range orders {
		//for safety, we convert it to dec and trim trailing zeros to make sure the prices are unique
		orderPriceDec := types.MustNewDecFromStr(order.Price)
		prices[internal.TrimAmountTrailingZeros(orderPriceDec.String())] = struct{}{}
	}

	return prices
}

func (v *Orders) fillOrders(existingOrders []tradebinTypes.AggregatedOrder, startPrice *types.Dec, orderType string, neededOrders int, excludedPrices map[string]struct{}) error {
	l := v.l.WithField("orderType", orderType)

	l.Info("start building order add messages")
	if neededOrders <= 0 {
		l.Info("no orders needed to be placed")
		return nil
	}

	minAmount := v.ordersConfig.GetOrderMinAmount()
	maxAmount := v.ordersConfig.GetOrderMaxAmount()
	newStartPrice := *startPrice
	var newOrdersMsgs []*tradebinTypes.MsgCreateOrder
	l.Info("not enough existing orders to fill the needed number of orders")
	for neededOrders > 0 {
		//excluded prices ar the prices we already have an order for
		if _, ok := excludedPrices[internal.TrimAmountTrailingZeros(newStartPrice.String())]; ok {
			if orderType == tradebinTypes.OrderTypeBuy {
				newStartPrice = newStartPrice.Sub(*v.ordersConfig.GetPriceStepDec())
			} else {
				newStartPrice = newStartPrice.Add(*v.ordersConfig.GetPriceStepDec())
			}

			neededOrders--
			continue
		}

		shouldPlace := true
		for _, existing := range existingOrders {
			if existing.OrderType != orderType {
				return fmt.Errorf("expected order type to be %s but encountered order of different type: %s", orderType, existing.OrderType)
			}

			existingPrice := types.MustNewDecFromStr(existing.Price)
			if existingPrice.Equal(newStartPrice) {
				shouldPlace = false
				break
			}

			if orderType == tradebinTypes.OrderTypeBuy && existingPrice.LT(newStartPrice) {
				shouldPlace = true
				break
			} else if orderType == tradebinTypes.OrderTypeSell && existingPrice.GT(newStartPrice) {
				shouldPlace = true
				break
			}
		}

		if shouldPlace {
			randAmount := internal.MustRandomInt(minAmount, maxAmount)
			msg := tradebinTypes.NewMsgCreateOrder(
				v.addressProvider.GetAddress().String(),
				orderType,
				randAmount.String(),
				internal.TrimAmountTrailingZeros(newStartPrice.String()),
				existingOrders[0].MarketId,
			)
			newOrdersMsgs = append(newOrdersMsgs, msg)
			neededOrders--
		}

		if orderType == tradebinTypes.OrderTypeBuy {
			newStartPrice = newStartPrice.Sub(*v.ordersConfig.GetPriceStepDec())
		} else {
			newStartPrice = newStartPrice.Add(*v.ordersConfig.GetPriceStepDec())
		}
	}

	if len(newOrdersMsgs) == 0 {
		l.Debug("no new orders to fill")
		return nil
	}

	l.Info("submitting new orders")

	return v.orderSubmitter.AddOrders(newOrdersMsgs)
}

func (v *Orders) getStartPrice(marketId string, biggestBuy *types.Dec, smallestSell *types.Dec) *types.Dec {
	history, err := v.ordersProvider.GetLastMarketOrder(marketId)
	if history == nil {
		//in this case fallback on taking the price from the spread
		if err != nil {
			v.l.WithError(err).Error("failed to get last order for market")
		}

		return v.getStartPriceFromSpread(biggestBuy, smallestSell)
	}

	histPrice := types.MustNewDecFromStr(history.Price)
	if !histPrice.IsPositive() {

		return v.getStartPriceFromSpread(biggestBuy, smallestSell)
	}

	if history.GetOrderType() == tradebinTypes.OrderTypeSell {
		step := v.ordersConfig.GetPriceStepDec()
		start := histPrice.Add(*step)

		return &start
	}

	return &histPrice
}

func (v *Orders) getStartPriceFromSpread(biggestBuy *types.Dec, smallestSell *types.Dec) *types.Dec {
	if biggestBuy.IsZero() && smallestSell.IsZero() {
		//start from configured price
		return v.ordersConfig.GetStartPriceDec()
	}

	if biggestBuy.IsZero() {
		//start from the smallest sell, there's no buy to use
		return smallestSell
	}
	step := v.ordersConfig.GetPriceStepDec()

	if smallestSell.IsZero() {
		//start from the biggest buy price + step
		start := biggestBuy.Add(*step)

		return &start
	}

	//let's see the diff between buy and sell prices
	diff := smallestSell.Sub(*biggestBuy)
	if diff.LTE(*step) {
		return smallestSell
	}

	// calculate how many steps are between the biggest buy and the smallest sell and divide by two to find the middle
	noOfSteps := diff.Quo(*step).Quo(types.NewDec(2)).TruncateDec()
	start := smallestSell.Sub(step.Mul(noOfSteps))

	return &start
}

func (v *Orders) cancelExtraOrders(buys []tradebinTypes.Order, sells []tradebinTypes.Order) error {
	if len(buys) > v.ordersConfig.GetBuyNo() {
		internal.SortOrdersByPrice(buys, false)
		err := v.cancelOrders(buys, len(buys)-v.ordersConfig.GetBuyNo())
		if err != nil {
			return fmt.Errorf("failed to cancel extra buy orders: %v", err)
		}
	}

	if len(sells) > v.ordersConfig.GetSellNo() {
		internal.SortOrdersByPrice(sells, true)
		err := v.cancelOrders(sells, len(sells)-v.ordersConfig.GetSellNo())
		if err != nil {
			return fmt.Errorf("failed to cancel extra sell orders: %v", err)
		}
	}

	return nil
}

func (v *Orders) cancelOrders(sortedOrders []tradebinTypes.Order, limit int) error {
	var msgs []*tradebinTypes.MsgCancelOrder
	for _, order := range sortedOrders[:limit] {
		m := tradebinTypes.NewMsgCancelOrder(v.addressProvider.GetAddress().String(), order.MarketId, order.Id, order.OrderType)
		msgs = append(msgs, m)
	}

	err := v.orderSubmitter.CancelOrders(msgs)
	if err != nil {
		return fmt.Errorf("failed to cancel order: %v", err)
	}

	return nil
}

func (v *Orders) getSpread(buys, sells []tradebinTypes.AggregatedOrder) (biggestBuy *types.Dec, smallestSell *types.Dec) {
	b := types.ZeroDec()
	s := types.ZeroDec()

	if len(buys) > 0 {
		b = types.MustNewDecFromStr(buys[0].Price)
	}

	if len(sells) > 0 {
		s = types.MustNewDecFromStr(sells[0].Price)
	}

	return &b, &s
}
