package handler

import (
	"fmt"
	"math/rand"
	"tradebin-mm/app_v2/dto"

	"cosmossdk.io/math"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
)

type VolumeMsgFactory interface {
	NewCreateOrderMsg(amount, orderType, price string) tradebinTypes.MsgCreateOrder
}

type TxService interface {
	SubmitTxMsgs(msgs []sdktypes.Msg) (string, error)
}

type VolumeContext interface {
	GetMarketBalance() dto.MarketBalance
	GetOrderBook() dto.OrderBook
	GetBaseDenom() string
	GetQuoteDenom() string
	GetAmmSpotPrice(denom string) *math.LegacyDec
	GetWalletAddress() string
	GetHistoryLastOrder() (tradebinTypes.HistoryOrder, bool)
}

type VolumeHandlerConfig interface {
	GetPriceTolerancePercentage() math.LegacyDec
	GetMinAmount() int64
	GetMaxAmount() int64
	GetMinExtraAmount() int64
	GetMaxExtraAmount() int64
	GetExtraEvery() int64
}

type VolumeHandler struct {
	cfg        VolumeHandlerConfig
	logger     logrus.FieldLogger
	msgFactory VolumeMsgFactory

	executionsCounter int64
	tx                TxService
}

func NewVolumeHandler() (*VolumeHandler, error) {
	//TODO: validate config
	return &VolumeHandler{
		executionsCounter: 1,
	}, nil
}

func (h *VolumeHandler) MakeVolume(ctx VolumeContext) error {
	l := h.logger.WithField("func", "MakeVolume")
	l.Info("starting volume making process")

	balances := ctx.GetMarketBalance()
	l.WithFields(logrus.Fields{
		"base_balance":  balances.GetBase().String(),
		"quote_balance": balances.GetQuote().String(),
	}).Debug("retrieved market balances")

	if !balances.HasPositiveBalance() {
		l.Warn("no positive balance available to make volume")
		return fmt.Errorf("no balance to make volume")
	}

	toFill, err := h.decideOrderToFill(ctx)
	if err != nil {
		l.WithError(err).Error("failed to decide which order to fill")
		return err
	}

	if toFill == nil {
		l.Warn("no suitable order found to fill")
		return fmt.Errorf("can not make volume: no order to fill")
	}

	l.WithFields(logrus.Fields{
		"order_type":   toFill.OrderType.String(),
		"order_price":  toFill.AggregatedOrder.Price,
		"order_amount": toFill.AggregatedOrder.Amount,
	}).Debug("selected order to fill")

	amount := h.getRandomAmount(h.cfg.GetMinAmount(), h.cfg.GetMaxAmount())
	l.WithField("random_amount", amount.String()).Debug("generated random amount")

	if amount.GT(toFill.GetAmount()) {
		amount = toFill.GetAmount()
		l.WithField("adjusted_amount", amount.String()).Debug("adjusted amount to match available order amount")
	}

	var msgs []sdktypes.Msg
	shouldExtraVolume := h.shouldDoExtraVolume()
	canExtraVolume := h.canDoExtraVolume(toFill)

	l.WithFields(logrus.Fields{
		"should_do_extra": shouldExtraVolume,
		"can_do_extra":    canExtraVolume,
		"execution_count": h.executionsCounter,
	}).Debug("evaluated extra volume conditions")

	if shouldExtraVolume && canExtraVolume {
		extraAmount := h.getRandomAmount(h.cfg.GetMinExtraAmount(), h.cfg.GetMaxExtraAmount())
		l.WithField("extra_amount", extraAmount.String()).Debug("doing extra volume - generated extra amount")

		amount = amount.Add(extraAmount)
		if amount.GT(toFill.GetAmount()) {
			amountForNewOrder := amount.Sub(toFill.GetAmount())
			l.WithField("maker_order_amount", amountForNewOrder.String()).Debug("creating additional maker order for extra volume")

			makerOrder := h.msgFactory.NewCreateOrderMsg(amountForNewOrder.String(), toFill.OrderType.String(), toFill.AggregatedOrder.Price)
			msgs = append(msgs, &makerOrder)
		}
	}

	takerOrder := h.msgFactory.NewCreateOrderMsg(amount.String(), toFill.OrderType.GetOpposite().String(), toFill.AggregatedOrder.Price)
	msgs = append(msgs, &takerOrder)

	l.WithFields(logrus.Fields{
		"total_messages": len(msgs),
		"final_amount":   amount.String(),
		"price":          toFill.AggregatedOrder.Price,
		"taker_type":     toFill.OrderType.GetOpposite().String(),
	}).Debug("prepared transaction messages")

	h.IncrementExecutionsCounter()

	txID, err := h.tx.SubmitTxMsgs(msgs)
	if err != nil {
		l.WithError(err).Error("failed to submit volume making transaction")
		return fmt.Errorf("failed to submit volume making tx: %w", err)
	}

	l.WithFields(logrus.Fields{
		"tx_id":        txID,
		"msg_count":    len(msgs),
		"trade_amount": amount.String(),
		"trade_price":  toFill.AggregatedOrder.Price,
	}).Info("volume making transaction submitted successfully")

	return nil
}

