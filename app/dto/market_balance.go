package dto

import (
	"github.com/cosmos/cosmos-sdk/types"
)

type MarketBalance struct {
	MarketId     string
	BaseBalance  *types.Coin
	QuoteBalance *types.Coin
}
