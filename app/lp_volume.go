package app

import (
	"fmt"
	"tradebin-mm/app/internal"

	basev1beta1 "cosmossdk.io/api/cosmos/base/v1beta1"
	"cosmossdk.io/math"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
)

const (
	ubzeDenom   = "ubze"
	ubzeReserve = 300000
)

type lpProvider interface {
	GetLiquidityPool(poolId string) (*tradebinTypes.LiquidityPool, error)
}

type lpBroadcaster interface {
	BroadcastBlock(msgs []sdk.Msg) error
}

type LPVolumeMaker struct {
	logger          logrus.FieldLogger
	lpProvider      lpProvider
	balanceProvider addressBalanceProvider
	addrProvider    addressProvider
	broadcaster     lpBroadcaster
	poolId          string
	slippage        math.LegacyDec
}

func NewLPVolumeMaker(
	logger logrus.FieldLogger,
	lpProvider lpProvider,
	balanceProvider addressBalanceProvider,
	addrProvider addressProvider,
	broadcaster lpBroadcaster,
	poolId string,
	slippage float64,
) (*LPVolumeMaker, error) {
	if logger == nil || lpProvider == nil || balanceProvider == nil || addrProvider == nil || broadcaster == nil {
		return nil, internal.NewInvalidDependenciesErr("NewLPVolumeMaker")
	}

	if poolId == "" {
		return nil, fmt.Errorf("pool id is required")
	}

	return &LPVolumeMaker{
		logger:          logger.WithField("service", "LPVolumeMaker"),
		lpProvider:      lpProvider,
		balanceProvider: balanceProvider,
		addrProvider:    addrProvider,
		broadcaster:     broadcaster,
		poolId:          poolId,
		slippage:        math.LegacyMustNewDecFromStr(fmt.Sprintf("%f", slippage)),
	}, nil
}

func (lv *LPVolumeMaker) MakeVolume() error {
	l := lv.logger.WithField("func", "MakeVolume")

	lp, err := lv.lpProvider.GetLiquidityPool(lv.poolId)
	if err != nil {
		return fmt.Errorf("failed to get liquidity pool: %w", err)
	}
	if lp == nil {
		return fmt.Errorf("liquidity pool not found")
	}

	l.WithField("pool_id", lp.Id).
		WithField("base", lp.Base).
		WithField("quote", lp.Quote).
		Debug("fetched liquidity pool")

	balances, err := lv.balanceProvider.GetAddressBalances(lv.addrProvider.GetAddress().String())
	if err != nil {
		return fmt.Errorf("failed to get balances: %w", err)
	}

	baseBalance := lv.findBalance(balances, lp.Base)
	quoteBalance := lv.findBalance(balances, lp.Quote)

	swapCoin, outputDenom := lv.determineSwap(lp, baseBalance, quoteBalance, l)
	if swapCoin.IsNil() || !swapCoin.IsPositive() {
		l.Info("no suitable balance to swap")
		return nil
	}

	reserveIn, reserveOut := lv.getReserves(lp, swapCoin.Denom)

	minOutput := lv.calculateMinOutput(swapCoin.Amount, reserveIn, reserveOut)
	if !minOutput.IsPositive() {
		l.Warn("calculated min output is not positive, skipping swap")
		return nil
	}

	minOutputCoin := sdk.NewCoin(outputDenom, minOutput)

	msg := tradebinTypes.NewMsgMultiSwap(
		lv.addrProvider.GetAddress().String(),
		[]string{lv.poolId},
		swapCoin,
		minOutputCoin,
	)

	l.WithField("input", swapCoin.String()).
		WithField("min_output", minOutputCoin.String()).
		Info("broadcasting swap")

	err = lv.broadcaster.BroadcastBlock([]sdk.Msg{msg})
	if err != nil {
		return fmt.Errorf("failed to broadcast swap: %w", err)
	}

	l.Info("swap broadcasted successfully")

	return nil
}

func (lv *LPVolumeMaker) determineSwap(lp *tradebinTypes.LiquidityPool, baseBalance, quoteBalance math.Int, l logrus.FieldLogger) (sdk.Coin, string) {
	hasBase := !baseBalance.IsNil() && baseBalance.IsPositive()
	hasQuote := !quoteBalance.IsNil() && quoteBalance.IsPositive()

	if !hasBase && !hasQuote {
		l.Warn("no balance for either LP denom")
		return sdk.Coin{}, ""
	}

	var swapDenom, outputDenom string
	var swapAmount math.Int

	if hasBase && hasQuote {
		// Both have balance — swap the one that is NOT ubze
		if lp.Base == ubzeDenom {
			swapDenom = lp.Quote
			outputDenom = lp.Base
			swapAmount = quoteBalance
		} else {
			swapDenom = lp.Base
			outputDenom = lp.Quote
			swapAmount = baseBalance
		}
	} else if hasBase {
		swapDenom = lp.Base
		outputDenom = lp.Quote
		swapAmount = baseBalance
	} else {
		swapDenom = lp.Quote
		outputDenom = lp.Base
		swapAmount = quoteBalance
	}

	// If swapping ubze, reserve some for fees
	if swapDenom == ubzeDenom {
		swapAmount = swapAmount.SubRaw(ubzeReserve)
		if !swapAmount.IsPositive() {
			l.Info("ubze balance too low after reserving for fees")
			return sdk.Coin{}, ""
		}
	}

	return sdk.NewCoin(swapDenom, swapAmount), outputDenom
}

func (lv *LPVolumeMaker) getReserves(lp *tradebinTypes.LiquidityPool, inputDenom string) (reserveIn, reserveOut math.Int) {
	if inputDenom == lp.Base {
		return lp.ReserveBase, lp.ReserveQuote
	}
	return lp.ReserveQuote, lp.ReserveBase
}

func (lv *LPVolumeMaker) calculateMinOutput(input, reserveIn, reserveOut math.Int) math.Int {
	// expected_output = (reserve_out * input) / (reserve_in + input)
	inputDec := math.LegacyNewDecFromInt(input)
	reserveInDec := math.LegacyNewDecFromInt(reserveIn)
	reserveOutDec := math.LegacyNewDecFromInt(reserveOut)

	expectedOutput := reserveOutDec.Mul(inputDec).Quo(reserveInDec.Add(inputDec))

	// min_output = expected_output * (1 - slippage)
	slippageFactor := math.LegacyOneDec().Sub(lv.slippage)
	minOutput := expectedOutput.Mul(slippageFactor)

	return minOutput.TruncateInt()
}

func (lv *LPVolumeMaker) findBalance(balances []*basev1beta1.Coin, denom string) math.Int {
	for _, coin := range balances {
		if coin.Denom == denom {
			amt, ok := math.NewIntFromString(coin.Amount)
			if ok {
				return amt
			}
		}
	}
	return math.ZeroInt()
}
