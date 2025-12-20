package config

import (
	"cosmossdk.io/math"
)

var (
	ErrInvalidBuyNo             = NewConfigError("invalid buy_no")
	ErrInvalidSellNo            = NewConfigError("invalid sell_no")
	ErrInvalidStep              = NewConfigError("invalid step")
	ErrInvalidStartPrice        = NewConfigError("invalid start_price")
	ErrInvalidMinAmount         = NewConfigError("invalid min_amount")
	ErrInvalidMaxAmount         = NewConfigError("invalid max_amount")
	ErrInvalidLiquidityStrategy = NewConfigError("liquidity_strategy must be 'fixed' or 'clmm'")
	ErrInvalidRangeType         = NewConfigError("range_type must be 'percentage' or 'fixed' for CLMM")
	ErrInvalidRangePct          = NewConfigError("range_pct must be between 0 and 100 for CLMM")
	ErrInvalidRangeBounds       = NewConfigError("range_lower and range_upper required for fixed range type")
	ErrInvalidRangeValues       = NewConfigError("range_upper must be greater than range_lower")
)

type Orders struct {
	Buy         int    `yaml:"buy_no"`
	Sell        int    `yaml:"sell_no"`
	Step        string `yaml:"step"`
	SpreadSteps int64  `yaml:"spread_steps"`
	StartPrice  string `yaml:"start_price"`
	MinAmount   int64  `yaml:"min_amount"`
	MaxAmount   int64  `yaml:"max_amount"`
	HoldBack    int    `yaml:"hold_back_interval"`

	// CLMM strategy fields
	LiquidityStrategy   string  `yaml:"liquidity_strategy"`
	RangeType           string  `yaml:"range_type"`
	RangePct            float64 `yaml:"range_pct"`
	RangeLower          string  `yaml:"range_lower"`
	RangeUpper          string  `yaml:"range_upper"`
	ConcentrationFactor float64 `yaml:"concentration_factor"`
}

func (o *Orders) Validate() error {
	if o.Buy <= 0 {
		return ErrInvalidBuyNo
	}

	if o.Sell <= 0 {
		return ErrInvalidSellNo
	}

	stepDec, err := math.LegacyNewDecFromStr(o.Step)
	if err != nil {
		return ErrInvalidStep
	}

	if !stepDec.IsPositive() {
		return ErrInvalidStep
	}

	startPriceDec, err := math.LegacyNewDecFromStr(o.StartPrice)
	if err != nil {
		return ErrInvalidStartPrice
	}

	if !startPriceDec.IsPositive() {
		return ErrInvalidStartPrice
	}

	if o.MinAmount <= 0 {
		return ErrInvalidMinAmount
	}

	if o.MaxAmount <= 0 {
		return ErrInvalidMaxAmount
	}

	// Set default liquidity strategy if not specified
	if o.LiquidityStrategy == "" {
		o.LiquidityStrategy = "fixed"
	}

	// Validate liquidity strategy
	if o.LiquidityStrategy != "fixed" && o.LiquidityStrategy != "clmm" {
		return ErrInvalidLiquidityStrategy
	}

	// CLMM-specific validation
	if o.LiquidityStrategy == "clmm" {
		// Validate range type
		if o.RangeType != "percentage" && o.RangeType != "fixed" {
			return ErrInvalidRangeType
		}

		// Validate percentage range
		if o.RangeType == "percentage" {
			if o.RangePct <= 0 || o.RangePct > 100 {
				return ErrInvalidRangePct
			}
		}

		// Validate fixed range
		if o.RangeType == "fixed" {
			if o.RangeLower == "" || o.RangeUpper == "" {
				return ErrInvalidRangeBounds
			}

			lowerDec, err := math.LegacyNewDecFromStr(o.RangeLower)
			if err != nil {
				return ErrInvalidRangeBounds
			}

			upperDec, err := math.LegacyNewDecFromStr(o.RangeUpper)
			if err != nil {
				return ErrInvalidRangeBounds
			}

			if !lowerDec.IsPositive() || !upperDec.IsPositive() {
				return ErrInvalidRangeBounds
			}

			if upperDec.LTE(lowerDec) {
				return ErrInvalidRangeValues
			}
		}

		// Set default concentration factor if not specified
		if o.ConcentrationFactor <= 0 {
			o.ConcentrationFactor = 2.0
		}
	}

	return nil
}

func (o *Orders) GetBuyNo() int {
	return o.Buy
}

func (o *Orders) GetSellNo() int {
	return o.Sell
}

func (o *Orders) GetStartPriceDec() *math.LegacyDec {
	num, err := math.LegacyNewDecFromStr(o.StartPrice)
	if err != nil {
		panic(err)
	}

	return &num
}

func (o *Orders) GetPriceStepDec() *math.LegacyDec {
	num, err := math.LegacyNewDecFromStr(o.Step)
	if err != nil {
		panic(err)
	}

	return &num
}

func (o *Orders) GetOrderMinAmount() *math.Int {
	num := math.NewInt(o.MinAmount)

	return &num
}

func (o *Orders) GetOrderMaxAmount() *math.Int {
	num := math.NewInt(o.MaxAmount)

	return &num
}

func (o *Orders) GetSpreadSteps() *math.Int {
	num := math.NewInt(o.SpreadSteps)

	return &num
}

func (o *Orders) GetHoldBackSeconds() int {
	return o.HoldBack
}

func (o *Orders) GetLiquidityStrategy() string {
	if o.LiquidityStrategy == "" {
		return "fixed" // Default to fixed strategy for backward compatibility
	}
	return o.LiquidityStrategy
}

func (o *Orders) GetRangeType() string {
	return o.RangeType
}

func (o *Orders) GetRangePct() float64 {
	return o.RangePct
}

func (o *Orders) GetRangeLowerDec() *math.LegacyDec {
	if o.RangeLower == "" {
		return nil
	}
	num, err := math.LegacyNewDecFromStr(o.RangeLower)
	if err != nil {
		panic(err)
	}
	return &num
}

func (o *Orders) GetRangeUpperDec() *math.LegacyDec {
	if o.RangeUpper == "" {
		return nil
	}
	num, err := math.LegacyNewDecFromStr(o.RangeUpper)
	if err != nil {
		panic(err)
	}
	return &num
}

func (o *Orders) GetConcentrationFactor() float64 {
	if o.ConcentrationFactor <= 0 {
		return 2.0 // Default concentration factor
	}
	return o.ConcentrationFactor
}
