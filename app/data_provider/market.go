package data_provider

import (
	"context"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/sirupsen/logrus"
	"tradebin-mm/app/internal"
)

type clientProvider interface {
	GetTradebinQueryClient() (tradebinTypes.QueryClient, error)
}

type Market struct {
	provider clientProvider
	logger   logrus.FieldLogger
}

func NewMarketProvider(cl clientProvider, logger logrus.FieldLogger) (*Market, error) {
	if cl == nil || logger == nil {
		return nil, internal.NewInvalidDependenciesErr("NewMarketProvider")
	}

	return &Market{
		provider: cl,
		logger:   logger.WithField("service", "DataProvider.Market"),
	}, nil
}

func (m *Market) GetAllMarkets() ([]tradebinTypes.Market, error) {
	m.logger.Info("getting tradebin query client")
	qc, err := m.provider.GetTradebinQueryClient()
	if err != nil {
		return nil, err
	}

	params := m.getMarketsParams()
	m.logger.Info("fetching markets from blockchain")

	res, err := qc.MarketAll(context.Background(), params)
	if err != nil {
		return nil, err
	}
	m.logger.Info("markets fetched")

	return res.GetMarket(), nil
}

func (m *Market) getMarketsParams() *tradebinTypes.QueryAllMarketRequest {
	return &tradebinTypes.QueryAllMarketRequest{
		Pagination: &query.PageRequest{
			Limit: 10000,
		},
	}
}
