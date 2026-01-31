package handler

import (
	"fmt"
	"tradebin-mm/app_v2/dto"

	"cosmossdk.io/math"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
)

// ArbitrageContext defines the context data required by the arbitrage handler
type ArbitrageContext interface {
	GetMarketBalance() dto.MarketBalance
	GetOrderBook() dto.OrderBook
	GetBaseDenom() string
	GetQuoteDenom() string
	GetAmmSpotPrice(denom string) *math.LegacyDec
	GetWalletAddress() string
}

// ArbitrageConfig defines the configuration interface for arbitrage trading
type ArbitrageConfig interface {
	// GetMinProfitThreshold returns the minimum profit (in quote denom) required to execute arbitrage
	// This avoids unprofitable trades after gas fees
	GetMinProfitThreshold() math.Int

	// GetMaxProfitThreshold returns the maximum profit (in quote denom) to take in a single trade
	// This prevents being too greedy and allows others to arbitrage as well
	GetMaxProfitThreshold() math.Int

	// GetMaxTradeSize returns the maximum amount (in base denom) to trade in a single arbitrage
	// This limits exposure and risk per trade
	GetMaxTradeSize() math.Int

	// GetSlippageTolerance returns the slippage tolerance for AMM swaps (as decimal, e.g., 0.01 = 1%)
	GetSlippageTolerance() math.LegacyDec
}

// ArbitrageMsgFactory creates messages for arbitrage execution
// Dependencies to implement:
// - Market order filling service (to fill order book orders)
// - LP swap service (to swap on AMM)
type ArbitrageMsgFactory interface {
	// NewFillOrderMsg creates a message to fill an order book order
	// This acts as the taker, filling an existing maker order
	NewFillOrderMsg(orderId, orderType, amount, price string) tradebinTypes.MsgFillOrders

	// NewSwapMsg creates a swap message for the liquidity pool
	// amountIn: amount being sold to AMM
	// minAmountOut: minimum acceptable amount to receive (slippage protection)
	// denomIn: denom being sold
	// denomOut: denom being bought
	NewSwapMsg(amountIn, minAmountOut, denomIn, denomOut string) sdktypes.Msg
}

// Arbitrage handles arbitrage opportunities between order book and AMM
type Arbitrage struct {
	logger     logrus.FieldLogger
	cfg        ArbitrageConfig
	msgFactory ArbitrageMsgFactory
	tx         TxService
}

// NewArbitrage creates a new arbitrage handler instance
func NewArbitrage() (*Arbitrage, error) {
	return &Arbitrage{}, nil
}

// ArbitrageOpportunity represents a detected arbitrage opportunity
type ArbitrageOpportunity struct {
	Direction      string              // "buy_from_book" or "sell_to_book"
	OrderBookEntry dto.OrderBookEntry  // The order book entry to arbitrage
	OrderToFill    tradebinTypes.Order // Specific order to fill
	TradeAmount    math.Int            // Amount to trade
	OrderBookPrice math.LegacyDec      // Price on order book
	AmmPrice       math.LegacyDec      // Price on AMM
	ExpectedProfit math.LegacyDec      // Expected profit in quote denom
}

