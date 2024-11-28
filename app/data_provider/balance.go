package data_provider

import (
	"context"
	bankv1beta1 "cosmossdk.io/api/cosmos/bank/v1beta1"
	basev1beta1 "cosmossdk.io/api/cosmos/base/v1beta1"
	"github.com/sirupsen/logrus"
	"tradebin-mm/app/dto"
	"tradebin-mm/app/internal"
)

type MarketProvider interface {
	GetBaseDenom() string
	GetQuoteDenom() string
	GetMarketId() string
}

type bankClientProvider interface {
	GetBankQueryClient() (bankv1beta1.QueryClient, error)
}

type Balance struct {
	provider bankClientProvider
	logger   logrus.FieldLogger
}

func NewBalanceDataProvider(logger logrus.FieldLogger, provider bankClientProvider) (*Balance, error) {
	if provider == nil || logger == nil {
		return nil, internal.NewInvalidDependenciesErr("NewBalanceDataProvider")
	}

	return &Balance{
		provider: provider,
		logger:   logger.WithField("service", "DataProvider.Balance"),
	}, nil
}

func (b *Balance) GetAddressBalancesForMarket(address string, marketCfg MarketProvider) (*dto.MarketBalance, error) {
	qc, err := b.GetAddressBalances(address)
	if err != nil {
		return nil, err
	}

	var base *basev1beta1.Coin
	var quote *basev1beta1.Coin
	for _, coin := range qc {
		if coin.Denom == marketCfg.GetBaseDenom() {
			base = coin
		}
		if coin.Denom == marketCfg.GetQuoteDenom() {
			quote = coin
		}
	}

	return &dto.MarketBalance{
		MarketId:     marketCfg.GetMarketId(),
		BaseBalance:  base,
		QuoteBalance: quote,
	}, nil
}

func (b *Balance) GetAddressBalances(address string) ([]*basev1beta1.Coin, error) {
	qc, err := b.provider.GetBankQueryClient()
	if err != nil {
		return nil, err
	}

	res, err := qc.AllBalances(context.Background(), &bankv1beta1.QueryAllBalancesRequest{
		Address: address,
	})

	if err != nil {
		return nil, err
	}
	b.logger.Info("balances fetched")

	return res.Balances, nil
}
