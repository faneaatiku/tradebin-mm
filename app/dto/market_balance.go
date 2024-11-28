package dto

import basev1beta1 "cosmossdk.io/api/cosmos/base/v1beta1"

type MarketBalance struct {
	MarketId     string
	BaseBalance  *basev1beta1.Coin
	QuoteBalance *basev1beta1.Coin
}
