package handler

import (
	"fmt"
	stdmath "math"
	"tradebin-mm/app_v2/internal"

	"tradebin-mm/app_v2/dto"

	"cosmossdk.io/math"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
)

// OrderBookContext defines the context data required by the order book maker
type OrderBookContext interface {
	GetMarketBalance() dto.MarketBalance
	GetOrderBook() dto.OrderBook
	GetBaseDenom() string
	GetQuoteDenom() string
	GetAmmSpotPrice(denom string) *math.LegacyDec
	GetWalletAddress() string
}

// OrderBookConfig defines the configuration interface for order book making
type OrderBookConfig interface {
	GetBuyOrdersCount() int
	GetSellOrdersCount() int
	GetMinAmount() int64
	GetMaxAmount() int64
	GetPriceStep() math.LegacyDec
	GetStartPrice() math.LegacyDec

	// CLMM strategy configuration
	GetConcentrationFactor() float64
	GetRangeType() string           // "percentage" or "fixed"
	GetRangePercentage() float64    // Used when range_type = "percentage"
	GetRangeLower() *math.LegacyDec // Used when range_type = "fixed"
	GetRangeUpper() *math.LegacyDec // Used when range_type = "fixed"
}

// OrderBookMsgFactory creates order messages
type OrderBookMsgFactory interface {
	NewCreateOrderMsg(amount, orderType, price string) tradebinTypes.MsgCreateOrder
	NewCancelOrderMsg(marketId, orderId, orderType string) tradebinTypes.MsgCancelOrder
}

// OrderBookMaker handles order book liquidity placement using CLMM strategy
type OrderBookMaker struct {
	logger     logrus.FieldLogger
	cfg        OrderBookConfig
	msgFactory OrderBookMsgFactory
	tx         TxService
}

// NewOrderBookMaker creates a new order book maker instance
func NewOrderBookMaker() (*OrderBookMaker, error) {
	return &OrderBookMaker{}, nil
}

