package app

import (
	"fmt"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/sirupsen/logrus"
	"tradebin-mm/app/internal"
)

type orderCanceler interface {
	CancelOrders([]*tradebinTypes.MsgCancelOrder) error
}

type cancelOrderProvider interface {
	GetAddressActiveOrders(marketId, address string, limit int) (buys, sells []tradebinTypes.Order, err error)
}

type marketProvider interface {
	GetMarketId() string
}

type Cancel struct {
	c orderCanceler
	p cancelOrderProvider
	a addressProvider

	l logrus.FieldLogger
}

func NewCancelAction(l logrus.FieldLogger, c orderCanceler, p cancelOrderProvider, a addressProvider) (*Cancel, error) {
	if l == nil || c == nil || p == nil || a == nil {
		return nil, internal.NewInvalidDependenciesErr("NewCancelAction")
	}

	return &Cancel{
		c: c,
		p: p,
		a: a,
		l: l.WithField("service", "CancelAction"),
	}, nil
}

func (c *Cancel) CancelAllOrders(m marketProvider) error {
	myBuys, mySells, err := c.p.GetAddressActiveOrders(m.GetMarketId(), c.a.GetAddress().String(), 500)
	if err != nil {
		return fmt.Errorf("failed to get address active orders: %v", err)
	}

	all := append(myBuys, mySells...)
	c.l.Infof("found %d buys and %d sells", len(myBuys), len(mySells))
	if len(all) == 0 {
		c.l.Infof("no orders to cancel")

		return nil
	}

	var msgs []*tradebinTypes.MsgCancelOrder
	for _, order := range all {
		msg := tradebinTypes.NewMsgCancelOrder(c.a.GetAddress().String(), order.MarketId, order.Id, order.OrderType)
		msgs = append(msgs, msg)
	}

	err = c.c.CancelOrders(msgs)
	if err != nil {
		return fmt.Errorf("failed to cancel orders: %v", err)
	}

	return nil
}
