package app

import (
	"fmt"
	"time"
	"tradebin-mm/app/data_provider"
	"tradebin-mm/app/dto"
	"tradebin-mm/app/internal"

	"cosmossdk.io/math"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
)

const (
	volumeLock = "volume:make_volume"

	volumeMul = 5
)

type volumeOrderConfig interface {
	GetPriceStepDec() *math.LegacyDec
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
	GetInventorySkewEnabled() bool
	GetInventorySkew() float64
	GetUseLiquidityPool() bool
	GetLpTolerance() float64
	GetLpRebalanceAmount() float64
}

type liquidityPoolProvider interface {
	GetPool(poolId string) (*tradebinTypes.LiquidityPool, error)
	GetSpotPrice(pool *tradebinTypes.LiquidityPool) (*math.LegacyDec, error)
}

type lpSwapper interface {
	SwapToPool(poolId string, inputDenom string, inputAmount math.Int, outputDenom string, minOutputAmount math.Int) error
}

type volumeStrategy interface {
	GetMinAmount() *math.Int
	GetMaxAmount() *math.Int
	GetRemainingAmount() *math.Int
	SetRemainingAmount(amount *math.Int)
	GetOrderType() string
	LastRunAt() *time.Time
	SetLastRunAt(*time.Time)
	GetExtraMinVolume() *math.Int
	GetExtraMaxVolume() *math.Int
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

	// Liquidity pool integration
	lpProvider liquidityPoolProvider
	lpSwapper  lpSwapper

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
	lpProvider liquidityPoolProvider,
	lpSwapper lpSwapper,
) (*Volume, error) {
	if cfg == nil || marketConfig == nil || balanceProvider == nil || addressProvider == nil || ordersProvider == nil || orderSubmitter == nil || l == nil || locker == nil || orderConfig == nil {
		return nil, internal.NewInvalidDependenciesErr("NewVolumeMaker")
	}

	// LP providers are optional - only required if use_liquidity_pool is enabled
	if cfg.GetUseLiquidityPool() {
		if lpProvider == nil || lpSwapper == nil {
			return nil, internal.NewInvalidDependenciesErr("NewVolumeMaker: LP providers required when use_liquidity_pool is enabled")
		}
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
		lpProvider:      lpProvider,
		lpSwapper:       lpSwapper,
	}, nil
}

