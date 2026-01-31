package handler

import (
	"fmt"
	"tradebin-mm/app_v2/dto"

	"cosmossdk.io/math"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
)

// BalancerContext defines the context data required by the balancer
type BalancerContext interface {
	GetMarketBalance() dto.MarketBalance
	GetBaseDenom() string
	GetQuoteDenom() string
	GetAmmSpotPrice(denom string) *math.LegacyDec
	GetWalletAddress() string
}

// BalancerConfig defines the configuration interface for portfolio balancing
type BalancerConfig interface {
	// GetRebalanceThreshold returns the inventory ratio threshold beyond which rebalancing is triggered
	// Example: 0.3 means rebalance if ratio < 0.3 or > 0.7 (30% deviation from balanced 0.5)
	GetRebalanceThreshold() float64

	// GetTargetRatio returns the desired inventory ratio to rebalance towards
	// Example: 0.5 means balanced (50% base value, 50% quote value)
	GetTargetRatio() float64

	// GetMinSwapAmount returns the minimum amount worth swapping (in quote denom value)
	GetMinSwapAmount() math.Int
}

// BalancerMsgFactory creates liquidity pool swap messages
type BalancerMsgFactory interface {
	// NewSwapMsg creates a swap message for the liquidity pool
	// amountIn: amount being sold
	// denomIn: denom being sold
	// denomOut: denom being bought
	// minAmountOut: minimum acceptable amount to receive (slippage protection)
	NewSwapMsg(amountIn, minAmountOut, denomIn, denomOut string) sdktypes.Msg
}

// Balancer handles portfolio rebalancing via liquidity pool swaps
type Balancer struct {
	logger     logrus.FieldLogger
	cfg        BalancerConfig
	msgFactory BalancerMsgFactory
	tx         TxService
}

// NewBalancer creates a new balancer instance
func NewBalancer() (*Balancer, error) {
	return &Balancer{}, nil
}

// Rebalance checks portfolio balance and rebalances via LP if needed
func (h *Balancer) Rebalance(ctx BalancerContext) error {
	l := h.logger.WithField("func", "Rebalance")
	l.Info("starting portfolio rebalancing process")

	// Check if liquidity pool exists
	ammPrice := ctx.GetAmmSpotPrice(ctx.GetBaseDenom())
	if ammPrice == nil {
		l.Info("no liquidity pool available, skipping rebalancing")
		return nil
	}

	l.WithField("amm_spot_price", ammPrice.String()).Debug("liquidity pool available")

	// Get current balances
	balances := ctx.GetMarketBalance()
	l.WithFields(logrus.Fields{
		"base_balance":  balances.GetBase().String(),
		"quote_balance": balances.GetQuote().String(),
	}).Debug("retrieved market balances")

	if !balances.HasPositiveBalance() {
		l.Warn("no positive balance available for rebalancing")
		return nil
	}

	// Calculate current inventory ratio
	inventoryRatio := h.calculateInventoryRatio(balances, *ammPrice)
	targetRatio := h.cfg.GetTargetRatio()
	threshold := h.cfg.GetRebalanceThreshold()

	l.WithFields(logrus.Fields{
		"inventory_ratio":     inventoryRatio,
		"target_ratio":        targetRatio,
		"rebalance_threshold": threshold,
		"lower_trigger":       targetRatio - threshold,
		"upper_trigger":       targetRatio + threshold,
		"interpretation":      h.interpretInventoryRatio(inventoryRatio, targetRatio),
		"needs_rebalancing":   h.needsRebalancing(inventoryRatio),
	}).Info("evaluated portfolio balance")

	// Check if rebalancing is needed
	if !h.needsRebalancing(inventoryRatio) {
		l.Info("portfolio is balanced, no rebalancing needed")
		return nil
	}

	// Calculate swap amounts
	swapAmount, swapDirection, err := h.calculateSwapAmount(balances, *ammPrice, inventoryRatio)
	if err != nil {
		l.WithError(err).Error("failed to calculate swap amount")
		return fmt.Errorf("failed to calculate swap amount: %w", err)
	}

	// Check if swap amount meets minimum threshold
	minSwapAmount := h.cfg.GetMinSwapAmount()
	if swapAmount.LT(minSwapAmount) {
		l.WithFields(logrus.Fields{
			"swap_amount":     swapAmount.String(),
			"min_swap_amount": minSwapAmount.String(),
		}).Info("swap amount below minimum threshold, skipping rebalancing")
		return nil
	}

	l.WithFields(logrus.Fields{
		"swap_amount":     swapAmount.String(),
		"swap_direction":  swapDirection,
		"min_swap_amount": minSwapAmount.String(),
	}).Info("calculated swap parameters for rebalancing")

	// Execute the swap
	err = h.executeSwap(ctx, swapAmount, swapDirection, *ammPrice)
	if err != nil {
		l.WithError(err).Error("failed to execute rebalancing swap")
		return fmt.Errorf("failed to execute swap: %w", err)
	}

	l.Info("portfolio rebalancing completed successfully")
	return nil
}

