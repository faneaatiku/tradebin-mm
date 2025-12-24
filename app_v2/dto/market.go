package dto

import (
	"cosmossdk.io/math"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
)

// OrderBookEntry - contains aggregated order from the blockchain and the orders that are included in this aggregation.
// tradebinTypes.AggregatedOrder is fetched from the blockchain
// []tradebinTypes.Order - is fetched from the blockchain in the context of an address, we can't get the orders that are
// included in the aggregated order unless we have the address that created them.
// in the context of this app we can fetch only the orders of the app's wallet.
type OrderBookEntry struct {
	AggregatedOrder tradebinTypes.AggregatedOrder
	Orders          []tradebinTypes.Order //orders owned by the app's wallet
	Price           math.LegacyDec
	OrderType       OrderType
}

// IsComplete - checks if the slice of orders contains all the orders aggregated in the entry
func (obe OrderBookEntry) IsComplete() bool {
	amount := math.ZeroInt()
	for _, o := range obe.Orders {
		oAmount, ok := math.NewIntFromString(o.Amount)
		if !ok {
			continue
		}
		amount = amount.Add(oAmount)
	}

	aggAmount, ok := math.NewIntFromString(obe.AggregatedOrder.Amount)
	if !ok {
		return false
	}

	return amount.Equal(aggAmount)
}

func (obe OrderBookEntry) GetAmount() math.Int {
	amount, _ := math.NewIntFromString(obe.AggregatedOrder.Amount)

	return amount
}

// OrderBook - contains a list of entries
type OrderBook struct {
	Buys  []OrderBookEntry
	Sells []OrderBookEntry
}
