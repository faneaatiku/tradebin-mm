package service

import (
	"fmt"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/sirupsen/logrus"
	"tradebin-mm/app/internal"
)

type Orders struct {
	l logrus.FieldLogger
	b *Broadcaster
}

func NewOrderService(l logrus.FieldLogger, b *Broadcaster) (*Orders, error) {
	if l == nil || b == nil {
		return nil, internal.NewInvalidDependenciesErr("NewOrderService")
	}

	return &Orders{
		l: l,
		b: b,
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
