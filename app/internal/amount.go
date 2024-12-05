package internal

import (
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"math"
	"strings"
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

func TruncateToStep(num, step *sdk.Dec) (*sdk.Dec, error) {
	numberFloat := num.MustFloat64()
	stepFloat := step.MustFloat64()

	stepStr := fmt.Sprintf("%f", stepFloat)
	stepDecimals := len(strings.Split(strings.TrimRight(stepStr, "0"), ".")[1])

	// Compute the truncation factor based on the numberFloat of decimals in stepFloat
	factor := math.Pow(10, float64(stepDecimals))

	// Truncate the numberFloat
	truncated := math.Trunc(numberFloat*factor) / factor

	// Format the truncated numberFloat back to a string with the desired precision
	format := fmt.Sprintf("%%.%df", stepDecimals)
	resultStr := fmt.Sprintf(format, truncated)

	result, err := sdk.NewDecFromStr(resultStr)
	if err != nil {
		return nil, err
	}

	return &result, nil
}
