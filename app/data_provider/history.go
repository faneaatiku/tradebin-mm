package data_provider

import (
	"context"
	"github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/sirupsen/logrus"
	"tradebin-mm/app/internal"
)

type History struct {
	provider clientProvider
	logger   logrus.FieldLogger
}

func NewHistoryDataProvider(logger logrus.FieldLogger, provider clientProvider) (*History, error) {
	if provider == nil || logger == nil {
		return nil, internal.NewInvalidDependenciesErr("NewHistoryDataProvider")
	}

	return &History{
		provider: provider,
		logger:   logger.WithField("service", "DataProvider.History"),
	}, nil
}

func (o *History) GetMarketHistory(marketId string, limit uint64, key string) ([]types.HistoryOrder, string, error) {
	qc, err := o.provider.GetTradebinQueryClient()
	if err != nil {
		return nil, "", err
	}

	params := o.getHistoryQueryParams(marketId, limit, key)
	o.logger.Debug("fetching history orders from blockchain")

	res, err := qc.MarketHistory(context.Background(), params)
	if err != nil {
		return nil, "", err
	}
	o.logger.Debug("history orders fetched")

	return res.GetList(), string(res.GetPagination().GetNextKey()), nil
}
func (o *History) getHistoryQueryParams(marketId string, limit uint64, key string) *types.QueryMarketHistoryRequest {
	res := types.QueryMarketHistoryRequest{
		Market: marketId,
		Pagination: &query.PageRequest{
			Limit:      limit,
			Reverse:    true,
			CountTotal: true,
		},
	}

	if key != "" {
		res.Pagination.Key = []byte(key)
	}

	return &res
}