// calculateInventoryRatio computes the ratio of base asset value to total portfolio value
func (h *Balancer) calculateInventoryRatio(balances dto.MarketBalance, ammPrice math.LegacyDec) float64 {
	l := h.logger.WithField("func", "calculateInventoryRatio")

	baseValue := balances.GetBase().Amount.ToLegacyDec().Mul(ammPrice)
	quoteValue := balances.GetQuote().Amount.ToLegacyDec()
	totalValue := baseValue.Add(quoteValue)

	l.WithFields(logrus.Fields{
		"base_amount":  balances.GetBase().Amount.String(),
		"quote_amount": balances.GetQuote().Amount.String(),
		"amm_price":    ammPrice.String(),
		"base_value":   baseValue.String(),
		"quote_value":  quoteValue.String(),
		"total_value":  totalValue.String(),
	}).Debug("calculated portfolio values")

	if !totalValue.IsPositive() {
		l.Warn("total portfolio value not positive, using default balanced ratio")
		return h.cfg.GetTargetRatio()
	}

	// Return ratio of base value to total value (0-1 range)
	ratio := baseValue.Quo(totalValue)
	ratioFloat, _ := ratio.Float64()

	l.WithFields(logrus.Fields{
		"ratio":          ratioFloat,
		"interpretation": h.interpretInventoryRatio(ratioFloat, h.cfg.GetTargetRatio()),
	}).Debug("calculated inventory ratio")

	return ratioFloat
}

// needsRebalancing determines if portfolio rebalancing is required
func (h *Balancer) needsRebalancing(inventoryRatio float64) bool {
	l := h.logger.WithField("func", "needsRebalancing")

	targetRatio := h.cfg.GetTargetRatio()
	threshold := h.cfg.GetRebalanceThreshold()

	lowerBound := targetRatio - threshold
	upperBound := targetRatio + threshold

	needsRebalance := inventoryRatio < lowerBound || inventoryRatio > upperBound

	l.WithFields(logrus.Fields{
		"inventory_ratio":   inventoryRatio,
		"target_ratio":      targetRatio,
		"threshold":         threshold,
		"lower_bound":       lowerBound,
		"upper_bound":       upperBound,
		"needs_rebalancing": needsRebalance,
		"deviation":         inventoryRatio - targetRatio,
	}).Debug("evaluated rebalancing need")

	return needsRebalance
}

// calculateSwapAmount determines how much to swap and in which direction
func (h *Balancer) calculateSwapAmount(
	balances dto.MarketBalance,
	ammPrice math.LegacyDec,
	currentRatio float64,
) (amount math.Int, direction string, err error) {
	l := h.logger.WithField("func", "calculateSwapAmount")

	targetRatio := h.cfg.GetTargetRatio()
	baseValue := balances.GetBase().Amount.ToLegacyDec().Mul(ammPrice)
	quoteValue := balances.GetQuote().Amount.ToLegacyDec()
	totalValue := baseValue.Add(quoteValue)

	l.WithFields(logrus.Fields{
		"current_ratio": currentRatio,
		"target_ratio":  targetRatio,
		"total_value":   totalValue.String(),
	}).Debug("calculating swap amount to reach target ratio")

	// Calculate target values
	targetRatioDec := math.LegacyMustNewDecFromStr(fmt.Sprintf("%f", targetRatio))
	targetBaseValue := totalValue.Mul(targetRatioDec)
	targetQuoteValue := totalValue.Mul(math.LegacyOneDec().Sub(targetRatioDec))

	l.WithFields(logrus.Fields{
		"current_base_value":  baseValue.String(),
		"current_quote_value": quoteValue.String(),
		"target_base_value":   targetBaseValue.String(),
		"target_quote_value":  targetQuoteValue.String(),
	}).Debug("calculated target values")

	var swapAmount math.Int
	var swapDirection string

	if currentRatio > targetRatio {
		// Too much base, need to sell base for quote
		excessBaseValue := baseValue.Sub(targetBaseValue)
		swapAmountBase := excessBaseValue.Quo(ammPrice).TruncateInt()
		swapAmount = swapAmountBase
		swapDirection = "base_to_quote"

		l.WithFields(logrus.Fields{
			"excess_base_value": excessBaseValue.String(),
			"swap_amount_base":  swapAmountBase.String(),
		}).Debug("too much base asset, will swap base to quote")

	} else {
		// Too much quote, need to buy base with quote
		excessQuoteValue := quoteValue.Sub(targetQuoteValue)
		swapAmountQuote := excessQuoteValue.TruncateInt()
		swapAmount = swapAmountQuote
		swapDirection = "quote_to_base"

		l.WithFields(logrus.Fields{
			"excess_quote_value": excessQuoteValue.String(),
			"swap_amount_quote":  swapAmountQuote.String(),
		}).Debug("too much quote asset, will swap quote to base")
	}

	l.WithFields(logrus.Fields{
		"swap_amount":    swapAmount.String(),
		"swap_direction": swapDirection,
	}).Info("calculated swap parameters")

	return swapAmount, swapDirection, nil
}