// FillOrderBook places orders on the order book using CLMM strategy
func (h *OrderBookMaker) FillOrderBook(ctx OrderBookContext) error {
	l := h.logger.WithField("func", "FillOrderBook")
	l.Info("starting order book fill process")

	balances := ctx.GetMarketBalance()
	l.WithFields(logrus.Fields{
		"base_balance":  balances.GetBase().String(),
		"quote_balance": balances.GetQuote().String(),
	}).Debug("retrieved market balances")

	if !balances.HasPositiveBalance() {
		l.Warn("insufficient balance to fill order book")
		return fmt.Errorf("insufficient balance to fill order book")
	}

	orderBook := ctx.GetOrderBook()
	l.WithFields(logrus.Fields{
		"buy_levels":  len(orderBook.Buys),
		"sell_levels": len(orderBook.Sells),
	}).Debug("retrieved order book state")

	// Calculate mid-price for CLMM strategy
	midPrice, err := h.calculateMidPrice(ctx, orderBook)
	if err != nil {
		l.WithError(err).Error("failed to calculate mid-price")
		return fmt.Errorf("failed to calculate mid-price: %w", err)
	}

	l.WithField("mid_price", midPrice.String()).Info("calculated mid-price for CLMM")

	// Calculate price range bounds for liquidity concentration
	lowerBound, upperBound, err := h.calculatePriceRange(*midPrice)
	if err != nil {
		l.WithError(err).Error("failed to calculate price range")
		return fmt.Errorf("failed to calculate price range: %w", err)
	}

	l.WithFields(logrus.Fields{
		"lower_bound": lowerBound.String(),
		"upper_bound": upperBound.String(),
		"range_type":  h.cfg.GetRangeType(),
	}).Info("calculated CLMM price range")

	// Calculate inventory ratio for balance-aware distribution
	inventoryRatio := h.calculateInventoryRatio(balances, *midPrice)
	l.WithField("inventory_ratio", inventoryRatio).Info("calculated inventory ratio for balance-aware distribution")

	// Generate price levels with concentration
	buyPrices := h.generatePriceLevels(lowerBound, *midPrice, h.cfg.GetBuyOrdersCount())
	sellPrices := h.generatePriceLevels(*midPrice, upperBound, h.cfg.GetSellOrdersCount())

	l.WithFields(logrus.Fields{
		"buy_prices_generated":  len(buyPrices),
		"sell_prices_generated": len(sellPrices),
		"concentration_factor":  h.cfg.GetConcentrationFactor(),
	}).Debug("generated price levels with concentration")

	// Calculate liquidity amounts per price level
	buyAmounts := h.calculateLiquidityAmounts(buyPrices, *midPrice, inventoryRatio)
	sellAmounts := h.calculateLiquidityAmounts(sellPrices, *midPrice, inventoryRatio)

	l.Debug("calculated liquidity amounts for each price level")

	// Create order messages
	buyMsgs, err := h.createOrderMessages(buyPrices, buyAmounts, dto.NewOrderTypeBuy(), orderBook)
	if err != nil {
		l.WithError(err).Error("failed to create buy order messages")
		return fmt.Errorf("failed to create buy orders: %w", err)
	}

	sellMsgs, err := h.createOrderMessages(sellPrices, sellAmounts, dto.NewOrderTypeSell(), orderBook)
	if err != nil {
		l.WithError(err).Error("failed to create sell order messages")
		return fmt.Errorf("failed to create sell orders: %w", err)
	}

	l.WithFields(logrus.Fields{
		"buy_msgs_created":  len(buyMsgs),
		"sell_msgs_created": len(sellMsgs),
	}).Debug("created order messages")

	// Combine all order messages
	allOrderMsgs := append(buyMsgs, sellMsgs...)

	// Submit orders if any
	if len(allOrderMsgs) > 0 {
		var msgs []sdktypes.Msg
		for i := range allOrderMsgs {
			msgs = append(msgs, &allOrderMsgs[i])
		}

		l.WithField("order_count", len(allOrderMsgs)).Debug("submitting order creation transaction")

		txHash, err := h.tx.SubmitTxMsgs(msgs)
		if err != nil {
			l.WithError(err).Error("failed to submit order creation transaction")
			return fmt.Errorf("failed to submit order creation transaction: %w", err)
		}

		l.WithFields(logrus.Fields{
			"tx_hash":     txHash,
			"order_count": len(allOrderMsgs),
			"buy_count":   len(buyMsgs),
			"sell_count":  len(sellMsgs),
		}).Info("order book fill transaction submitted successfully")
	} else {
		l.Info("no new orders needed")
	}

	// Create cancel messages for out-of-range orders
	cancelMsgs := h.createCancelMessages(lowerBound, upperBound, orderBook)

	if len(cancelMsgs) > 0 {
		var msgs []sdktypes.Msg
		for i := range cancelMsgs {
			msgs = append(msgs, &cancelMsgs[i])
		}

		l.WithField("cancel_count", len(cancelMsgs)).Debug("submitting cancel transaction for out-of-range orders")

		txHash, err := h.tx.SubmitTxMsgs(msgs)
		if err != nil {
			l.WithError(err).Error("failed to submit cancel transaction")
		} else {
			l.WithFields(logrus.Fields{
				"tx_hash":      txHash,
				"cancel_count": len(cancelMsgs),
			}).Info("out-of-range orders cancelled successfully")
		}
	} else {
		l.Debug("no out-of-range orders to cancel")
	}

	l.Info("order book fill process completed successfully")
	return nil
}

// calculateMidPrice determines the mid-price from AMM or order book
func (h *OrderBookMaker) calculateMidPrice(ctx OrderBookContext, orderBook dto.OrderBook) (*math.LegacyDec, error) {
	l := h.logger.WithField("func", "calculateMidPrice")

	// Priority 1: Try AMM spot price if available
	ammPrice := ctx.GetAmmSpotPrice(ctx.GetBaseDenom())
	if ammPrice != nil {
		l.WithField("amm_price", ammPrice.String()).Debug("using AMM spot price")
		return ammPrice, nil
	}

	// Priority 2: Calculate from order book spread
	if len(orderBook.Buys) > 0 && len(orderBook.Sells) > 0 {
		highestBuy := orderBook.Buys[0].Price
		lowestSell := orderBook.Sells[0].Price
		midPrice := highestBuy.Add(lowestSell).Quo(math.LegacyNewDec(2))

		l.WithFields(logrus.Fields{
			"highest_buy": highestBuy.String(),
			"lowest_sell": lowestSell.String(),
			"mid_price":   midPrice.String(),
		}).Debug("calculated mid-price from order book")

		return &midPrice, nil
	}

	// Priority 3: Fallback to configured start price
	startPrice := h.cfg.GetStartPrice()
	l.WithField("start_price", startPrice.String()).Debug("using configured start price")

	return &startPrice, nil
}