func (v *Volume) MakeVolume() (time.Duration, error) {
	l := v.l.WithField("func", "MakeVolume")
	if v.cfg.GetMax() == 0 {
		l.Info("volume maker is stopped due to volume max setting set to 0 or below")
	}

	v.locker.Lock(volumeLock)
	defer v.locker.Unlock(volumeLock)
	l.Debugf("lock [%s] acquired", volumeLock)

	balances, err := v.balanceProvider.GetMarketBalance(v.addressProvider.GetAddress().String(), v.marketConfig)
	if err != nil {

		return defaultHoldback, fmt.Errorf("failed to get balances: %w", err)
	}

	l.WithField("balances", balances).Debugf("balances fetched")

	buys, sells, err := v.ordersProvider.GetActiveOrders(balances)
	if err != nil {
		return defaultHoldback, fmt.Errorf("failed to get active orders: %w", err)
	}

	// Liquidity pool integration: query LP and rebalance if needed
	var spotPrice *math.LegacyDec
	if v.cfg.GetUseLiquidityPool() {
		poolId := data_provider.GeneratePoolId(
			v.marketConfig.GetBaseDenom(),
			v.marketConfig.GetQuoteDenom(),
		)

		pool, err := v.lpProvider.GetPool(poolId)
		if err != nil {
			l.WithError(err).Error("failed to get LP")
			// Continue without LP (graceful degradation)
		} else if pool == nil {
			l.Warn("LP integration enabled but pool not found, continuing without LP")
			// Continue without LP (graceful degradation)
		} else {
			spotPrice, err = v.lpProvider.GetSpotPrice(pool)
			if err != nil {
				l.WithError(err).Error("failed to calculate LP spot price")
			} else {
				l.WithField("spot_price", spotPrice).Info("fetched LP spot price")

				// Check if rebalancing needed
				if v.cfg.GetInventorySkewEnabled() && v.shouldRebalanceToLP(balances, *spotPrice) {
					l.Info("inventory imbalance detected, rebalancing via LP swap")
					err := v.rebalanceViaLP(pool, poolId, balances, *spotPrice)
					if err != nil {
						l.WithError(err).Error("failed to rebalance via LP")
					} else {
						l.Info("successfully rebalanced via LP, refreshing balances")
						// Refresh balances after swap
						balances, err = v.balanceProvider.GetMarketBalance(
							v.addressProvider.GetAddress().String(),
							v.marketConfig,
						)
						if err != nil {
							return defaultHoldback, fmt.Errorf("failed to refresh balances: %w", err)
						}
					}
				}
			}
		}
	}

	strategy, err := v.getStrategy(balances, buys, sells)
	if err != nil {

		return defaultHoldback, fmt.Errorf("failed to get strategy: %w", err)
	}

	allowedInterval := time.Now().Add(-time.Duration(v.cfg.GetTradeInterval()) * time.Second)
	if strategy.LastRunAt().After(allowedInterval) {
		holdback := strategy.LastRunAt().Sub(allowedInterval)
		l.Debugf("it's not the time to make volume yet. Will try again in %d seconds", holdback/time.Second)

		return holdback, nil
	}

	history, err := v.ordersProvider.GetLastMarketOrder(balances.MarketId)
	if err != nil {
		return defaultHoldback, fmt.Errorf("failed to get last market order: %w", err)
	}

	if history != nil {
		histDate := time.Unix(history.ExecutedAt, 0)
		if histDate.After(allowedInterval) {
			l.WithField("hist_date", histDate).Info("market has been active in the last minutes. Will NOT create volume")

			return defaultHoldback, nil
		}

		//if a foreign address is the last trader hold back for X duration taken from config
		if history.Maker != v.addressProvider.GetAddress().String() || history.Taker != v.addressProvider.GetAddress().String() {
			holdBackUntil := histDate.Add(time.Duration(v.cfg.GetHoldBackSeconds()) * time.Second)
			if holdBackUntil.After(time.Now()) {
				l.WithField("hist_date", histDate).Infof("found foreign order holding back until: %s", holdBackUntil)

				duration := holdBackUntil.Sub(time.Now())

				return duration, nil
			}
		}
	}

	// Check if order book price is outside LP tolerance - if so, create strategic order
	if v.cfg.GetUseLiquidityPool() && spotPrice != nil && len(buys) > 0 && len(sells) > 0 {
		highestBuy := math.LegacyMustNewDecFromStr(buys[0].Price)
		lowestSell := math.LegacyMustNewDecFromStr(sells[0].Price)
		midPrice := highestBuy.Add(lowestSell).Quo(math.LegacyNewDec(2))

		if !v.validatePriceTolerance(midPrice, *spotPrice) {
			l.WithFields(map[string]interface{}{
				"mid_price":  midPrice.String(),
				"spot_price": spotPrice.String(),
				"tolerance":  v.cfg.GetLpTolerance(),
			}).Info("order book price outside LP tolerance, creating strategic order to push toward LP")

			return defaultHoldback, v.createStrategicOrderToLP(midPrice, *spotPrice, balances, buys, sells)
		}
	}

	// Order book price is within tolerance or LP not enabled - use normal volume strategy
	order, orderAmount, msgs := v.makeOrder(strategy, buys, sells, balances)
	if order == nil || !orderAmount.IsPositive() || len(msgs) == 0 {
		return 0, nil
	}

	l.WithField("order_msg", order).Info("order message created")

	err = v.orderSubmitter.AddOrders(msgs)
	if err != nil {
		//destroy the strategy maybe it's out of balance and it should change
		v.strategy = nil

		return defaultHoldback, fmt.Errorf("failed to submit order: %w", err)
	}
	l.WithField("order_msg", order).Debug("order message submitted")

	v.ackOrder(orderAmount)
	l.WithField("order_msg", order).Debug("order acknowledged")

	l.WithField("strategy", strategy).Info("finished making volume")

	return defaultHoldback, nil
}

