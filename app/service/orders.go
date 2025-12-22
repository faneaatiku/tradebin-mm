package service

import (
	"cosmossdk.io/math"
	"fmt"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
	"tradebin-mm/app/internal"
)

type addressProvider interface {
	GetAddress() sdk.AccAddress
}

type Orders struct {
	l    logrus.FieldLogger
	b    *Broadcaster
	addr addressProvider
}

func NewOrderService(l logrus.FieldLogger, b *Broadcaster, addr addressProvider) (*Orders, error) {
	if l == nil || b == nil || addr == nil {
		return nil, internal.NewInvalidDependenciesErr("NewOrderService")
	}

	return &Orders{
		l:    l,
		b:    b,
		addr: addr,
	}, nil
}

func (o *Orders) CancelOrders(orders []*tradebinTypes.MsgCancelOrder) error {
	o.l.Infof("canceling orders: %v", orders)

	msgs, err := anyToSdkMsgs(internal.SliceToInterfaceSlice(orders))
	if err != nil {
		return fmt.Errorf("failed to convert orders to sdk messages: %v", err)
	}

	return o.b.BroadcastBlock(msgs)
}

func (o *Orders) AddOrders(orders []*tradebinTypes.MsgCreateOrder) error {
	o.l.Debugf("adding orders: %v", orders)

	msgs, err := anyToSdkMsgs(internal.SliceToInterfaceSlice(orders))
	if err != nil {
		return fmt.Errorf("failed to convert orders to sdk messages: %v", err)
	}

	return o.b.BroadcastBlock(msgs)
}

func (o *Orders) FillOrders(fillMsg *tradebinTypes.MsgFillOrders) error {
	o.l.Infof("filling orders: type=%s, count=%d", fillMsg.OrderType, len(fillMsg.Orders))

	msgs, err := anyToSdkMsgs([]interface{}{fillMsg})
	if err != nil {
		return fmt.Errorf("failed to convert fill orders to sdk messages: %v", err)
	}

	return o.b.BroadcastBlock(msgs)
}

// SwapToPool executes a swap on the liquidity pool
func (o *Orders) SwapToPool(poolId string, inputDenom string, inputAmount math.Int, outputDenom string, minOutputAmount math.Int) error {
	o.l.WithFields(map[string]interface{}{
		"pool_id":      poolId,
		"input_denom":  inputDenom,
		"input_amount": inputAmount.String(),
		"output_denom": outputDenom,
		"min_output":   minOutputAmount.String(),
	}).Info("swapping to LP for rebalancing")

	// Create the swap message
	swapMsg := &tradebinTypes.MsgMultiSwap{
		Creator: o.addr.GetAddress().String(),
		Routes:  []string{poolId},
		Input: sdk.Coin{
			Denom:  inputDenom,
			Amount: inputAmount,
		},
		MinOutput: sdk.Coin{
			Denom:  outputDenom,
			Amount: minOutputAmount,
		},
	}

	msgs, err := anyToSdkMsgs([]interface{}{swapMsg})
	if err != nil {
		return fmt.Errorf("failed to convert swap to sdk message: %v", err)
	}

	return o.b.BroadcastBlock(msgs)
}