// calculatePriceRange computes the lower and upper bounds for liquidity placement
func (h *OrderBookMaker) calculatePriceRange(midPrice math.LegacyDec) (lower, upper math.LegacyDec, err error) {
	l := h.logger.WithField("func", "calculatePriceRange")

	rangeType := h.cfg.GetRangeType()
	l.WithFields(logrus.Fields{
		"range_type": rangeType,
		"mid_price":  midPrice.String(),
	}).Debug("calculating price range bounds")

	switch rangeType {
	case "percentage":
		pct := h.cfg.GetRangePercentage()
		pctDec := math.LegacyMustNewDecFromStr(fmt.Sprintf("%f", pct/100.0))

		lower = midPrice.Mul(math.LegacyOneDec().Sub(pctDec))
		upper = midPrice.Mul(math.LegacyOneDec().Add(pctDec))

		l.WithFields(logrus.Fields{
			"percentage":  pct,
			"lower_bound": lower.String(),
			"upper_bound": upper.String(),
		}).Debug("calculated percentage-based range")

	case "fixed":
		lowerPtr := h.cfg.GetRangeLower()
		upperPtr := h.cfg.GetRangeUpper()

		if lowerPtr == nil || upperPtr == nil {
			l.Error("fixed range bounds not configured in config")
			return lower, upper, fmt.Errorf("fixed range bounds not configured")
		}

		lower = *lowerPtr
		upper = *upperPtr

		l.WithFields(logrus.Fields{
			"lower_bound": lower.String(),
			"upper_bound": upper.String(),
		}).Debug("using fixed range bounds from config")

	default:
		l.WithField("invalid_type", rangeType).Error("invalid range type")
		return lower, upper, fmt.Errorf("invalid range type: %s (expected 'percentage' or 'fixed')", rangeType)
	}

	return lower, upper, nil
}

// calculateInventoryRatio computes the ratio of base asset value to total portfolio value
func (h *OrderBookMaker) calculateInventoryRatio(balances dto.MarketBalance, midPrice math.LegacyDec) float64 {
	l := h.logger.WithField("func", "calculateInventoryRatio")

	baseValue := balances.GetBase().Amount.ToLegacyDec().Mul(midPrice)
	quoteValue := balances.GetQuote().Amount.ToLegacyDec()
	totalValue := baseValue.Add(quoteValue)

	l.WithFields(logrus.Fields{
		"base_amount":  balances.GetBase().Amount.String(),
		"quote_amount": balances.GetQuote().Amount.String(),
		"base_value":   baseValue.String(),
		"quote_value":  quoteValue.String(),
		"total_value":  totalValue.String(),
	}).Debug("calculated portfolio values")

	if !totalValue.IsPositive() {
		l.Warn("total portfolio value not positive, using default balanced ratio")
		return 0.5 // Default to balanced if no value
	}

	// Return ratio of base value to total value (0-1 range)
	ratio := baseValue.Quo(totalValue)
	ratioFloat, _ := ratio.Float64()

	l.WithFields(logrus.Fields{
		"ratio":          ratioFloat,
		"interpretation": h.interpretInventoryRatio(ratioFloat),
	}).Debug("calculated inventory ratio")

	return ratioFloat
}

// interpretInventoryRatio provides human-readable interpretation
func (h *OrderBookMaker) interpretInventoryRatio(ratio float64) string {
	if ratio < 0.3 {
		return "heavy_quote_imbalance"
	} else if ratio < 0.45 {
		return "quote_skewed"
	} else if ratio < 0.55 {
		return "balanced"
	} else if ratio < 0.7 {
		return "base_skewed"
	}
	return "heavy_base_imbalance"
}