// ExecuteArbitrage finds and executes arbitrage opportunities
func (h *Arbitrage) ExecuteArbitrage(ctx ArbitrageContext) error {
	l := h.logger.WithField("func", "ExecuteArbitrage")
	l.Info("starting arbitrage opportunity search")

	// Check if AMM exists
	ammPrice := ctx.GetAmmSpotPrice(ctx.GetBaseDenom())
	if ammPrice == nil {
		l.Info("no AMM available, skipping arbitrage")
		return nil
	}

	l.WithField("amm_price", ammPrice.String()).Debug("AMM spot price available")

	// Get order book
	orderBook := ctx.GetOrderBook()
	l.WithFields(logrus.Fields{
		"buy_levels":  len(orderBook.Buys),
		"sell_levels": len(orderBook.Sells),
	}).Debug("retrieved order book")

	if len(orderBook.Buys) == 0 && len(orderBook.Sells) == 0 {
		l.Info("order book is empty, no arbitrage opportunities")
		return nil
	}

	// Get balances for validation
	balances := ctx.GetMarketBalance()
	l.WithFields(logrus.Fields{
		"base_balance":  balances.GetBase().String(),
		"quote_balance": balances.GetQuote().String(),
	}).Debug("retrieved balances")

	// Find arbitrage opportunities
	opportunity := h.findBestOpportunity(orderBook, *ammPrice, balances)
	if opportunity == nil {
		l.Info("no profitable arbitrage opportunity found")
		return nil
	}

	l.WithFields(logrus.Fields{
		"direction":        opportunity.Direction,
		"order_book_price": opportunity.OrderBookPrice.String(),
		"amm_price":        opportunity.AmmPrice.String(),
		"trade_amount":     opportunity.TradeAmount.String(),
		"expected_profit":  opportunity.ExpectedProfit.String(),
	}).Info("found arbitrage opportunity")

	// Validate profit is within acceptable range
	if !h.isProfitAcceptable(opportunity.ExpectedProfit) {
		l.WithFields(logrus.Fields{
			"expected_profit": opportunity.ExpectedProfit.String(),
			"min_profit":      h.cfg.GetMinProfitThreshold().String(),
			"max_profit":      h.cfg.GetMaxProfitThreshold().String(),
		}).Info("profit outside acceptable range, skipping arbitrage")
		return nil
	}

	// Execute the arbitrage
	err := h.executeOpportunity(ctx, opportunity)
	if err != nil {
		l.WithError(err).Error("failed to execute arbitrage")
		return fmt.Errorf("failed to execute arbitrage: %w", err)
	}

	l.WithFields(logrus.Fields{
		"direction": opportunity.Direction,
		"profit":    opportunity.ExpectedProfit.String(),
	}).Info("arbitrage executed successfully")

	return nil
}

// findBestOpportunity searches for the most profitable arbitrage opportunity
func (h *Arbitrage) findBestOpportunity(
	orderBook dto.OrderBook,
	ammPrice math.LegacyDec,
	balances dto.MarketBalance,
) *ArbitrageOpportunity {
	l := h.logger.WithField("func", "findBestOpportunity")

	var bestOpportunity *ArbitrageOpportunity

	// Check buy side: if order book buy price > AMM price, we can sell to the order book and buy from AMM
	for _, entry := range orderBook.Buys {
		if entry.Price.GT(ammPrice) {
			l.WithFields(logrus.Fields{
				"order_book_buy_price": entry.Price.String(),
				"amm_price":            ammPrice.String(),
				"price_diff":           entry.Price.Sub(ammPrice).String(),
			}).Debug("found potential sell-to-book opportunity")

			opportunity := h.evaluateSellToBookOpportunity(entry, ammPrice, balances)
			if opportunity != nil && (bestOpportunity == nil || opportunity.ExpectedProfit.GT(bestOpportunity.ExpectedProfit)) {
				bestOpportunity = opportunity
			}
		}
	}

	// Check sell side: if order book sell price < AMM price, we can buy from order book and sell to AMM
	for _, entry := range orderBook.Sells {
		if entry.Price.LT(ammPrice) {
			l.WithFields(logrus.Fields{
				"order_book_sell_price": entry.Price.String(),
				"amm_price":             ammPrice.String(),
				"price_diff":            ammPrice.Sub(entry.Price).String(),
			}).Debug("found potential buy-from-book opportunity")

			opportunity := h.evaluateBuyFromBookOpportunity(entry, ammPrice, balances)
			if opportunity != nil && (bestOpportunity == nil || opportunity.ExpectedProfit.GT(bestOpportunity.ExpectedProfit)) {
				bestOpportunity = opportunity
			}
		}
	}

	if bestOpportunity != nil {
		l.WithFields(logrus.Fields{
			"best_direction": bestOpportunity.Direction,
			"best_profit":    bestOpportunity.ExpectedProfit.String(),
		}).Debug("selected best arbitrage opportunity")
	} else {
		l.Debug("no viable arbitrage opportunities found")
	}

	return bestOpportunity
}

