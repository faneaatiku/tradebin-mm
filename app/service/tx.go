package service

import (
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

func getFee(gas uint64, gasPrice string) (sdk.Coins, error) {
	parsed, err := sdk.ParseDecCoin(gasPrice)
	if err != nil {
		return nil, err
	}

	parsed.Amount = parsed.Amount.MulInt64(int64(gas))

	return sdk.NewCoins(sdk.NewCoin(parsed.Denom, parsed.Amount.TruncateInt())), nil
}

func anyToSdkMsgs(msgs []interface{}) ([]sdk.Msg, error) {
	var res []sdk.Msg
	for _, m := range msgs {
		msg, ok := m.(sdk.Msg)
		if !ok {
			return nil, fmt.Errorf("failed to convert message to sdk.Msg")
		}

		res = append(res, msg)
	}

	return res, nil
}
