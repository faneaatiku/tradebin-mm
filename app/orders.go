package app

import (
	"fmt"
	stdmath "math"
	"time"
	"tradebin-mm/app/data_provider"
	"tradebin-mm/app/dto"
	"tradebin-mm/app/internal"

	"cosmossdk.io/math"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
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
	FillOrders(*tradebinTypes.MsgFillOrders) error
}

type ordersVolumeConfig interface {
	GetUseLiquidityPool() bool
}

type ordersLiquidityPoolProvider interface {
	GetPool(poolId string) (*tradebinTypes.LiquidityPool, error)
	GetSpotPrice(pool *tradebinTypes.LiquidityPool) (*math.LegacyDec, error)
}

type ordersConfig interface {
	GetBuyNo() int
	GetSellNo() int
	GetStartPriceDec() *math.LegacyDec
	GetPriceStepDec() *math.LegacyDec
	GetOrderMinAmount() *math.Int
	GetOrderMaxAmount() *math.Int
	GetSpreadSteps() *math.Int
	GetHoldBackSeconds() int

	// CLMM strategy methods
	GetLiquidityStrategy() string
	GetRangeType() string
	GetRangePct() float64
	GetRangeLowerDec() *math.LegacyDec
	GetRangeUpperDec() *math.LegacyDec
	GetConcentrationFactor() float64
}

type Orders struct {
	ordersConfig ordersConfig
	volumeConfig ordersVolumeConfig
	marketConfig data_provider.MarketProvider

	balanceProvider balanceProvider
	addressProvider addressProvider
	ordersProvider  ordersProvider
	orderSubmitter  orderSubmitter

	// Liquidity pool integration
	lpProvider ordersLiquidityPoolProvider

	l logrus.FieldLogger
}

func NewOrdersFiller(
	l logrus.FieldLogger,
	ordersConfig ordersConfig,
	volumeConfig ordersVolumeConfig,
	marketConfig data_provider.MarketProvider,
	balanceProvider balanceProvider,
	addressProvider addressProvider,
	ordersProvider ordersProvider,
	orderSubmitter orderSubmitter,
	lpProvider ordersLiquidityPoolProvider,
) (*Orders, error) {
	if ordersConfig == nil || volumeConfig == nil || marketConfig == nil || balanceProvider == nil || addressProvider == nil || ordersProvider == nil || orderSubmitter == nil {
		return nil, internal.NewInvalidDependenciesErr("NewOrdersFiller")
	}

	// LP provider is optional - only required if use_liquidity_pool is enabled
	if volumeConfig.GetUseLiquidityPool() && lpProvider == nil {
		return nil, internal.NewInvalidDependenciesErr("NewOrdersFiller: LP provider required when use_liquidity_pool is enabled")
	}

	return &Orders{
		ordersConfig:    ordersConfig,
		volumeConfig:    volumeConfig,
		marketConfig:    marketConfig,
		balanceProvider: balanceProvider,
		addressProvider: addressProvider,
		ordersProvider:  ordersProvider,
		orderSubmitter:  orderSubmitter,
		lpProvider:      lpProvider,
		l:               l.WithField("service", "OrdersFiller"),
	}, nil
}

func (v *Orders) FillOrderBook() error {
	strategy := v.ordersConfig.GetLiquidityStrategy()

	if strategy == "clmm" {
		return v.fillOrderBookCLMM()
	}

	// Default: use fixed grid strategy
	return v.fillOrderBookFixed()
}