// generatePriceLevels creates price levels with concentration using exponential distribution
func (h *OrderBookMaker) generatePriceLevels(lowerBound, upperBound math.LegacyDec, count int) []math.LegacyDec {
	l := h.logger.WithField("func", "generatePriceLevels")

	if count <= 0 {
		l.Debug("requested count is zero or negative, returning empty price levels")
		return []math.LegacyDec{}
	}

	prices := []math.LegacyDec{}
	priceMap := make(map[string]bool)
	priceRange := upperBound.Sub(lowerBound)
	step := h.cfg.GetPriceStep()
	concentrationFactor := h.cfg.GetConcentrationFactor()

	l.WithFields(logrus.Fields{
		"lower_bound":          lowerBound.String(),
		"upper_bound":          upperBound.String(),
		"price_range":          priceRange.String(),
		"requested_count":      count,
		"concentration_factor": concentrationFactor,
		"price_step":           step.String(),
	}).Debug("starting price level generation")

	// Generate more candidates to account for duplicates after step snapping
	maxAttempts := count * 3
	skippedDueToBounds := 0
	skippedDueToDuplicates := 0

	for i := 0; i < maxAttempts && len(prices) < count; i++ {
		// Linear position: 0 to 1
		linearPos := float64(i) / float64(maxAttempts-1)

		// Apply concentration curve (higher factor = more concentration near bounds)
		curvedPos := stdmath.Pow(linearPos, 1.0/concentrationFactor)

		// Map to price range
		offset := math.LegacyMustNewDecFromStr(fmt.Sprintf("%f", curvedPos))
		rawPrice := lowerBound.Add(priceRange.Mul(offset))

		// Snap price to step intervals
		snappedPrice, err := internal.TruncateToStep(&rawPrice, &step)
		if err != nil {
			l.WithError(err).Debug("failed to snap price to step, skipping")
			continue
		}

		// Ensure price is within bounds
		if snappedPrice.LT(lowerBound) || snappedPrice.GT(upperBound) {
			skippedDueToBounds++
			continue
		}

		// Check for duplicates (after snapping, prices may collapse)
		priceStr := snappedPrice.String()
		if priceMap[priceStr] {
			skippedDueToDuplicates++
			continue
		}

		priceMap[priceStr] = true
		prices = append(prices, *snappedPrice)
	}

	l.WithFields(logrus.Fields{
		"generated_count":           len(prices),
		"requested_count":           count,
		"skipped_due_to_bounds":     skippedDueToBounds,
		"skipped_due_to_duplicates": skippedDueToDuplicates,
	}).Debug("completed price level generation")

	if len(prices) < count {
		l.WithFields(logrus.Fields{
			"generated": len(prices),
			"requested": count,
			"deficit":   count - len(prices),
		}).Warn("could not generate requested number of price levels")
	}

	return prices
}

// calculateLiquidityAmounts computes order amounts based on distance from mid-price and inventory
func (h *OrderBookMaker) calculateLiquidityAmounts(
	prices []math.LegacyDec,
	midPrice math.LegacyDec,
	inventoryRatio float64,
) []math.Int {
	l := h.logger.WithField("func", "calculateLiquidityAmounts")

	amounts := []math.Int{}
	concentrationFactor := h.cfg.GetConcentrationFactor()
	minAmount := math.NewInt(h.cfg.GetMinAmount())
	maxAmount := math.NewInt(h.cfg.GetMaxAmount())

	l.WithFields(logrus.Fields{
		"price_levels":         len(prices),
		"mid_price":            midPrice.String(),
		"inventory_ratio":      inventoryRatio,
		"concentration_factor": concentrationFactor,
		"min_amount":           minAmount.String(),
		"max_amount":           maxAmount.String(),
	}).Debug("starting liquidity amount calculation")

	skewAdjustmentsApplied := 0

	for i, price := range prices {
		// Calculate distance from mid-price (normalized)
		distanceFromMid := price.Sub(midPrice).Abs().Quo(midPrice)
		distanceFloat, _ := distanceFromMid.Float64()

		// Exponential decay from mid-price (more liquidity near mid-price)
		baseWeight := stdmath.Exp(-concentrationFactor * distanceFloat)

		// Apply balance skew adjustment
		skewAdjustment := 1.0
		imbalance := stdmath.Abs(inventoryRatio-0.5) * 2.0 // 0-1 range

		if imbalance > 0.2 { // Only adjust if significantly imbalanced
			proximityToMid := 1.0 - distanceFloat
			skewBoost := 1.0 + (imbalance * proximityToMid * 2.0) // Up to 3x at mid when very skewed
			skewAdjustment = skewBoost
			skewAdjustmentsApplied++
		}

		// Calculate final amount
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

		// Log details for first, middle, and last price levels
		if i == 0 || i == len(prices)/2 || i == len(prices)-1 {
			l.WithFields(logrus.Fields{
				"index":             i,
				"price":             price.String(),
				"distance_from_mid": distanceFloat,
				"base_weight":       baseWeight,
				"skew_adjustment":   skewAdjustment,
				"final_weight":      finalWeight,
				"amount":            amount.String(),
			}).Debug("calculated amount for sample price level")
		}
	}

	l.WithFields(logrus.Fields{
		"amounts_calculated":       len(amounts),
		"skew_adjustments_applied": skewAdjustmentsApplied,
	}).Debug("completed liquidity amount calculation")

	return amounts
}