func (h *VolumeHandler) getRandomAmount(min, max int64) math.Int {
	result := rand.Int63n(max-min) + min

	return math.NewInt(result)
}

func (h *VolumeHandler) IncrementExecutionsCounter() {
	h.executionsCounter++
}

func (h *VolumeHandler) canDoExtraVolume(toFill *dto.OrderBookEntry) bool {
	if toFill == nil {
		return false
	}

	return toFill.IsComplete()
}

func (h *VolumeHandler) shouldDoExtraVolume() bool {
	return h.executionsCounter%h.cfg.GetExtraEvery() == 0
}

func (h *VolumeHandler) decideOrderToFill(ctx VolumeContext) (*dto.OrderBookEntry, error) {
	l := h.logger.WithField("func", "decideOrderToFill")
	l.Debug("starting order selection process")

	orderBook := ctx.GetOrderBook()
	l.WithFields(logrus.Fields{
		"buy_orders_count":  len(orderBook.Buys),
		"sell_orders_count": len(orderBook.Sells),
	}).Debug("retrieved order book")

	if len(orderBook.Buys) == 0 && len(orderBook.Sells) == 0 {
		l.Warn("order book is empty")
		return nil, fmt.Errorf("no orders to make volume")
	}

	var firstBuy, firstSell *dto.OrderBookEntry
	var hasFirstBuy, hasFirstSell, canBuy, canSell bool
	if len(orderBook.Buys) > 0 {
		firstBuy = &orderBook.Buys[0]
		hasFirstBuy = true
		l.WithField("first_buy_price", firstBuy.Price.String()).Debug("found first buy order")
	}

	if len(orderBook.Sells) > 0 {
		firstSell = &orderBook.Sells[0]
		hasFirstSell = true
		l.WithField("first_sell_price", firstSell.Price.String()).Debug("found first sell order")
	}

	canBuy, canSell = h.canBuyOrSell(firstBuy, firstSell)
	l.WithFields(logrus.Fields{
		"can_buy_initial":  canBuy,
		"can_sell_initial": canSell,
	}).Debug("evaluated initial buy/sell capabilities")

	if (canBuy && !hasFirstSell) || (canSell && !hasFirstBuy) {
		l.Error("inconsistent state: can buy/sell but order missing")
		panic("something went wrong with canBuyOrSell logic")
	}

	ammPrice := ctx.GetAmmSpotPrice(ctx.GetBaseDenom())
	if ammPrice != nil {
		l.WithField("amm_price", ammPrice.String()).Debug("AMM spot price available")

		low, high := h.getPriceTolerance(*ammPrice)
		l.WithFields(logrus.Fields{
			"price_tolerance_low":  low.String(),
			"price_tolerance_high": high.String(),
		}).Debug("calculated price tolerance range")

		if canBuy && firstSell.Price.GT(high) {
			canBuy = false
			l.WithField("first_sell_price", firstSell.Price.String()).Debug("first sell price too high, disabling buy")
		}

		if canSell && firstBuy.Price.LT(low) {
			canSell = false
			l.WithField("first_buy_price", firstBuy.Price.String()).Debug("first buy price too low, disabling sell")
		}

		if canBuy && canSell {
			buyOrderDiff := firstBuy.Price.Sub(*ammPrice).Abs()
			sellOrderDiff := firstSell.Price.Sub(*ammPrice).Abs()

			l.WithFields(logrus.Fields{
				"buy_order_diff":  buyOrderDiff.String(),
				"sell_order_diff": sellOrderDiff.String(),
			}).Debug("both buy and sell possible, choosing closer to AMM price")

			if buyOrderDiff.LT(sellOrderDiff) {
				canSell = false
				l.Debug("buy order closer to AMM price, choosing buy")
			} else {
				canBuy = false
				l.Debug("sell order closer to AMM price, choosing sell")
			}
		}
	} else {
		l.Debug("AMM spot price not available, skipping price tolerance check")
	}

	if !canBuy && !canSell {
		l.Warn("cannot buy or sell after all checks")
		return nil, fmt.Errorf("can not buy or sell to make volume")
	}

	if canBuy && canSell {
		canBuy = rand.Intn(2) == 0
		canSell = !canBuy
		l.WithFields(logrus.Fields{
			"can_buy":  canBuy,
			"can_sell": canSell,
		}).Debug("both options viable, randomly selected one")
	}

	if canBuy {
		l.WithFields(logrus.Fields{
			"decision":     "buy",
			"fill_order":   "sell",
			"order_price":  firstSell.Price.String(),
			"order_amount": firstSell.AggregatedOrder.Amount,
		}).Info("decided to buy (fill sell order)")
		return firstSell, nil
	}

	if canSell {
		l.WithFields(logrus.Fields{
			"decision":     "sell",
			"fill_order":   "buy",
			"order_price":  firstBuy.Price.String(),
			"order_amount": firstBuy.AggregatedOrder.Amount,
		}).Info("decided to sell (fill buy order)")
		return firstBuy, nil
	}

	return nil, nil
}