func (v *Orders) fillOrderBookFixed() error {
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

	if !v.shouldFillOrderBook(balances.MarketId) {
		v.l.Info("should not fill order book yet. hold back.")

		return nil
	}

	buys, sells, err := v.ordersProvider.GetActiveOrders(balances)
	if err != nil {
		return err
	}

	bBuy, sSell := v.getSpread(buys, sells)
	toTruncate := v.getStartPrice(v.marketConfig.GetMarketId(), bBuy, sSell)
	v.l.WithField("start_price_raw", toTruncate).Debug("found start price")
	step := v.ordersConfig.GetPriceStepDec()

	startPrice, err := internal.TruncateToStep(toTruncate, step)
	if err != nil {
		return fmt.Errorf("failed to truncate start price to step: %v", err)
	}
	v.l.WithField("start_price", startPrice).Debug("start price truncated")

	err = v.fillOrders(sells, startPrice, tradebinTypes.OrderTypeSell, v.ordersConfig.GetSellNo(), v.buildPricesMap(mySells))
	if err != nil {
		return fmt.Errorf("failed to fill sell orders: %v", err)
	}

	buyStartPrice := startPrice.Sub(*step)
	err = v.fillOrders(buys, &buyStartPrice, tradebinTypes.OrderTypeBuy, v.ordersConfig.GetBuyNo(), v.buildPricesMap(myBuys))
	if err != nil {
		return fmt.Errorf("failed to fill buy orders: %v", err)
	}

	return nil
}

