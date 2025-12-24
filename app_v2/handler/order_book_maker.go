package handler

import "github.com/sirupsen/logrus"

type OrderBookConfig interface {
}

type OrderBookMaker struct {
	logger logrus.FieldLogger
	cfg    OrderBookConfig
}

func NewOrderBookMaker() (*OrderBookMaker, error) {
	return &OrderBookMaker{}, nil
}