func (h *VolumeHandler) canBuyOrSell(firstBuy, firstSell *dto.OrderBookEntry) (canBuy, canSell bool) {
	l := h.logger.WithField("func", "canBuyOrSell")

	var allSellOurs, allBuyOurs bool
	if firstSell != nil {
		if len(firstSell.Orders) > 0 {
			allSellOurs = firstSell.IsComplete()
			canBuy = true
			l.WithFields(logrus.Fields{
				"sell_orders_count": len(firstSell.Orders),
				"all_sell_ours":     allSellOurs,
			}).Debug("evaluated sell orders ownership")
		}
	}

	if firstBuy != nil {
		if len(firstBuy.Orders) > 0 {
			allBuyOurs = firstBuy.IsComplete()
			canSell = true
			l.WithFields(logrus.Fields{
				"buy_orders_count": len(firstBuy.Orders),
				"all_buy_ours":     allBuyOurs,
			}).Debug("evaluated buy orders ownership")
		}
	}

	if allBuyOurs != allSellOurs {
		canBuy = allSellOurs
		canSell = allBuyOurs
		l.WithFields(logrus.Fields{
			"can_buy":  canBuy,
			"can_sell": canSell,
			"reason":   "ownership_asymmetry",
		}).Debug("adjusted buy/sell capabilities based on ownership")
	}

	l.WithFields(logrus.Fields{
		"final_can_buy":  canBuy,
		"final_can_sell": canSell,
	}).Debug("determined buy/sell capabilities")

	return canBuy, canSell
}

func (h *VolumeHandler) getPriceTolerance(currentPrice math.LegacyDec) (low, high math.LegacyDec) {
	l := h.logger.WithField("func", "getPriceTolerance")

	percent := h.cfg.GetPriceTolerancePercentage()
	tolerance := currentPrice.Mul(percent)

	low = currentPrice.Sub(tolerance)
	if !low.IsPositive() {
		low = math.LegacyZeroDec()
		l.Debug("adjusted low tolerance to zero (was negative)")
	}

	high = currentPrice.Add(tolerance)
	if !high.IsPositive() {
		high = math.LegacyZeroDec()
		l.Debug("adjusted high tolerance to zero (was negative)")
	}

	l.WithFields(logrus.Fields{
		"current_price": currentPrice.String(),
		"tolerance_pct": percent.String(),
		"tolerance_abs": tolerance.String(),
		"low_bound":     low.String(),
		"high_bound":    high.String(),
	}).Debug("calculated price tolerance bounds")

	return low, high
}