// evaluateSellToBookOpportunity evaluates selling to order book and buying from AMM
func (h *Arbitrage) evaluateSellToBookOpportunity(
	entry dto.OrderBookEntry,
	ammPrice math.LegacyDec,
	balances dto.MarketBalance,
) *ArbitrageOpportunity {
	l := h.logger.WithField("func", "evaluateSellToBookOpportunity")

	// Get the first available order we can fill
	if len(entry.Orders) == 0 {
		return nil
	}

	order := entry.Orders[0]
	orderAmount, _ := math.NewIntFromString(order.Amount)

	// Limit trade size
	maxTradeSize := h.cfg.GetMaxTradeSize()
	tradeAmount := orderAmount
	if tradeAmount.GT(maxTradeSize) {
		tradeAmount = maxTradeSize
	}

	// Check if we have enough base balance to sell
	if tradeAmount.GT(balances.GetBase().Amount) {
		tradeAmount = balances.GetBase().Amount
		l.WithField("limited_by_balance", tradeAmount.String()).Debug("trade amount limited by base balance")
	}

	if !tradeAmount.IsPositive() {
		return nil
	}

	// Calculate profit
	// Sell base on order book at entry.Price -> receive quote
	// Buy base from AMM at ammPrice using quote -> costs quote
	// Profit = (orderBookPrice - ammPrice) * tradeAmount
	priceDiff := entry.Price.Sub(ammPrice)
	profit := tradeAmount.ToLegacyDec().Mul(priceDiff)

	l.WithFields(logrus.Fields{
		"trade_amount":     tradeAmount.String(),
		"order_book_price": entry.Price.String(),
		"amm_price":        ammPrice.String(),
		"price_diff":       priceDiff.String(),
		"expected_profit":  profit.String(),
	}).Debug("evaluated sell-to-book opportunity")

	return &ArbitrageOpportunity{
		Direction:      "sell_to_book",
		OrderBookEntry: entry,
		OrderToFill:    order,
		TradeAmount:    tradeAmount,
		OrderBookPrice: entry.Price,
		AmmPrice:       ammPrice,
		ExpectedProfit: profit,
	}
}

// evaluateBuyFromBookOpportunity evaluates buying from order book and selling to AMM
func (h *Arbitrage) evaluateBuyFromBookOpportunity(
	entry dto.OrderBookEntry,
	ammPrice math.LegacyDec,
	balances dto.MarketBalance,
) *ArbitrageOpportunity {
	l := h.logger.WithField("func", "evaluateBuyFromBookOpportunity")

	// Get the first available order we can fill
	if len(entry.Orders) == 0 {
		return nil
	}

	order := entry.Orders[0]
	orderAmount, _ := math.NewIntFromString(order.Amount)

	// Limit trade size
	maxTradeSize := h.cfg.GetMaxTradeSize()
	tradeAmount := orderAmount
	if tradeAmount.GT(maxTradeSize) {
		tradeAmount = maxTradeSize
	}

	// Check if we have enough quote balance to buy
	quoteNeeded := tradeAmount.ToLegacyDec().Mul(entry.Price).TruncateInt()
	if quoteNeeded.GT(balances.GetQuote().Amount) {
		// Reduce trade amount based on available quote
		tradeAmount = balances.GetQuote().Amount.ToLegacyDec().Quo(entry.Price).TruncateInt()
		l.WithField("limited_by_balance", tradeAmount.String()).Debug("trade amount limited by quote balance")
	}

	if !tradeAmount.IsPositive() {
		return nil
	}

	// Calculate profit
	// Buy base from order book at entry.Price -> costs quote
	// Sell base to AMM at ammPrice -> receive quote
	// Profit = (ammPrice - orderBookPrice) * tradeAmount
	priceDiff := ammPrice.Sub(entry.Price)
	profit := tradeAmount.ToLegacyDec().Mul(priceDiff)

	l.WithFields(logrus.Fields{
		"trade_amount":     tradeAmount.String(),
		"order_book_price": entry.Price.String(),
		"amm_price":        ammPrice.String(),
		"price_diff":       priceDiff.String(),
		"expected_profit":  profit.String(),
	}).Debug("evaluated buy-from-book opportunity")

	return &ArbitrageOpportunity{
		Direction:      "buy_from_book",
		OrderBookEntry: entry,
		OrderToFill:    order,
		TradeAmount:    tradeAmount,
		OrderBookPrice: entry.Price,
		AmmPrice:       ammPrice,
		ExpectedProfit: profit,
	}
}

// isProfitAcceptable checks if profit is within configured thresholds
func (h *Arbitrage) isProfitAcceptable(profit math.LegacyDec) bool {
	l := h.logger.WithField("func", "isProfitAcceptable")

	profitInt := profit.TruncateInt()
	minProfit := h.cfg.GetMinProfitThreshold()
	maxProfit := h.cfg.GetMaxProfitThreshold()

	acceptable := profitInt.GTE(minProfit) && profitInt.LTE(maxProfit)

	l.WithFields(logrus.Fields{
		"profit":     profitInt.String(),
		"min_profit": minProfit.String(),
		"max_profit": maxProfit.String(),
		"acceptable": acceptable,
	}).Debug("evaluated profit acceptability")

	return acceptable
}

