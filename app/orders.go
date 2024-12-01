package app

import (
	"fmt"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
	"sync"
	"tradebin-mm/app/data_provider"
	"tradebin-mm/app/dto"
	"tradebin-mm/app/internal"
)

const (
	cancelOrdersDelta = 5
)

type balanceProvider interface {
	GetAddressBalancesForMarket(address string, marketId data_provider.MarketProvider) (*dto.MarketBalance, error)
}

type addressProvider interface {
	GetAddress() types.AccAddress
}

type ordersProvider interface {
	GetActiveBuyOrders(marketId string) ([]tradebinTypes.AggregatedOrder, error)
	GetActiveSellOrders(marketId string) ([]tradebinTypes.AggregatedOrder, error)
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

func (o *Orders) FillOrderBook() error {
	balances, err := o.getBalances()
	if err != nil {
		return fmt.Errorf("failed to get balances: %v", err)
	}

	requiredOrders := o.ordersConfig.GetBuyNo() + o.ordersConfig.GetSellNo()
	myBuys, mySells, err := o.ordersProvider.GetAddressActiveOrders(balances.MarketId, o.addressProvider.GetAddress().String(), requiredOrders*2)
	if err != nil {
		return fmt.Errorf("failed to get address active orders: %v", err)
	}

	myOrdersCount := len(myBuys) + len(mySells)
	if myOrdersCount > (requiredOrders + cancelOrdersDelta) {
		o.l.Info("too many orders placed, cancelling some")

		err = o.cancelExtraOrders(myBuys, mySells)
		if err != nil {
			o.l.WithError(err).Errorf("failed to cancel extra orders")
		}
	}

	buys, sells, err := o.getActiveOrders(balances)
	if err != nil {
		return err
	}

	bBuy, sSell := o.getSpread(buys, sells)
	startPrice := o.getStartPrice(o.marketConfig.GetMarketId(), bBuy, sSell)

	err = o.fillOrders(sells, startPrice, tradebinTypes.OrderTypeSell, o.ordersConfig.GetSellNo(), o.buildPricesMap(mySells))
	if err != nil {
		return fmt.Errorf("failed to fill sell orders: %v", err)
	}

	step := o.ordersConfig.GetPriceStepDec()
	buyStartPrice := startPrice.Sub(*step)
	err = o.fillOrders(buys, &buyStartPrice, tradebinTypes.OrderTypeBuy, o.ordersConfig.GetBuyNo(), o.buildPricesMap(myBuys))
	if err != nil {
		return fmt.Errorf("failed to fill sell orders: %v", err)
	}

	return nil
}

func (o *Orders) buildPricesMap(orders []tradebinTypes.Order) map[string]struct{} {
	prices := make(map[string]struct{})
	for _, order := range orders {
		//for safety, we convert it to dec and trim trailing zeros to make sure the prices are unique
		orderPriceDec := types.MustNewDecFromStr(order.Price)
		prices[internal.TrimAmountTrailingZeros(orderPriceDec.String())] = struct{}{}
	}

	return prices
}

func (o *Orders) getBalances() (*dto.MarketBalance, error) {
	balances, err := o.balanceProvider.GetAddressBalancesForMarket(o.addressProvider.GetAddress().String(), o.marketConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to get address balances for market: %v", err)
	}

	if balances == nil {
		return nil, fmt.Errorf("failed to get address balances for market")
	}

	if balances.QuoteBalance == nil || !balances.QuoteBalance.IsPositive() {
		return nil, fmt.Errorf("no balance found for %s", o.marketConfig.GetQuoteDenom())
	}

	if balances.BaseBalance == nil || !balances.BaseBalance.IsPositive() {
		return nil, fmt.Errorf("no balance found for %s", o.marketConfig.GetQuoteDenom())
	}

	return balances, nil
}

func (o *Orders) fillOrders(existingOrders []tradebinTypes.AggregatedOrder, startPrice *types.Dec, orderType string, neededOrders int, excludedPrices map[string]struct{}) error {
	l := o.l.WithField("orderType", orderType)

	l.Info("start building order add messages")
	if neededOrders <= 0 {
		l.Info("no orders needed to be placed")
		return nil
	}

	minAmount := o.ordersConfig.GetOrderMinAmount()
	maxAmount := o.ordersConfig.GetOrderMaxAmount()
	newStartPrice := *startPrice
	var newOrdersMsgs []*tradebinTypes.MsgCreateOrder
	l.Info("not enough existing orders to fill the needed number of orders")
	for neededOrders > 0 {
		//excluded prices ar the prices we already have an order for
		if _, ok := excludedPrices[internal.TrimAmountTrailingZeros(newStartPrice.String())]; ok {
			l.WithField("excluded_prices", excludedPrices).Debugf("found excluded price: %s", newStartPrice.String())

			if orderType == tradebinTypes.OrderTypeBuy {
				newStartPrice = newStartPrice.Sub(*o.ordersConfig.GetPriceStepDec())
			} else {
				newStartPrice = newStartPrice.Add(*o.ordersConfig.GetPriceStepDec())
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
				o.addressProvider.GetAddress().String(),
				orderType,
				randAmount.String(),
				internal.TrimAmountTrailingZeros(newStartPrice.String()),
				existingOrders[0].MarketId,
			)
			newOrdersMsgs = append(newOrdersMsgs, msg)
			neededOrders--
		}

		if orderType == tradebinTypes.OrderTypeBuy {
			newStartPrice = newStartPrice.Sub(*o.ordersConfig.GetPriceStepDec())
		} else {
			newStartPrice = newStartPrice.Add(*o.ordersConfig.GetPriceStepDec())
		}
	}

	if len(newOrdersMsgs) == 0 {
		l.Debug("no new orders to fill")
		return nil
	}

	l.Info("submitting new orders")

	return o.orderSubmitter.AddOrders(newOrdersMsgs)
}

func (o *Orders) getStartPrice(marketId string, biggestBuy *types.Dec, smallestSell *types.Dec) *types.Dec {
	history, err := o.ordersProvider.GetLastMarketOrder(marketId)
	if history == nil {
		//in this case fallback on taking the price from the spread
		if err != nil {
			o.l.WithError(err).Error("failed to get last order for market")
		}

		return o.getStartPriceFromSpread(biggestBuy, smallestSell)
	}

	histPrice := types.MustNewDecFromStr(history.Price)
	if !histPrice.IsPositive() {

		return o.getStartPriceFromSpread(biggestBuy, smallestSell)
	}

	if history.GetOrderType() == tradebinTypes.OrderTypeSell {
		step := o.ordersConfig.GetPriceStepDec()
		start := histPrice.Add(*step)

		return &start
	}

	return &histPrice
}

func (o *Orders) getStartPriceFromSpread(biggestBuy *types.Dec, smallestSell *types.Dec) *types.Dec {
	if biggestBuy.IsZero() && smallestSell.IsZero() {
		//start from configured price
		return o.ordersConfig.GetStartPriceDec()
	}

	if biggestBuy.IsZero() {
		//start from the smallest sell, there's no buy to use
		return smallestSell
	}
	step := o.ordersConfig.GetPriceStepDec()

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

func (o *Orders) cancelExtraOrders(buys []tradebinTypes.Order, sells []tradebinTypes.Order) error {
	if len(buys) > o.ordersConfig.GetBuyNo() {
		internal.SortOrdersByPrice(buys, false)
		err := o.cancelOrders(buys, len(buys)-o.ordersConfig.GetBuyNo())
		if err != nil {
			return fmt.Errorf("failed to cancel extra buy orders: %v", err)
		}
	}

	if len(sells) > o.ordersConfig.GetSellNo() {
		internal.SortOrdersByPrice(sells, true)
		err := o.cancelOrders(sells, len(sells)-o.ordersConfig.GetSellNo())
		if err != nil {
			return fmt.Errorf("failed to cancel extra sell orders: %v", err)
		}
	}

	return nil
}

func (o *Orders) cancelOrders(sortedOrders []tradebinTypes.Order, limit int) error {
	var msgs []*tradebinTypes.MsgCancelOrder
	for _, order := range sortedOrders[:limit] {
		m := tradebinTypes.NewMsgCancelOrder(o.addressProvider.GetAddress().String(), order.MarketId, order.Id, order.OrderType)
		msgs = append(msgs, m)
	}

	err := o.orderSubmitter.CancelOrders(msgs)
	if err != nil {
		return fmt.Errorf("failed to cancel order: %v", err)
	}

	return nil
}

func (o *Orders) getSpread(buys, sells []tradebinTypes.AggregatedOrder) (biggestBuy *types.Dec, smallestSell *types.Dec) {
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

func (o *Orders) getActiveOrders(mb *dto.MarketBalance) (buys, sells []tradebinTypes.AggregatedOrder, err error) {
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		var rErr error
		buys, rErr = o.ordersProvider.GetActiveBuyOrders(mb.MarketId)
		if rErr != nil {
			err = fmt.Errorf("failed to get active buy orders: %v", rErr)
		}
	}()

	go func() {
		defer wg.Done()
		var rErr error
		sells, rErr = o.ordersProvider.GetActiveSellOrders(mb.MarketId)
		if rErr != nil {
			err = fmt.Errorf("failed to get active buy orders: %v", rErr)
		}
	}()

	wg.Wait()

	return buys, sells, err
}