func (v *Volume) ackOrder(orderAmount math.Int) {
	now := time.Now()
	v.strategy.SetLastRunAt(&now)
	remaining := v.strategy.GetRemainingAmount().Sub(orderAmount)
	v.strategy.SetRemainingAmount(&remaining)
	v.strategy.IncrementTradesCount()
}

// applyInventorySkew adjusts the order amount based on current inventory balance
// Returns the adjusted amount
func (v *Volume) applyInventorySkew(orderAmount math.Int, orderType string, balances *dto.MarketBalance, currentPrice math.LegacyDec) math.Int {
	if !v.cfg.GetInventorySkewEnabled() {
		return orderAmount
	}

	// Calculate inventory ratio
	baseValue := balances.BaseBalance.Amount.ToLegacyDec().Mul(currentPrice)
	quoteValue := balances.QuoteBalance.Amount.ToLegacyDec()
	totalValue := baseValue.Add(quoteValue)

	if !totalValue.IsPositive() {
		v.l.Debug("total value is not positive, skipping inventory skew")
		return orderAmount
	}

	// inventoryRatio ranges from -1 to 1
	// Positive means too much base asset
	// Negative means too much quote asset
	inventoryRatio := baseValue.Sub(quoteValue).Quo(totalValue)

	skewMultiplier := math.LegacyMustNewDecFromStr(fmt.Sprintf("%f", v.cfg.GetInventorySkew()))

	var adjustment math.LegacyDec
	if orderType == tradebinTypes.OrderTypeBuy {
		// Buying means acquiring more base
		// If we have too much base (inventoryRatio > 0), reduce buy size
		adjustment = math.LegacyOneDec().Sub(inventoryRatio.Mul(skewMultiplier))
	} else {
		// Selling means reducing base
		// If we have too much base (inventoryRatio > 0), increase sell size
		adjustment = math.LegacyOneDec().Add(inventoryRatio.Mul(skewMultiplier))
	}

	// Ensure adjustment is positive and reasonable (between 0.1 and 2.0)
	minAdjustment := math.LegacyMustNewDecFromStr("0.1")
	maxAdjustment := math.LegacyMustNewDecFromStr("2.0")
	if adjustment.LT(minAdjustment) {
		adjustment = minAdjustment
	}
	if adjustment.GT(maxAdjustment) {
		adjustment = maxAdjustment
	}

	adjustedAmount := orderAmount.ToLegacyDec().Mul(adjustment).TruncateInt()

	v.l.WithFields(map[string]interface{}{
		"order_type":      orderType,
		"original_amount": orderAmount.String(),
		"adjusted_amount": adjustedAmount.String(),
		"inventory_ratio": inventoryRatio.String(),
		"adjustment_mult": adjustment.String(),
		"base_value":      baseValue.String(),
		"quote_value":     quoteValue.String(),
	}).Debug("inventory skew applied")

	return adjustedAmount
}

func (v *Volume) makeOrder(strategy volumeStrategy, buys, sells []tradebinTypes.AggregatedOrder, balances *dto.MarketBalance) (msg *tradebinTypes.MsgCreateOrder, orderAmount math.Int, msgs []*tradebinTypes.MsgCreateOrder) {
	var bookOrder tradebinTypes.AggregatedOrder
	if strategy.GetOrderType() == tradebinTypes.OrderTypeBuy {
		bookOrder = sells[0]
	} else {
		bookOrder = buys[0]
	}

	if v.isCarouselStrategy(strategy.GetType()) {
		return v.makeCarouselOrder(strategy, &bookOrder, balances)
	}

	return v.makeSpreadOrder(strategy, buys, sells, balances)
}