// executeSwap performs the liquidity pool swap
func (h *Balancer) executeSwap(
	ctx BalancerContext,
	amount math.Int,
	direction string,
	ammPrice math.LegacyDec,
) error {
	l := h.logger.WithField("func", "executeSwap")

	var denomIn, denomOut string
	var minAmountOut math.Int

	// Slippage tolerance: 1% (0.99 = 99% of expected output)
	slippageFactor := math.LegacyMustNewDecFromStr("0.99")

	if direction == "base_to_quote" {
		denomIn = ctx.GetBaseDenom()
		denomOut = ctx.GetQuoteDenom()
		// Calculate minimum amount out with slippage protection (use AMM price - 1% as conservative estimate)
		expectedOut := amount.ToLegacyDec().Mul(ammPrice)
		minAmountOut = expectedOut.Mul(slippageFactor).TruncateInt() // 1% slippage tolerance

		l.WithFields(logrus.Fields{
			"denom_in":           denomIn,
			"denom_out":          denomOut,
			"amount_in":          amount.String(),
			"expected_out":       expectedOut.String(),
			"min_amount_out":     minAmountOut.String(),
			"slippage_tolerance": "1%",
		}).Debug("prepared base to quote swap")

	} else {
		denomIn = ctx.GetQuoteDenom()
		denomOut = ctx.GetBaseDenom()
		// Calculate minimum amount out
		expectedOut := amount.ToLegacyDec().Quo(ammPrice)
		minAmountOut = expectedOut.Mul(slippageFactor).TruncateInt() // 1% slippage tolerance

		l.WithFields(logrus.Fields{
			"denom_in":           denomIn,
			"denom_out":          denomOut,
			"amount_in":          amount.String(),
			"expected_out":       expectedOut.String(),
			"min_amount_out":     minAmountOut.String(),
			"slippage_tolerance": "1%",
		}).Debug("prepared quote to base swap")
	}

	// Create swap message
	swapMsg := h.msgFactory.NewSwapMsg(
		amount.String(),
		minAmountOut.String(),
		denomIn,
		denomOut,
	)

	l.WithFields(logrus.Fields{
		"swap_direction": direction,
		"amount_in":      amount.String(),
		"min_amount_out": minAmountOut.String(),
	}).Debug("created swap message")

	// Submit transaction
	txHash, err := h.tx.SubmitTxMsgs([]sdktypes.Msg{swapMsg})
	if err != nil {
		l.WithError(err).Error("failed to submit swap transaction")
		return fmt.Errorf("failed to submit swap transaction: %w", err)
	}

	l.WithFields(logrus.Fields{
		"tx_hash":        txHash,
		"swap_direction": direction,
		"amount_in":      amount.String(),
		"denom_in":       denomIn,
		"denom_out":      denomOut,
	}).Info("rebalancing swap transaction submitted successfully")

	return nil
}

// interpretInventoryRatio provides human-readable interpretation
func (h *Balancer) interpretInventoryRatio(ratio, targetRatio float64) string {
	deviation := ratio - targetRatio

	if deviation < -0.3 {
		return "critically_low_base"
	} else if deviation < -0.15 {
		return "low_base"
	} else if deviation < -0.05 {
		return "slightly_low_base"
	} else if deviation < 0.05 {
		return "balanced"
	} else if deviation < 0.15 {
		return "slightly_high_base"
	} else if deviation < 0.3 {
		return "high_base"
	}
	return "critically_high_base"
}
