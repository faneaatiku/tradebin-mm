package internal

import "strings"

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
