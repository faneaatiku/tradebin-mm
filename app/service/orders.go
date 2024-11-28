package service

import (
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/sirupsen/logrus"
)

type Orders struct {
	l logrus.FieldLogger
}

func NewOrderService(l logrus.FieldLogger) (*Orders, error) {
	return &Orders{l: l}, nil
}

func (o *Orders) CancelOrder(order tradebinTypes.Order) error {
	o.l.Infof("canceling order: %s", order.String())

	//TODO implement me
	return nil
}

func (o *Orders) AddOrders(orders []*tradebinTypes.MsgCreateOrder) error {
	o.l.Infof("adding orders: %v", orders)

	//TODO implement me
	return nil
}