func (v *Orders) fillOrderBookCLMM() error {
	l := v.l.WithField("func", "fillOrderBookCLMM")

	// Get balances
	balances, err := v.balanceProvider.GetMarketBalance(v.addressProvider.GetAddress().String(), v.marketConfig)
	if err != nil {
		return fmt.Errorf("failed to get balances: %v", err)
	}

	// Get my existing orders
	requiredOrders := v.ordersConfig.GetBuyNo() + v.ordersConfig.GetSellNo()
	myBuys, mySells, err := v.ordersProvider.GetAddressActiveOrders(balances.MarketId, v.addressProvider.GetAddress().String(), requiredOrders*2)
	if err != nil {
		return fmt.Errorf("failed to get address active orders: %v", err)
	}

	// Cancel extra orders if too many
	myOrdersCount := len(myBuys) + len(mySells)
	if myOrdersCount > (requiredOrders + cancelOrdersDelta) {
		l.Info("too many orders placed, cancelling some")
		err = v.cancelExtraOrders(myBuys, mySells)
		if err != nil {
			l.WithError(err).Errorf("failed to cancel extra orders")
		}
	}

	// Check hold-back timer
	if !v.shouldFillOrderBook(balances.MarketId) {
		l.Info("should not fill order book yet. hold back.")
		return nil
	}

	// Get market orders
	buys, sells, err := v.ordersProvider.GetActiveOrders(balances)
	if err != nil {
		return err
	}

	// Calculate mid-price from order book
	orderBookMidPrice := v.calculateMidPrice(buys, sells)

	// Get LP spot price if enabled
	var lpSpotPrice *math.LegacyDec
	if v.volumeConfig.GetUseLiquidityPool() && v.lpProvider != nil {
		poolId := data_provider.GeneratePoolId(
			v.marketConfig.GetBaseDenom(),
			v.marketConfig.GetQuoteDenom(),
		)

		pool, err := v.lpProvider.GetPool(poolId)
		if err != nil {
			l.WithError(err).Warn("failed to get LP for CLMM mid-price")
		} else if pool != nil {
			spotPrice, err := v.lpProvider.GetSpotPrice(pool)
			if err != nil {
				l.WithError(err).Warn("failed to calculate LP spot price for CLMM")
			} else {
				lpSpotPrice = spotPrice
				l.WithField("lp_spot_price", lpSpotPrice.String()).Debug("fetched LP spot price for CLMM")
			}
		}
	}

	// Select mid-price: use MINIMUM of LP spot price vs order book mid-price
	var midPrice *math.LegacyDec
	if orderBookMidPrice != nil && lpSpotPrice != nil {
		// Both exist - use the SMALLER one
		if lpSpotPrice.LT(*orderBookMidPrice) {
			midPrice = lpSpotPrice
			l.WithFields(map[string]interface{}{
				"lp_spot_price":       lpSpotPrice.String(),
				"orderbook_mid_price": orderBookMidPrice.String(),
				"selected":            "lp_spot_price (smaller)",
			}).Info("using LP spot price (smaller than order book mid-price)")
		} else {
			midPrice = orderBookMidPrice
			l.WithFields(map[string]interface{}{
				"lp_spot_price":       lpSpotPrice.String(),
				"orderbook_mid_price": orderBookMidPrice.String(),
				"selected":            "orderbook_mid_price (smaller)",
			}).Info("using order book mid-price (smaller than LP spot price)")
		}
	} else if lpSpotPrice != nil {
		// Only LP exists
		midPrice = lpSpotPrice
		l.WithField("lp_spot_price", lpSpotPrice.String()).Info("using LP spot price (no order book)")
	} else if orderBookMidPrice != nil {
		// Only order book exists
		midPrice = orderBookMidPrice
		l.WithField("orderbook_mid_price", orderBookMidPrice.String()).Info("using order book mid-price (no LP)")
	} else {
		// Neither exists - fallback to config
		midPrice = v.ordersConfig.GetStartPriceDec()
		l.WithField("start_price", midPrice.String()).Info("using config start price (no LP or order book)")
	}

	l.WithField("mid_price", midPrice.String()).Debug("calculated mid-price")

	// Calculate price range bounds
	lowerBound, upperBound, err := v.calculateCLMMRange(*midPrice)
	if err != nil {
		return fmt.Errorf("failed to calculate CLMM range: %w", err)
	}

	l.WithFields(map[string]interface{}{
		"lower_bound": lowerBound.String(),
		"upper_bound": upperBound.String(),
	}).Debug("calculated price range")

	// Calculate inventory ratio for balance-aware distribution
	inventoryRatio := v.calculateInventoryRatio(balances, *midPrice)
	l.WithField("inventory_ratio", inventoryRatio).Debug("calculated inventory ratio")

	// Generate price levels with step snapping
	concentrationFactor := v.ordersConfig.GetConcentrationFactor()
	step := v.ordersConfig.GetPriceStepDec()
	buyPrices := v.generateCLMMPrices(lowerBound, *midPrice, v.ordersConfig.GetBuyNo(), concentrationFactor, step)
	sellPrices := v.generateCLMMPrices(*midPrice, upperBound, v.ordersConfig.GetSellNo(), concentrationFactor, step)

	// Calculate liquidity amounts per price level
	minAmount := v.ordersConfig.GetOrderMinAmount()
	maxAmount := v.ordersConfig.GetOrderMaxAmount()
	buyAmounts := v.calculateCLMMLiquidity(buyPrices, *midPrice, inventoryRatio, concentrationFactor, *minAmount, *maxAmount)
	sellAmounts := v.calculateCLMMLiquidity(sellPrices, *midPrice, inventoryRatio, concentrationFactor, *minAmount, *maxAmount)

	// Collect all order creation messages
	buyMsgs, err := v.placeOrdersAtLevels(buyPrices, buyAmounts, tradebinTypes.OrderTypeBuy, myBuys, buys)
	if err != nil {
		return fmt.Errorf("failed to create buy orders: %w", err)
	}

	sellMsgs, err := v.placeOrdersAtLevels(sellPrices, sellAmounts, tradebinTypes.OrderTypeSell, mySells, sells)
	if err != nil {
		return fmt.Errorf("failed to create sell orders: %w", err)
	}

	// Combine all create messages
	allCreateMsgs := append(buyMsgs, sellMsgs...)

	// Submit all creates in one transaction
	if len(allCreateMsgs) > 0 {
		err = v.orderSubmitter.AddOrders(allCreateMsgs)
		if err != nil {
			return fmt.Errorf("failed to submit orders: %w", err)
		}
		l.WithField("count", len(allCreateMsgs)).Info("submitted all orders in batch")
	}

	// Collect cancel messages
	cancelMsgs := v.cancelOutOfRangeOrders(lowerBound, upperBound, myBuys, mySells)

	// Wait for create transactions to be included in blocks before cancelling
	if len(cancelMsgs) > 0 {
		l.WithField("count", len(cancelMsgs)).Info("waiting 10 seconds before cancelling out-of-range orders")
		time.Sleep(10 * time.Second)

		err = v.orderSubmitter.CancelOrders(cancelMsgs)
		if err != nil {
			l.WithError(err).Error("failed to cancel out-of-range orders")
		} else {
			l.WithField("count", len(cancelMsgs)).Info("cancelled out-of-range orders")
		}
	}

	l.Info("CLMM order book filled successfully")
	return nil
}