// executeOpportunity executes the arbitrage opportunity
func (h *Arbitrage) executeOpportunity(ctx ArbitrageContext, opp *ArbitrageOpportunity) error {
	l := h.logger.WithField("func", "executeOpportunity")

	l.WithFields(logrus.Fields{
		"direction":    opp.Direction,
		"trade_amount": opp.TradeAmount.String(),
	}).Info("executing arbitrage opportunity")

	var msgs []sdktypes.Msg

	if opp.Direction == "sell_to_book" {
		// Step 1: Sell base to order book (fill buy order)
		fillMsg := h.msgFactory.NewFillOrderMsg(
			opp.OrderToFill.Id,
			opp.OrderToFill.OrderType,
			opp.TradeAmount.String(),
			opp.OrderBookPrice.String(),
		)
		msgs = append(msgs, &fillMsg)

		l.WithFields(logrus.Fields{
			"step":     1,
			"action":   "fill_buy_order",
			"order_id": opp.OrderToFill.Id,
			"amount":   opp.TradeAmount.String(),
			"price":    opp.OrderBookPrice.String(),
			"receive":  opp.TradeAmount.ToLegacyDec().Mul(opp.OrderBookPrice).String() + " quote",
		}).Debug("prepared order book fill message")

		// Step 2: Buy base from AMM using received quote
		quoteReceived := opp.TradeAmount.ToLegacyDec().Mul(opp.OrderBookPrice).TruncateInt()
		slippageFactor := math.LegacyOneDec().Sub(h.cfg.GetSlippageTolerance())
		minBaseOut := opp.TradeAmount.ToLegacyDec().Mul(slippageFactor).TruncateInt()

		swapMsg := h.msgFactory.NewSwapMsg(
			quoteReceived.String(),
			minBaseOut.String(),
			ctx.GetQuoteDenom(),
			ctx.GetBaseDenom(),
		)
		msgs = append(msgs, swapMsg)

		l.WithFields(logrus.Fields{
			"step":               2,
			"action":             "buy_from_amm",
			"quote_in":           quoteReceived.String(),
			"min_base_out":       minBaseOut.String(),
			"slippage_tolerance": h.cfg.GetSlippageTolerance().String(),
		}).Debug("prepared AMM swap message")

	} else {
		// Step 1: Buy base from order book (fill sell order)
		fillMsg := h.msgFactory.NewFillOrderMsg(
			opp.OrderToFill.Id,
			opp.OrderToFill.OrderType,
			opp.TradeAmount.String(),
			opp.OrderBookPrice.String(),
		)
		msgs = append(msgs, &fillMsg)

		quoteCost := opp.TradeAmount.ToLegacyDec().Mul(opp.OrderBookPrice).TruncateInt()

		l.WithFields(logrus.Fields{
			"step":     1,
			"action":   "fill_sell_order",
			"order_id": opp.OrderToFill.Id,
			"amount":   opp.TradeAmount.String(),
			"price":    opp.OrderBookPrice.String(),
			"cost":     quoteCost.String() + " quote",
		}).Debug("prepared order book fill message")

		// Step 2: Sell base to AMM
		slippageFactor := math.LegacyOneDec().Sub(h.cfg.GetSlippageTolerance())
		minQuoteOut := opp.TradeAmount.ToLegacyDec().Mul(opp.AmmPrice).Mul(slippageFactor).TruncateInt()

		swapMsg := h.msgFactory.NewSwapMsg(
			opp.TradeAmount.String(),
			minQuoteOut.String(),
			ctx.GetBaseDenom(),
			ctx.GetQuoteDenom(),
		)
		msgs = append(msgs, swapMsg)

		l.WithFields(logrus.Fields{
			"step":               2,
			"action":             "sell_to_amm",
			"base_in":            opp.TradeAmount.String(),
			"min_quote_out":      minQuoteOut.String(),
			"slippage_tolerance": h.cfg.GetSlippageTolerance().String(),
		}).Debug("prepared AMM swap message")
	}

	// Submit transaction with both messages
	l.WithField("message_count", len(msgs)).Debug("submitting arbitrage transaction")

	txHash, err := h.tx.SubmitTxMsgs(msgs)
	if err != nil {
		l.WithError(err).Error("failed to submit arbitrage transaction")
		return fmt.Errorf("failed to submit transaction: %w", err)
	}

	l.WithFields(logrus.Fields{
		"tx_hash":         txHash,
		"direction":       opp.Direction,
		"trade_amount":    opp.TradeAmount.String(),
		"expected_profit": opp.ExpectedProfit.String(),
	}).Info("arbitrage transaction submitted successfully")

	return nil
}
