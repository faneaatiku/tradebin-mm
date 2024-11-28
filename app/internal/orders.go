package internal

import (
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/cosmos/cosmos-sdk/types"
	"math/big"
	"math/rand"
	"slices"
	"time"
)

func SortOrdersByPrice(orders []tradebinTypes.Order, reverse bool) {
	mult := 1
	if reverse {
		mult = -1
	}

	slices.SortStableFunc(orders, func(i, j tradebinTypes.Order) int {
		decI := types.MustNewDecFromStr(i.GetPrice())
		decJ := types.MustNewDecFromStr(j.GetPrice())
		diff := decI.Sub(decJ)
		if diff.IsPositive() {
			return 1 * mult
		} else if diff.IsNegative() {
			return -1 * mult
		}

		return 0
	})
}

func MustRandomInt(min, max *types.Int) types.Int {
	// Ensure min is less than or equal to max
	if min.GT(*max) {
		panic("min must be less than or equal to max")
	}

	// Calculate the range
	diff := max.Sub(*min).BigInt()

	// Generate a random big.Int in the range [0, diff]
	rand.Seed(time.Now().UnixNano())
	randomBigInt := new(big.Int).Rand(rand.New(rand.NewSource(time.Now().UnixNano())), diff)

	// Add the minimum to the random offset
	randomBigInt.Add(randomBigInt, min.BigInt())

	// Return the result as types.Int
	return types.NewIntFromBigInt(randomBigInt)
}