func (v *Orders) buildPricesMap(orders []tradebinTypes.Order) map[string]struct{} {
	prices := make(map[string]struct{})
	for _, order := range orders {
		//for safety, we convert it to dec and trim trailing zeros to make sure the prices are unique
		orderPriceDec := math.LegacyMustNewDecFromStr(order.Price)
		prices[internal.TrimAmountTrailingZeros(orderPriceDec.String())] = struct{}{}
	}

	return prices
}

func (v *Orders) fillOrders(existingOrders []tradebinTypes.AggregatedOrder, startPrice *math.LegacyDec, orderType string, neededOrders int, excludedPrices map[string]struct{}) error {
	l := v.l.WithField("orderType", orderType)

	l.Info("start building order add messages")
	if neededOrders <= 0 {
		l.Info("no orders needed to be placed")
		return nil
	}

	minAmount := v.ordersConfig.GetOrderMinAmount()
	maxAmount := v.ordersConfig.GetOrderMaxAmount()
	newStartPrice := *startPrice
	//leave configured spread empty
	if v.ordersConfig.GetSpreadSteps().IsPositive() {
		//divide the total spread steps required to be empty in two (half for sells/half for buys)
		sideSpreadSteps := v.ordersConfig.GetSpreadSteps().QuoRaw(2)
		spreadTotal := v.ordersConfig.GetPriceStepDec().MulInt(sideSpreadSteps)
		if orderType == tradebinTypes.OrderTypeBuy {
			newStartPrice = newStartPrice.Sub(spreadTotal)
		} else {
			newStartPrice = newStartPrice.Add(spreadTotal)
		}
	}

	var newOrdersMsgs []*tradebinTypes.MsgCreateOrder
	l.Info("not enough existing orders to fill the needed number of orders")
	for neededOrders > 0 {
		//excluded prices are the prices we already have an order for
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

			existingPrice := math.LegacyMustNewDecFromStr(existing.Price)
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

		if !newStartPrice.IsPositive() {
			l.Info("reached a non positive start price. stop creating order messages")
			break
		}

		if shouldPlace {
			randAmount := internal.MustRandomInt(minAmount, maxAmount)
			msg := tradebinTypes.NewMsgCreateOrder(
				v.addressProvider.GetAddress().String(),
				orderType,
				randAmount.String(),
				internal.TrimAmountTrailingZeros(newStartPrice.String()),
				v.marketConfig.GetMarketId(),
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

func (v *Orders) getStartPrice(marketId string, biggestBuy *math.LegacyDec, smallestSell *math.LegacyDec) *math.LegacyDec {
	if v.ordersConfig.GetSpreadSteps().IsPositive() {
		//if the config requires an empty spread we take the price from the spread
		return v.getStartPriceFromSpread(biggestBuy, smallestSell)
	}

	history, err := v.ordersProvider.GetLastMarketOrder(marketId)
	if history == nil {
		//in this case fallback on taking the price from the spread
		if err != nil {
			v.l.WithError(err).Error("failed to get last order for market")
		}

		return v.getStartPriceFromSpread(biggestBuy, smallestSell)
	}

	histPrice := math.LegacyMustNewDecFromStr(history.Price)
	if !histPrice.IsPositive() || histPrice.GT(*smallestSell) || histPrice.LT(*biggestBuy) {

		return v.getStartPriceFromSpread(biggestBuy, smallestSell)
	}

	if history.GetOrderType() == tradebinTypes.OrderTypeSell {
		step := v.ordersConfig.GetPriceStepDec()
		start := histPrice.Add(*step)

		return &start
	}

	return &histPrice
}

func (v *Orders) getStartPriceFromSpread(biggestBuy *math.LegacyDec, smallestSell *math.LegacyDec) *math.LegacyDec {
	if biggestBuy.IsZero() && smallestSell.IsZero() {
		// Priority 1: Try LP spot price if enabled
		if v.volumeConfig.GetUseLiquidityPool() && v.lpProvider != nil {
			poolId := data_provider.GeneratePoolId(
				v.marketConfig.GetBaseDenom(),
				v.marketConfig.GetQuoteDenom(),
			)

			pool, err := v.lpProvider.GetPool(poolId)
			if err != nil {
				v.l.WithError(err).Warn("failed to get LP for start price")
			} else if pool != nil {
				spotPrice, err := v.lpProvider.GetSpotPrice(pool)
				if err != nil {
					v.l.WithError(err).Warn("failed to calculate LP spot price")
				} else {
					v.l.WithField("price", spotPrice.String()).Info("using LP spot price as start price")
					return spotPrice
				}
			}
		}

		// Priority 2: Fallback to configured price
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
	noOfSteps := diff.Quo(*step).Quo(math.LegacyNewDec(2)).TruncateDec()
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

func (v *Orders) getSpread(buys, sells []tradebinTypes.AggregatedOrder) (biggestBuy *math.LegacyDec, smallestSell *math.LegacyDec) {
	b := math.LegacyZeroDec()
	s := math.LegacyZeroDec()

	if len(buys) > 0 {
		b = math.LegacyMustNewDecFromStr(buys[0].Price)
	}

	if len(sells) > 0 {
		s = math.LegacyMustNewDecFromStr(sells[0].Price)
	}

	return &b, &s
}

func (v *Orders) shouldFillOrderBook(marketId string) bool {
	l := v.l.WithField("method", "shouldFillOrderBook")
	history, err := v.ordersProvider.GetLastMarketOrder(marketId)
	if err != nil {
		l.WithError(err).Error("failed to get last order for market")
		return false
	}

	if history == nil {
		return true
	}

	if history.Maker == v.addressProvider.GetAddress().String() && history.Taker == v.addressProvider.GetAddress().String() {
		return true
	}

	return time.Now().Unix()-history.ExecutedAt > int64(v.ordersConfig.GetHoldBackSeconds())
}

// CLMM helper functions

func (v *Orders) calculateMidPrice(buys, sells []tradebinTypes.AggregatedOrder) *math.LegacyDec {
	if len(buys) == 0 || len(sells) == 0 {
		return nil
	}

	highestBuy := math.LegacyMustNewDecFromStr(buys[0].Price)
	lowestSell := math.LegacyMustNewDecFromStr(sells[0].Price)

	midPrice := highestBuy.Add(lowestSell).Quo(math.LegacyNewDec(2))
	return &midPrice
}

func (v *Orders) calculateCLMMRange(midPrice math.LegacyDec) (lowerBound, upperBound math.LegacyDec, err error) {
	rangeType := v.ordersConfig.GetRangeType()

	if rangeType == "percentage" {
		// Calculate range as ±X% from mid-price
		pct := v.ordersConfig.GetRangePct()
		pctDec := math.LegacyMustNewDecFromStr(fmt.Sprintf("%f", pct/100.0))

		lowerBound = midPrice.Mul(math.LegacyOneDec().Sub(pctDec))
		upperBound = midPrice.Mul(math.LegacyOneDec().Add(pctDec))
	} else if rangeType == "fixed" {
		// Use fixed price bounds
		lowerPtr := v.ordersConfig.GetRangeLowerDec()
		upperPtr := v.ordersConfig.GetRangeUpperDec()

		if lowerPtr == nil || upperPtr == nil {
			return lowerBound, upperBound, fmt.Errorf("fixed range bounds not configured")
		}

		lowerBound = *lowerPtr
		upperBound = *upperPtr
	} else {
		return lowerBound, upperBound, fmt.Errorf("invalid range type: %s", rangeType)
	}

	return lowerBound, upperBound, nil
}

func (v *Orders) calculateInventoryRatio(balances *dto.MarketBalance, midPrice math.LegacyDec) float64 {
	baseValue := balances.BaseBalance.Amount.ToLegacyDec().Mul(midPrice)
	quoteValue := balances.QuoteBalance.Amount.ToLegacyDec()
	totalValue := baseValue.Add(quoteValue)

	if !totalValue.IsPositive() {
		return 0.5 // Default to balanced if no value
	}

	// Return ratio of base value to total value (0-1 range)
	ratio := baseValue.Quo(totalValue)
	ratioFloat, _ := ratio.Float64()

	return ratioFloat
}

func (v *Orders) generateCLMMPrices(lowerBound, upperBound math.LegacyDec, numLevels int, concentrationFactor float64, step *math.LegacyDec) []math.LegacyDec {
	prices := []math.LegacyDec{}
	priceMap := make(map[string]bool) // Track unique prices after snapping

	if numLevels == 0 {
		return prices
	}

	priceRange := upperBound.Sub(lowerBound)

	// Generate more candidates than needed to account for duplicates after snapping
	maxAttempts := numLevels * 3

	for i := 0; i < maxAttempts && len(prices) < numLevels; i++ {
		// Linear position: 0 to 1
		linearPos := float64(i) / float64(maxAttempts-1)

		// Apply concentration curve
		// Higher concentration = more prices near bounds
		curvedPos := stdmath.Pow(linearPos, 1.0/concentrationFactor)

		// Map to price range
		offset := math.LegacyMustNewDecFromStr(fmt.Sprintf("%f", curvedPos))
		rawPrice := lowerBound.Add(priceRange.Mul(offset))

		// Snap price to step intervals
		snappedPrice, err := internal.TruncateToStep(&rawPrice, step)
		if err != nil {
			v.l.WithError(err).Debug("failed to truncate price to step, skipping")
			continue
		}

		// Ensure price is within bounds
		if snappedPrice.LT(lowerBound) || snappedPrice.GT(upperBound) {
			continue
		}

		// Check for duplicates (after snapping, prices may collapse to same value)
		priceStr := snappedPrice.String()
		if priceMap[priceStr] {
			continue
		}

		priceMap[priceStr] = true
		prices = append(prices, *snappedPrice)
	}

	return prices
}

func (v *Orders) calculateCLMMLiquidity(
	prices []math.LegacyDec,
	midPrice math.LegacyDec,
	inventoryRatio float64,
	concentrationFactor float64,
	minAmount, maxAmount math.Int,
) []math.Int {
	amounts := []math.Int{}

	for _, price := range prices {
		// 1. Base liquidity from concentration curve
		distanceFromMid := price.Sub(midPrice).Abs().Quo(midPrice)
		distanceFloat, _ := distanceFromMid.Float64()

		// Exponential decay from mid-price
		baseWeight := stdmath.Exp(-concentrationFactor * distanceFloat)

		// 2. Apply balance skew adjustment
		skewAdjustment := 1.0

		// inventoryRatio: 0.5 = balanced, <0.5 = need more base, >0.5 = need more quote
		imbalance := stdmath.Abs(inventoryRatio-0.5) * 2.0 // 0-1 range

		if imbalance > 0.2 { // Only adjust if significantly imbalanced
			// Make orders near mid-price fatter when imbalanced
			proximityToMid := 1.0 - distanceFloat
			skewBoost := 1.0 + (imbalance * proximityToMid * 2.0) // Up to 3x at mid when very skewed
			skewAdjustment = skewBoost
		}

		// 3. Calculate final amount
		finalWeight := baseWeight * skewAdjustment

		// Map weight to amount range
		amountRange := maxAmount.Sub(minAmount)
		weightDec := math.LegacyMustNewDecFromStr(fmt.Sprintf("%f", finalWeight))
		amountOffset := amountRange.ToLegacyDec().Mul(weightDec).TruncateInt()
		amount := minAmount.Add(amountOffset)

		// Ensure within bounds
		if amount.LT(minAmount) {
			amount = minAmount
		}
		if amount.GT(maxAmount) {
			amount = maxAmount
		}

		amounts = append(amounts, amount)
	}

	return amounts
}

func (v *Orders) placeOrdersAtLevels(
	prices []math.LegacyDec,
	amounts []math.Int,
	orderType string,
	myOrders []tradebinTypes.Order,
	marketOrders []tradebinTypes.AggregatedOrder,
) ([]*tradebinTypes.MsgCreateOrder, error) {
	l := v.l.WithField("func", "placeOrdersAtLevels").WithField("order_type", orderType)

	// Build map of existing prices
	myPrices := make(map[string]bool)
	for _, order := range myOrders {
		priceDec := math.LegacyMustNewDecFromStr(order.Price)
		myPrices[priceDec.String()] = true
	}

	ordersToCreate := []*tradebinTypes.MsgCreateOrder{}

	for i, price := range prices {
		priceStr := price.String()

		// Skip if we already have an order at this price
		if myPrices[priceStr] {
			l.WithField("price", priceStr).Debug("already have order at this price, skipping")
			continue
		}

		// Skip if there's already a market order at this price from someone else
		hasMarketOrder := false
		for _, marketOrder := range marketOrders {
			marketPrice := math.LegacyMustNewDecFromStr(marketOrder.Price)
			if marketPrice.Equal(price) {
				hasMarketOrder = true
				break
			}
		}

		if hasMarketOrder {
			l.WithField("price", priceStr).Debug("market already has order at this price, skipping")
			continue
		}

		// Create order
		amount := amounts[i]
		msg := tradebinTypes.NewMsgCreateOrder(
			v.addressProvider.GetAddress().String(),
			orderType,
			amount.String(),
			priceStr,
			v.marketConfig.GetMarketId(),
		)

		ordersToCreate = append(ordersToCreate, msg)

		l.WithFields(map[string]interface{}{
			"price":  priceStr,
			"amount": amount.String(),
		}).Debug("created order message")
	}

	if len(ordersToCreate) > 0 {
		l.WithField("count", len(ordersToCreate)).Debug("created order messages for batch submission")
	}

	return ordersToCreate, nil
}

func (v *Orders) cancelOutOfRangeOrders(
	lowerBound, upperBound math.LegacyDec,
	myBuys, mySells []tradebinTypes.Order,
) []*tradebinTypes.MsgCancelOrder {
	l := v.l.WithField("func", "cancelOutOfRangeOrders")

	ordersToCancel := []*tradebinTypes.MsgCancelOrder{}

	// Cancel buy orders below lower bound
	for _, order := range myBuys {
		orderPrice := math.LegacyMustNewDecFromStr(order.Price)
		if orderPrice.LT(lowerBound) {
			l.WithField("price", order.Price).Debug("marking buy order below range for cancellation")
			msg := tradebinTypes.NewMsgCancelOrder(
				v.addressProvider.GetAddress().String(),
				order.MarketId,
				order.Id,
				order.OrderType,
			)
			ordersToCancel = append(ordersToCancel, msg)
		}
	}

	// Cancel sell orders above upper bound
	for _, order := range mySells {
		orderPrice := math.LegacyMustNewDecFromStr(order.Price)
		if orderPrice.GT(upperBound) {
			l.WithField("price", order.Price).Debug("marking sell order above range for cancellation")
			msg := tradebinTypes.NewMsgCancelOrder(
				v.addressProvider.GetAddress().String(),
				order.MarketId,
				order.Id,
				order.OrderType,
			)
			ordersToCancel = append(ordersToCancel, msg)
		}
	}

	if len(ordersToCancel) > 0 {
		l.WithField("count", len(ordersToCancel)).Debug("created cancel messages for batch submission")
	}

	return ordersToCancel
}
