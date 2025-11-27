package internal

import (
	"fmt"
	"math"
	"strings"

	sdkMath "cosmossdk.io/math"
)

func TrimAmountTrailingZeros(amount string) string {
	if !strings.Contains(amount, ".") {
		return amount
	}

	result := strings.TrimRight(amount, "0") // Remove trailing zeros
	if strings.HasSuffix(result, ".") {
		result = strings.TrimSuffix(result, ".") // Remove decimal point if no fractional part
	}

	return result
}

func TruncateToStep(num, step *sdkMath.LegacyDec) (*sdkMath.LegacyDec, error) {
	stepFloat := step.MustFloat64()

	stepStr := fmt.Sprintf("%f", stepFloat)
	stepDecimals := len(strings.Split(strings.TrimRight(stepStr, "0"), ".")[1])

	// Compute the truncation factor based on the numberFloat of decimals in stepFloat
	factor := math.Pow(10, float64(stepDecimals))

	// Truncate the numberFloat
	truncated := num.MulInt64(int64(factor)).TruncateDec().QuoInt64(int64(factor))
	//truncated := math.Trunc(numberFloat*factor) / factor

	return &truncated, nil
}
