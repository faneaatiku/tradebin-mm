package data_provider

import (
	"context"
	bankv1beta1 "cosmossdk.io/api/cosmos/bank/v1beta1"
	basev1beta1 "cosmossdk.io/api/cosmos/base/v1beta1"
	"fmt"
	"github.com/cosmos/cosmos-sdk/types"
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

	m := &dto.MarketBalance{
		MarketId: marketCfg.GetMarketId(),
	}

	var base types.Coin
	var quote types.Coin
	for _, coin := range qc {
		if coin.Denom == marketCfg.GetBaseDenom() {
			baseInt, ok := types.NewIntFromString(coin.Amount)
			if !ok {
				return nil, fmt.Errorf("failed to parse base amount: %s for denom: %s", coin.Amount, coin.Denom)
			}
			base = types.NewCoin(coin.Denom, baseInt)
			m.BaseBalance = &base
		}
		if coin.Denom == marketCfg.GetQuoteDenom() {
			quoteInt, ok := types.NewIntFromString(coin.Amount)
			if !ok {
				return nil, fmt.Errorf("failed to parse quote amount: %s for denom: %s", coin.Amount, coin.Denom)
			}
			quote = types.NewCoin(coin.Denom, quoteInt)
			m.QuoteBalance = &quote
		}
	}

	return m, nil
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
	b.logger.Debug("balances fetched")

	return res.Balances, nil
}

func (b *Balance) GetMarketBalance(address string, marketCfg MarketProvider) (*dto.MarketBalance, error) {
	balances, err := b.GetAddressBalancesForMarket(address, marketCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to get address balances for market: %v", err)
	}

	if balances == nil {
		return nil, fmt.Errorf("failed to get address balances for market")
	}

	if balances.QuoteBalance == nil || !balances.QuoteBalance.IsPositive() {
		return nil, fmt.Errorf("no balance found for %s", marketCfg.GetQuoteDenom())
	}

	if balances.BaseBalance == nil || !balances.BaseBalance.IsPositive() {
		return nil, fmt.Errorf("no balance found for %s", marketCfg.GetQuoteDenom())
	}

	return balances, nil
}