// createOrderMessages generates order creation messages for given prices and amounts
func (h *OrderBookMaker) createOrderMessages(
	prices []math.LegacyDec,
	amounts []math.Int,
	orderType dto.OrderType,
	orderBook dto.OrderBook,
) ([]tradebinTypes.MsgCreateOrder, error) {
	l := h.logger.WithField("func", "createOrderMessages").WithField("order_type", orderType.String())

	if len(prices) != len(amounts) {
		return nil, fmt.Errorf("prices and amounts length mismatch: %d vs %d", len(prices), len(amounts))
	}

	// Build map of existing prices from our orders
	myPrices := h.buildMyPricesMap(orderBook, orderType)

	// Build map of all market prices
	marketPrices := h.buildMarketPricesMap(orderBook, orderType)

	messages := []tradebinTypes.MsgCreateOrder{}

	for i, price := range prices {
		priceStr := internal.TrimAmountTrailingZeros(price.String())

		// Skip if we already have an order at this price
		if myPrices[priceStr] {
			l.WithField("price", priceStr).Debug("already have order at this price, skipping")
			continue
		}

		// Skip if there's already a market order at this price
		if marketPrices[priceStr] {
			l.WithField("price", priceStr).Debug("market already has order at this price, skipping")
			continue
		}

		// Create order message
		amount := amounts[i]
		msg := h.msgFactory.NewCreateOrderMsg(
			amount.String(),
			orderType.String(),
			priceStr,
		)

		messages = append(messages, msg)

		l.WithFields(logrus.Fields{
			"price":  priceStr,
			"amount": amount.String(),
		}).Debug("created order message")
	}

	if len(messages) > 0 {
		l.WithField("count", len(messages)).Debug("created order messages")
	}

	return messages, nil
}

// createCancelMessages generates cancel messages for orders outside the price range
func (h *OrderBookMaker) createCancelMessages(
	lowerBound, upperBound math.LegacyDec,
	orderBook dto.OrderBook,
) []tradebinTypes.MsgCancelOrder {
	l := h.logger.WithField("func", "createCancelMessages")

	messages := []tradebinTypes.MsgCancelOrder{}

	// Cancel buy orders below lower bound
	for _, entry := range orderBook.Buys {
		if entry.Price.LT(lowerBound) {
			for _, order := range entry.Orders {
				msg := h.msgFactory.NewCancelOrderMsg(
					order.MarketId,
					order.Id,
					order.OrderType,
				)
				messages = append(messages, msg)

				l.WithFields(logrus.Fields{
					"order_id": order.Id,
					"price":    order.Price,
				}).Debug("marking buy order below range for cancellation")
			}
		}
	}

	// Cancel sell orders above upper bound
	for _, entry := range orderBook.Sells {
		if entry.Price.GT(upperBound) {
			for _, order := range entry.Orders {
				msg := h.msgFactory.NewCancelOrderMsg(
					order.MarketId,
					order.Id,
					order.OrderType,
				)
				messages = append(messages, msg)

				l.WithFields(logrus.Fields{
					"order_id": order.Id,
					"price":    order.Price,
				}).Debug("marking sell order above range for cancellation")
			}
		}
	}

	if len(messages) > 0 {
		l.WithField("count", len(messages)).Debug("created cancel messages")
	}

	return messages
}

// buildMyPricesMap creates a map of prices where we have existing orders
func (h *OrderBookMaker) buildMyPricesMap(orderBook dto.OrderBook, orderType dto.OrderType) map[string]bool {
	priceMap := make(map[string]bool)

	var entries []dto.OrderBookEntry
	if orderType.IsBuy() {
		entries = orderBook.Buys
	} else {
		entries = orderBook.Sells
	}

	for _, entry := range entries {
		if len(entry.Orders) > 0 {
			priceStr := internal.TrimAmountTrailingZeros(entry.Price.String())
			priceMap[priceStr] = true
		}
	}

	return priceMap
}

// buildMarketPricesMap creates a map of all prices in the order book
func (h *OrderBookMaker) buildMarketPricesMap(orderBook dto.OrderBook, orderType dto.OrderType) map[string]bool {
	priceMap := make(map[string]bool)

	var entries []dto.OrderBookEntry
	if orderType.IsBuy() {
		entries = orderBook.Buys
	} else {
		entries = orderBook.Sells
	}

	for _, entry := range entries {
		priceStr := internal.TrimAmountTrailingZeros(entry.AggregatedOrder.Price)
		priceMap[priceStr] = true
	}

	return priceMap
}
