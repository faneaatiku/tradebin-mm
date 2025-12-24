package dto

import (
	"context"
	"time"

	"cosmossdk.io/math"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
)

type AppContext struct {
	baseCtx context.Context

	//market
	marketId string

	//orders

	//wallet
	address      string
	msgCollector []sdktypes.Msg
}

func (a AppContext) GetMarketBalance() MarketBalance {
	//TODO implement me
	panic("implement me")
}

func (a AppContext) GetOrderBook() OrderBook {
	//TODO implement me
	panic("implement me")
}

func (a AppContext) GetBaseDenom() string {
	//TODO implement me
	panic("implement me")
}

func (a AppContext) GetQuoteDenom() string {
	//TODO implement me
	panic("implement me")
}

func (a AppContext) GetAmmSpotPrice(denom string) *math.LegacyDec {
	//TODO implement me - return nil when LP doesnt exist for this combination of base and quote
	// or return nil if the instance is configured NOT to use LP
	panic("implement me")
}

func (a AppContext) GetWalletAddress() string {
	//TODO implement me
	panic("implement me")
}

func (a AppContext) GetHistoryLastOrder() (tradebinTypes.HistoryOrder, bool) {
	//TODO implement me
	panic("implement me")
}

func (a AppContext) Deadline() (deadline time.Time, ok bool) {
	return a.baseCtx.Deadline()
}

func (a AppContext) Done() <-chan struct{} {
	return a.baseCtx.Done()
}

func (a AppContext) Err() error {
	return a.baseCtx.Err()
}

func (a AppContext) Value(key any) any {
	return a.baseCtx.Value(key)
}