func (v *Volume) makeCarouselOrder(strategy volumeStrategy, bookOrder *tradebinTypes.AggregatedOrder, balances *dto.MarketBalance) (msg *tradebinTypes.MsgCreateOrder, orderAmount math.Int, msgs []*tradebinTypes.MsgCreateOrder) {
	l := v.l.WithField("func", "makeCarouselOrder")
	msgs = []*tradebinTypes.MsgCreateOrder{}

	// Calculate current price from the book order
	currentPrice := math.LegacyMustNewDecFromStr(bookOrder.Price)

	extraAmount := math.ZeroInt()
	var extraMsg *tradebinTypes.MsgCreateOrder
	if strategy.GetExtraMaxVolume().IsPositive() &&
		strategy.GetTradesCount()%v.cfg.GetExtraEvery() == 0 &&
		strategy.GetTradesCount() > 0 {
		l.Debug("extra volume is required. adding a new order to fill immediately")
		extraAmount = internal.MustRandomInt(strategy.GetExtraMinVolume(), strategy.GetExtraMaxVolume())
		// Apply inventory skew to extra amount
		extraAmount = v.applyInventorySkew(extraAmount, bookOrder.GetOrderType(), balances, currentPrice)
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

	// Apply inventory skew to main order amount
	orderAmount = v.applyInventorySkew(orderAmount, strategy.GetOrderType(), balances, currentPrice)

	if !extraAmount.IsPositive() && orderAmount.GT(*strategy.GetRemainingAmount()) {
		l.Debug("order amount is greater than remaining amount")
		orderAmount = *strategy.GetRemainingAmount()
	}
	bookAmount, _ := math.NewIntFromString(bookOrder.Amount)

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

func (v *Volume) makeSpreadOrder(strategy volumeStrategy, buys, sells []tradebinTypes.AggregatedOrder, balances *dto.MarketBalance) (msg *tradebinTypes.MsgCreateOrder, orderAmount math.Int, msgs []*tradebinTypes.MsgCreateOrder) {
	l := v.l.WithField("func", "makeSpreadOrder")
	bookBuyPrice := math.LegacyMustNewDecFromStr(buys[0].Price)
	bookSellPrice := math.LegacyMustNewDecFromStr(sells[0].Price)

	var priceDec math.LegacyDec
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
	extraAmount := math.ZeroInt()
	if strategy.GetExtraMaxVolume().IsPositive() &&
		strategy.GetTradesCount()%v.cfg.GetExtraEvery() == 0 &&
		strategy.GetTradesCount() > 0 {
		l.Debug("extra volume is required. adding a new order to fill immediately")
		extraAmount = internal.MustRandomInt(strategy.GetExtraMinVolume(), strategy.GetExtraMaxVolume())
		// Apply inventory skew to extra amount
		extraAmount = v.applyInventorySkew(extraAmount, strategy.GetOrderType(), balances, priceDec)
	}

	orderAmount = internal.MustRandomInt(strategy.GetMinAmount(), strategy.GetMaxAmount())

	// Apply inventory skew to main order amount
	orderAmount = v.applyInventorySkew(orderAmount, strategy.GetOrderType(), balances, priceDec)

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
			sellAmt, _ := math.NewIntFromString(sells[0].Amount)
			buyAmt, _ := math.NewIntFromString(buys[0].Amount)
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

	minVolume := math.NewInt(v.cfg.GetMin())
	maxVolume := math.NewInt(v.cfg.GetMax())

	remainingMin := minVolume.MulRaw(volumeMul)
	remainingMax := maxVolume.MulRaw(volumeMul)
	remaining := internal.MustRandomInt(&remainingMin, &remainingMax)

	extraMin := math.NewInt(v.cfg.GetExtraMin())
	extraMax := math.NewInt(v.cfg.GetExtraMax())

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

// shouldRebalanceToLP checks if inventory is skewed enough to trigger LP swap
// Uses existing inventory_skew config as threshold
func (v *Volume) shouldRebalanceToLP(balances *dto.MarketBalance, spotPrice math.LegacyDec) bool {
	baseValue := balances.BaseBalance.Amount.ToLegacyDec().Mul(spotPrice)
	quoteValue := balances.QuoteBalance.Amount.ToLegacyDec()
	totalValue := baseValue.Add(quoteValue)

	if !totalValue.IsPositive() {
		return false
	}

	inventoryRatio := baseValue.Quo(totalValue) // 0-1 range, 0.5 = balanced
	threshold := v.cfg.GetInventorySkew()       // Reuse inventory_skew config

	// Trigger if > (0.5 + threshold) or < (0.5 - threshold)
	upperLimit := math.LegacyMustNewDecFromStr(fmt.Sprintf("%f", 0.5+threshold))
	lowerLimit := math.LegacyMustNewDecFromStr(fmt.Sprintf("%f", 0.5-threshold))

	return inventoryRatio.GT(upperLimit) || inventoryRatio.LT(lowerLimit)
}

// rebalanceViaLP swaps imbalanced asset to LP to restore balance
// Swaps user-configurable % of imbalanced asset
func (v *Volume) rebalanceViaLP(
	pool *tradebinTypes.LiquidityPool,
	poolId string,
	balances *dto.MarketBalance,
	spotPrice math.LegacyDec,
) error {
	baseValue := balances.BaseBalance.Amount.ToLegacyDec().Mul(spotPrice)
	quoteValue := balances.QuoteBalance.Amount.ToLegacyDec()
	totalValue := baseValue.Add(quoteValue)
	inventoryRatio := baseValue.Quo(totalValue)

	var inputDenom string
	var inputAmount math.Int
	var outputDenom string

	rebalancePercent := v.cfg.GetLpRebalanceAmount() // User configurable %

	if inventoryRatio.GT(math.LegacyMustNewDecFromStr("0.5")) {
		// Too much base, swap base → quote
		inputDenom = v.marketConfig.GetBaseDenom()
		inputAmount = balances.BaseBalance.Amount.ToLegacyDec().Mul(
			math.LegacyMustNewDecFromStr(fmt.Sprintf("%f", rebalancePercent)),
		).TruncateInt()
		outputDenom = v.marketConfig.GetQuoteDenom()
	} else {
		// Too much quote, swap quote → base
		inputDenom = v.marketConfig.GetQuoteDenom()
		inputAmount = balances.QuoteBalance.Amount.ToLegacyDec().Mul(
			math.LegacyMustNewDecFromStr(fmt.Sprintf("%f", rebalancePercent)),
		).TruncateInt()
		outputDenom = v.marketConfig.GetBaseDenom()
	}

	// Calculate expected output using constant product formula
	expectedOutput := v.calculateExpectedSwapOutput(pool, inputDenom, inputAmount)

	// Apply 2% slippage tolerance for min output
	minOutput := expectedOutput.Mul(math.LegacyMustNewDecFromStr("0.98")).TruncateInt()

	v.l.WithFields(map[string]interface{}{
		"input_denom":  inputDenom,
		"input_amount": inputAmount.String(),
		"output_denom": outputDenom,
		"min_output":   minOutput.String(),
		"pool_id":      poolId,
	}).Info("executing LP swap for rebalancing")

	return v.lpSwapper.SwapToPool(poolId, inputDenom, inputAmount, outputDenom, minOutput)
}

// calculateExpectedSwapOutput calculates expected output using constant product formula
// Formula: outputAmount = reserveOut - (reserveIn * reserveOut) / (reserveIn + inputAmount * (1 - fee))
func (v *Volume) calculateExpectedSwapOutput(
	pool *tradebinTypes.LiquidityPool,
	inputDenom string,
	inputAmount math.Int,
) math.LegacyDec {
	var reserveIn, reserveOut math.Int

	if inputDenom == pool.Base {
		reserveIn = pool.ReserveBase
		reserveOut = pool.ReserveQuote
	} else {
		reserveIn = pool.ReserveQuote
		reserveOut = pool.ReserveBase
	}

	// Calculate with fee
	fee := pool.Fee // e.g., 0.003 for 0.3%
	inputAfterFee := inputAmount.ToLegacyDec().Mul(
		math.LegacyOneDec().Sub(fee),
	)

	newReserveIn := reserveIn.ToLegacyDec().Add(inputAfterFee)
	constantProduct := reserveIn.ToLegacyDec().Mul(reserveOut.ToLegacyDec())
	newReserveOut := constantProduct.Quo(newReserveIn)

	expectedOutput := reserveOut.ToLegacyDec().Sub(newReserveOut)

	return expectedOutput
}

// validatePriceTolerance checks if order price is within acceptable range of LP spot price
// Returns false if outside tolerance
func (v *Volume) validatePriceTolerance(orderPrice, lpSpotPrice math.LegacyDec) bool {
	tolerance := v.cfg.GetLpTolerance() / 100.0 // Convert % to decimal
	toleranceDec := math.LegacyMustNewDecFromStr(fmt.Sprintf("%f", tolerance))

	upperBound := lpSpotPrice.Mul(math.LegacyOneDec().Add(toleranceDec))
	lowerBound := lpSpotPrice.Mul(math.LegacyOneDec().Sub(toleranceDec))

	withinTolerance := orderPrice.GTE(lowerBound) && orderPrice.LTE(upperBound)

	if !withinTolerance {
		v.l.WithFields(map[string]interface{}{
			"order_price":   orderPrice.String(),
			"spot_price":    lpSpotPrice.String(),
			"lower_bound":   lowerBound.String(),
			"upper_bound":   upperBound.String(),
			"tolerance_pct": v.cfg.GetLpTolerance(),
		}).Debug("price tolerance check failed")
	}

	return withinTolerance
}

// createStrategicOrderToLP fills existing orders to push the order book price toward LP spot price
// when they diverge beyond tolerance. This creates volume while arbitraging the price gap.
func (v *Volume) createStrategicOrderToLP(
	midPrice, lpSpotPrice math.LegacyDec,
	balances *dto.MarketBalance,
	buys, sells []tradebinTypes.AggregatedOrder,
) error {
	l := v.l.WithField("func", "createStrategicOrderToLP")

	var orderType string // Type of orders we're FILLING (not the action we're taking)
	var ordersToFill []tradebinTypes.AggregatedOrder

	// Determine which orders to fill based on divergence direction
	if midPrice.GT(lpSpotPrice) {
		// Order book price too high - fill BUY orders (sell to buyers) to push down
		orderType = tradebinTypes.OrderTypeBuy // Filling BUY orders
		ordersToFill = buys
	} else {
		// Order book price too low - fill SELL orders (buy from sellers) to push up
		orderType = tradebinTypes.OrderTypeSell // Filling SELL orders
		ordersToFill = sells
	}

	if len(ordersToFill) == 0 {
		l.Warn("no orders available to fill for strategic trade")
		return nil
	}

	// Build list of orders to fill - fill each ENTIRE order starting from best price
	// Stop when we reach LP price OR can't afford the next order
	var fillItems []*tradebinTypes.FillOrderItem
	remainingBase := balances.BaseBalance.Amount
	remainingQuote := balances.QuoteBalance.Amount

	for _, order := range ordersToFill {
		orderAmount, _ := math.NewIntFromString(order.Amount)
		price, _ := math.LegacyNewDecFromStr(order.Price)

		// Stop if this order's price has crossed the LP target
		if midPrice.GT(lpSpotPrice) {
			// Filling BUY orders (pushing price down toward LP) - stop if price goes below LP
			if price.LT(lpSpotPrice) {
				l.WithFields(map[string]interface{}{
					"order_price": price.String(),
					"lp_price":    lpSpotPrice.String(),
				}).Debug("strategic fill: reached LP price target, stopping")
				break
			}
		} else {
			// Filling SELL orders (pushing price up toward LP) - stop if price goes above LP
			if price.GT(lpSpotPrice) {
				l.WithFields(map[string]interface{}{
					"order_price": price.String(),
					"lp_price":    lpSpotPrice.String(),
				}).Debug("strategic fill: reached LP price target, stopping")
				break
			}
		}

		// Check if we can afford this ENTIRE order
		var canAfford bool
		if orderType == tradebinTypes.OrderTypeBuy {
			// Filling BUY orders (selling) - need base currency
			canAfford = orderAmount.LTE(remainingBase)
		} else {
			// Filling SELL orders (buying) - need quote currency
			cost := price.Mul(orderAmount.ToLegacyDec()).TruncateInt()
			canAfford = cost.LTE(remainingQuote)
		}

		if !canAfford {
			// Can't afford this entire order, stop here
			l.WithFields(map[string]interface{}{
				"price":           order.Price,
				"order_amount":    orderAmount.String(),
				"remaining_base":  remainingBase.String(),
				"remaining_quote": remainingQuote.String(),
			}).Debug("strategic fill: insufficient balance for this entire order, stopping")
			break
		}

		// Add this entire order to fill list
		fillItems = append(fillItems, &tradebinTypes.FillOrderItem{
			Price:  order.Price,
			Amount: orderAmount.String(),
		})

		// Deduct from available balance
		if orderType == tradebinTypes.OrderTypeBuy {
			remainingBase = remainingBase.Sub(orderAmount)
		} else {
			cost := price.Mul(orderAmount.ToLegacyDec()).TruncateInt()
			remainingQuote = remainingQuote.Sub(cost)
		}

		l.WithFields(map[string]interface{}{
			"price":           order.Price,
			"order_amount":    orderAmount.String(),
			"filled_entirely": true,
			"remaining_base":  remainingBase.String(),
			"remaining_quote": remainingQuote.String(),
		}).Debug("strategic fill: added entire order to fill list")
	}

	if len(fillItems) == 0 {
		l.Warn("no fill items created for strategic trade")
		return nil
	}

	// Calculate total amount being filled for logging
	totalFilled := math.ZeroInt()
	totalCost := math.ZeroInt()
	for _, item := range fillItems {
		amt, _ := math.NewIntFromString(item.Amount)
		totalFilled = totalFilled.Add(amt)

		if orderType == tradebinTypes.OrderTypeSell {
			// Calculate total cost when buying
			price, _ := math.LegacyNewDecFromStr(item.Price)
			cost := price.Mul(amt.ToLegacyDec()).TruncateInt()
			totalCost = totalCost.Add(cost)
		}
	}

	logFields := map[string]interface{}{
		"order_type":   orderType,
		"fill_count":   len(fillItems),
		"total_amount": totalFilled.String(),
		"mid_price":    midPrice.String(),
		"lp_price":     lpSpotPrice.String(),
	}
	if orderType == tradebinTypes.OrderTypeSell {
		logFields["total_cost"] = totalCost.String()
	}

	l.WithFields(logFields).Info("creating AGGRESSIVE strategic fill to align order book with LP (using max available balance)")

	msg := tradebinTypes.NewMsgFillOrders(
		v.addressProvider.GetAddress().String(),
		v.marketConfig.GetMarketId(),
		orderType,
		fillItems,
	)

	err := v.orderSubmitter.FillOrders(msg)
	if err != nil {
		return fmt.Errorf("failed to submit strategic fill order: %w", err)
	}

	l.Info("strategic fill order submitted successfully")
	return nil
}
