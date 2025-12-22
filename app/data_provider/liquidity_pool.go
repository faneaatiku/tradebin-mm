package data_provider

import (
	"context"
	"fmt"
	"tradebin-mm/app/internal"

	"cosmossdk.io/math"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/sirupsen/logrus"
)

// GeneratePoolId creates a pool ID from base and quote denoms
// Denoms are sorted lexicographically (alphabetically) and joined with underscore
// This matches the blockchain implementation
func GeneratePoolId(baseDenom, quoteDenom string) string {
	// Sort assets alphabetically using string comparison
	if baseDenom > quoteDenom {
		// Reverse the order of assets
		baseDenom, quoteDenom = quoteDenom, baseDenom
	}

	return fmt.Sprintf("%s_%s", baseDenom, quoteDenom)
}

type LiquidityPoolProvider struct {
	provider clientProvider
	logger   logrus.FieldLogger
}

func NewLiquidityPoolProvider(cl clientProvider, logger logrus.FieldLogger) (*LiquidityPoolProvider, error) {
	if cl == nil || logger == nil {
		return nil, internal.NewInvalidDependenciesErr("NewLiquidityPoolProvider")
	}

	return &LiquidityPoolProvider{
		provider: cl,
		logger:   logger.WithField("service", "DataProvider.LiquidityPool"),
	}, nil
}

// GetPool retrieves a liquidity pool by its ID
// Returns nil pool (not error) if pool doesn't exist
func (lp *LiquidityPoolProvider) GetPool(poolId string) (*tradebinTypes.LiquidityPool, error) {
	lp.logger.WithField("pool_id", poolId).Debug("getting liquidity pool")

	qc, err := lp.provider.GetTradebinQueryClient()
	if err != nil {
		return nil, fmt.Errorf("failed to get tradebin query client: %w", err)
	}

	res, err := qc.LiquidityPool(context.Background(), &tradebinTypes.QueryLiquidityPoolRequest{
		PoolId: poolId,
	})
	if err != nil {
		// Check if error is "pool not found" - return nil pool instead of error
		if isPoolNotFoundError(err) {
			lp.logger.WithField("pool_id", poolId).Debug("pool not found")
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query liquidity pool: %w", err)
	}

	if res.Pool == nil {
		lp.logger.WithField("pool_id", poolId).Debug("pool not found in response")
		return nil, nil
	}

	lp.logger.WithField("pool_id", poolId).Debug("liquidity pool fetched successfully")
	return res.Pool, nil
}

// GetSpotPrice calculates the spot price from pool reserves
// Spot price = reserve_quote / reserve_base (quote tokens per 1 base token)
// Returns error if reserves are zero to avoid division by zero
func (lp *LiquidityPoolProvider) GetSpotPrice(pool *tradebinTypes.LiquidityPool) (*math.LegacyDec, error) {
	if pool == nil {
		return nil, fmt.Errorf("pool is nil")
	}

	if pool.ReserveBase.IsZero() {
		return nil, fmt.Errorf("pool reserve_base is zero, cannot calculate spot price")
	}

	if pool.ReserveQuote.IsZero() {
		return nil, fmt.Errorf("pool reserve_quote is zero, cannot calculate spot price")
	}

	// Spot price = reserve_quote / reserve_base
	reserveQuoteDec := pool.ReserveQuote.ToLegacyDec()
	reserveBaseDec := pool.ReserveBase.ToLegacyDec()

	spotPrice := reserveQuoteDec.Quo(reserveBaseDec)

	lp.logger.WithFields(map[string]interface{}{
		"pool_id":       pool.Id,
		"reserve_base":  pool.ReserveBase.String(),
		"reserve_quote": pool.ReserveQuote.String(),
		"spot_price":    spotPrice.String(),
	}).Debug("calculated spot price")

	return &spotPrice, nil
}

// isPoolNotFoundError checks if the error indicates pool was not found
// This is a helper to distinguish "pool doesn't exist" from other errors
func isPoolNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	// Check common error messages for "not found"
	errMsg := err.Error()
	return contains(errMsg, "not found") || contains(errMsg, "does not exist") || contains(errMsg, "no pool")
}

// contains checks if a string contains a substring (case-insensitive check would be better but this works)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			indexOf(s, substr) >= 0))
}

// indexOf returns the index of substr in s, or -1 if not found
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
