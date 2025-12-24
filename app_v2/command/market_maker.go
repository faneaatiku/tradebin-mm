package command

import "fmt"

type Handler interface {
	MarketMake() error
}

type MarketMakerConfig interface {
}

type MarketMaker struct {
	cfg MarketMakerConfig
	h   Handler
}

func NewMarketMakerCommand(cfg MarketMakerConfig, h Handler) (*MarketMaker, error) {
	if cfg == nil {
		return nil, fmt.Errorf("invalid config provided to market maker command")
	}

	if h == nil {
		return nil, fmt.Errorf("invalid handler provided to market maker command")
	}

	return &MarketMaker{
		cfg: cfg,
		h:   h,
	}, nil
}

func (m *MarketMaker) Run() error {
	return m.h.MarketMake()
}
