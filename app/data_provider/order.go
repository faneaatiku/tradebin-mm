package data_provider

import (
	"context"
	"fmt"
	"github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/sirupsen/logrus"
	"time"
	"tradebin-mm/app/internal"
)

const (
	buy  = "buy"
	sell = "sell"
)

const (
	orderTtl       = 240 * time.Minute
	orderKeyPrefix = "order_%s_%s_%s"
)

type locker interface {
	Lock(key string)
	Unlock(key string)
}

type cache interface {
	Get(key string) ([]byte, error)
	Set(key string, data []byte, expiration time.Duration) error
}

type Order struct {
	provider clientProvider
	logger   logrus.FieldLogger

	locker locker
	cache  cache
}

func NewOrderDataProvider(logger logrus.FieldLogger, provider clientProvider, locker locker, cache cache) (*Order, error) {
	if provider == nil || logger == nil || locker == nil || cache == nil {
		return nil, internal.NewInvalidDependenciesErr("NewOrderDataProvider")
	}

	return &Order{
		provider: provider,
		logger:   logger.WithField("service", "DataProvider.Order"),
		locker:   locker,
		cache:    cache,
	}, nil
}

func (o *Order) GetAddressActiveOrders(marketId, address string, limit int) ([]types.Order, []types.Order, error) {
	qc, err := o.provider.GetTradebinQueryClient()
	if err != nil {
		return nil, nil, err
	}

	res, err := qc.UserMarketOrders(context.Background(), &types.QueryUserMarketOrdersRequest{
		Market:  marketId,
		Address: address,
		Pagination: &query.PageRequest{
			Limit: uint64(limit),
		},
	})

	if err != nil {
		return nil, nil, err
	}
	o.logger.Debug("user active orders fetched")

	var buys []types.Order
	var sells []types.Order
	for _, order := range res.GetList() {
		o.logger.Debug("order reference", order)
		fullOrder, err := o.GetCachedOrder(marketId, order.OrderType, order.Id)
		if err != nil {
			o.logger.Errorf("failed to get order: %v", err)
			continue
		}

		if fullOrder == nil {
			o.logger.Errorf("order not found: %s", order.Id)
			continue
		}

		if fullOrder.OrderType == buy {
			buys = append(buys, *fullOrder)
		} else {
			sells = append(sells, *fullOrder)
		}
	}

	return buys, sells, nil
}

func (o *Order) GetCachedOrder(marketId, orderType, orderId string) (*types.Order, error) {
	key := o.getOrderKey(marketId, orderType, orderId)
	data, err := o.cache.Get(key)
	if err != nil {
		return nil, err
	}

	if data != nil {
		var order types.Order
		if err := order.Unmarshal(data); err != nil {
			return nil, err
		}

		return &order, nil
	}

	order, err := o.GetOrder(marketId, orderType, orderId)
	if err != nil {
		return nil, err
	}

	toCache, err := order.Marshal()
	if err != nil {
		o.logger.Errorf("failed to marshal order: %v. Skipping cache save", err)

		return order, nil
	}

	if err := o.cache.Set(key, toCache, orderTtl); err != nil {
		o.logger.Errorf("failed to save order in cache: %v", err)
	}

	return order, nil
}

func (o *Order) GetOrder(marketId, orderType, orderId string) (*types.Order, error) {
	qc, err := o.provider.GetTradebinQueryClient()
	if err != nil {
		return nil, err
	}

	res, err := qc.MarketOrder(context.Background(), &types.QueryMarketOrderRequest{
		Market:    marketId,
		OrderType: orderType,
		OrderId:   orderId,
	})

	if err != nil {
		return nil, err
	}

	ord := res.GetOrder()

	return &ord, nil
}

func (o *Order) GetActiveBuyOrders(marketId string) ([]types.AggregatedOrder, error) {
	return o.getAggregatedOrders(marketId, buy)
}

func (o *Order) GetActiveSellOrders(marketId string) ([]types.AggregatedOrder, error) {
	return o.getAggregatedOrders(marketId, sell)
}

func (o *Order) getAggregatedOrders(marketId, orderType string) ([]types.AggregatedOrder, error) {
	o.logger.Info("getting tradebin query client")
	qc, err := o.provider.GetTradebinQueryClient()
	if err != nil {
		return nil, err
	}

	params := o.getAggregatedOrdersQueryParams(marketId, orderType)
	o.logger.Info("fetching aggregated orders from blockchain")

	res, err := qc.MarketAggregatedOrders(context.Background(), params)
	if err != nil {
		return nil, err
	}
	o.logger.Info("aggregated orders fetched")

	return res.GetList(), nil
}

func (o *Order) getAggregatedOrdersQueryParams(marketId, orderType string) *types.QueryMarketAggregatedOrdersRequest {
	var reverse bool
	if orderType == buy {
		reverse = true
	}

	return &types.QueryMarketAggregatedOrdersRequest{
		Market:    marketId,
		OrderType: orderType,
		Pagination: &query.PageRequest{
			Limit:   1000,
			Reverse: reverse,
		},
	}
}

func (o *Order) getOrderKey(marketId, orderType, orderId string) string {
	return fmt.Sprintf(orderKeyPrefix, marketId, orderType, orderId)
}
