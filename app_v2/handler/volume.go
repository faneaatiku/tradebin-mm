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
	balances := ctx.GetMarketBalance()
	if !balances.HasPositiveBalance() {
		return fmt.Errorf("no balance to make volume")
	}

	toFill, err := h.decideOrderToFill(ctx)
	if err != nil {
		return err
	}

	if toFill == nil {
		return fmt.Errorf("can not make volume: no order to fill")
	}

	amount := h.getRandomAmount(h.cfg.GetMinAmount(), h.cfg.GetMaxAmount())
	if amount.GT(toFill.GetAmount()) {
		amount = toFill.GetAmount()
	}

	var msgs []sdktypes.Msg
	if h.shouldDoExtraVolume() && h.canDoExtraVolume(toFill) {
		//random amount
		extraAmount := h.getRandomAmount(h.cfg.GetMinExtraAmount(), h.cfg.GetMaxExtraAmount())
		//add it to the total we have to trade
		//this amount will be used for the taker order
		amount = amount.Add(extraAmount)
		if amount.GT(toFill.GetAmount()) {
			//we have to do extra volume, but the existing order at this price doesn't have enough amount
			//we need to create a new order before filling the existing order (and the newly one created)

			//need to create a new order (maker order) which we will fill with the taker order
			amountForNewOrder := amount.Sub(toFill.GetAmount())
			makerOrder := h.msgFactory.NewCreateOrderMsg(amountForNewOrder.String(), toFill.OrderType.String(), toFill.AggregatedOrder.Price)
			msgs = append(msgs, &makerOrder)
		}
	}

	takerOrder := h.msgFactory.NewCreateOrderMsg(amount.String(), toFill.OrderType.GetOpposite().String(), toFill.AggregatedOrder.Price)
	msgs = append(msgs, &takerOrder)

	h.IncrementExecutionsCounter()

	txID, err := h.tx.SubmitTxMsgs(msgs)
	if err != nil {
		return fmt.Errorf("failed to submit volume making tx: %w", err)
	}

	h.logger.WithField("tx_id", txID).Info("volume making tx submitted successfully")

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
	orderBook := ctx.GetOrderBook()
	if len(orderBook.Buys) == 0 && len(orderBook.Sells) == 0 {
		return nil, fmt.Errorf("no orders to make volume")
	}

	var firstBuy, firstSell *dto.OrderBookEntry
	var hasFirstBuy, hasFirstSell, canBuy, canSell bool
	if len(orderBook.Buys) > 0 {
		firstBuy = &orderBook.Buys[0]
		hasFirstBuy = true
	}

	if len(orderBook.Sells) > 0 {
		firstSell = &orderBook.Sells[0]
		hasFirstSell = true
	}

	canBuy, canSell = h.canBuyOrSell(firstBuy, firstSell)
	if (canBuy && !hasFirstSell) || (canSell && !hasFirstBuy) {
		//can not buy or sell if we don't have an order to fill.
		//this should never happen, but if it does, we want to make sure we see it
		panic("something went wrong with canBuyOrSell logic")
	}

	ammPrice := ctx.GetAmmSpotPrice(ctx.GetBaseDenom())
	if ammPrice != nil {
		low, high := h.getPriceTolerance(*ammPrice)
		//if we can buy but the first sell is too high, we can't buy
		if canBuy && firstSell.Price.GT(high) {
			canBuy = false
		}

		//if we can sell but the first buy is too low, we can't sell
		if canSell && firstBuy.Price.LT(low) {
			canSell = false
		}

		//if we can do both, decide to only one of them, the one closer to the price
		if canBuy && canSell {
			buyOrderDiff := firstBuy.Price.Sub(*ammPrice).Abs()
			sellOrderDiff := firstSell.Price.Sub(*ammPrice).Abs()
			//we want the price movement to be as low as possible
			if buyOrderDiff.LT(sellOrderDiff) {
				//the buy order is closer to the price, so we can only buy
				canSell = false
			} else {
				//the sell order is closer to the price, so we can only sell
				canBuy = false
			}
		}
	}

	// we couldn't make a decision
	if !canBuy && !canSell {
		return nil, fmt.Errorf("can not buy or sell to make volume")
	}

	//if we can do both, we have to decide to only one of them
	if canBuy && canSell {
		//if we own the entire amount of buy and sell, we decide randomly
		canBuy = rand.Intn(2) == 0
		canSell = !canBuy
	}

	if canBuy {
		return firstSell, nil
	}

	if canSell {
		return firstBuy, nil
	}

	return nil, nil
}

func (h *VolumeHandler) canBuyOrSell(firstBuy, firstSell *dto.OrderBookEntry) (canBuy, canSell bool) {
	var allSellOurs, allBuyOurs bool
	if firstSell != nil {
		if len(firstSell.Orders) > 0 {
			allSellOurs = firstSell.IsComplete()
			canBuy = true
		}
	}

	if firstBuy != nil {
		if len(firstBuy.Orders) > 0 {
			allBuyOurs = firstBuy.IsComplete()
			canSell = true
		}
	}

	if allBuyOurs != allSellOurs {
		canBuy = allSellOurs
		canSell = allBuyOurs
	}

	return canBuy, canSell
}

func (h *VolumeHandler) getPriceTolerance(currentPrice math.LegacyDec) (low, high math.LegacyDec) {
	percent := h.cfg.GetPriceTolerancePercentage()
	tolerance := currentPrice.Mul(percent)
	low = currentPrice.Sub(tolerance)
	if !low.IsPositive() {
		low = math.LegacyZeroDec()
	}
	high = currentPrice.Add(tolerance)
	if !high.IsPositive() {
		high = math.LegacyZeroDec()
	}

	return low, high
}
