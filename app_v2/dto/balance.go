package dto

import sdk "github.com/cosmos/cosmos-sdk/types"

type MarketBalance struct {
	base  sdk.Coin
	quote sdk.Coin
}

func NewMarketBalance(base, quote sdk.Coin) *MarketBalance {
	return &MarketBalance{base, quote}
}

func (b *MarketBalance) GetBase() sdk.Coin {
	return b.base
}

func (b *MarketBalance) GetQuote() sdk.Coin {
	return b.quote
}

func (b *MarketBalance) BalanceOf(denom string) sdk.Coin {
	if denom == b.base.Denom {
		return b.base
	}

	if denom == b.quote.Denom {
		return b.quote
	}

	return sdk.NewInt64Coin(denom, 0)
}

func (b *MarketBalance) HasPositiveBalance() bool {
	return b.base.Amount.IsPositive() || b.quote.Amount.IsPositive()
}
