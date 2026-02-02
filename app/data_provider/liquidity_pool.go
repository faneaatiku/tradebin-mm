package data_provider

import (
	"context"
	"tradebin-mm/app/internal"

	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/sirupsen/logrus"
)

type LiquidityPoolProvider struct {
	provider clientProvider
	logger   logrus.FieldLogger
}

func NewLiquidityPoolProvider(logger logrus.FieldLogger, provider clientProvider) (*LiquidityPoolProvider, error) {
	if provider == nil || logger == nil {
		return nil, internal.NewInvalidDependenciesErr("NewLiquidityPoolProvider")
	}

	return &LiquidityPoolProvider{
		provider: provider,
		logger:   logger.WithField("service", "DataProvider.LiquidityPool"),
	}, nil
}

func (l *LiquidityPoolProvider) GetLiquidityPool(poolId string) (*tradebinTypes.LiquidityPool, error) {
	qc, err := l.provider.GetTradebinQueryClient()
	if err != nil {
		return nil, err
	}

	res, err := qc.LiquidityPool(context.Background(), &tradebinTypes.QueryLiquidityPoolRequest{
		PoolId: poolId,
	})
	if err != nil {
		return nil, err
	}

	l.logger.Debug("liquidity pool fetched")

	return res.Pool, nil
}
