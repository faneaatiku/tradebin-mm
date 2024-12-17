package config

import (
	"github.com/cosmos/cosmos-sdk/types"
)

var (
	ErrInvalidBuyNo      = NewConfigError("invalid buy_no")
	ErrInvalidSellNo     = NewConfigError("invalid sell_no")
	ErrInvalidStep       = NewConfigError("invalid step")
	ErrInvalidStartPrice = NewConfigError("invalid start_price")
	ErrInvalidMinAmount  = NewConfigError("invalid min_amount")
	ErrInvalidMaxAmount  = NewConfigError("invalid max_amount")
)

type Orders struct {
	Buy         int    `yaml:"buy_no"`
	Sell        int    `yaml:"sell_no"`
	Step        string `yaml:"step"`
	SpreadSteps int64  `yaml:"spread_steps"`
	StartPrice  string `yaml:"start_price"`
	MinAmount   int64  `yaml:"min_amount"`
	MaxAmount   int64  `yaml:"max_amount"`
}

func (o *Orders) Validate() error {
	if o.Buy <= 0 {
		return ErrInvalidBuyNo
	}

	if o.Sell <= 0 {
		return ErrInvalidSellNo
	}

	stepDec, err := types.NewDecFromStr(o.Step)
	if err != nil {
		return ErrInvalidStep
	}

	if !stepDec.IsPositive() {
		return ErrInvalidStep
	}

	startPriceDec, err := types.NewDecFromStr(o.StartPrice)
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

	return nil
}

func (o *Orders) GetBuyNo() int {
	return o.Buy
}

func (o *Orders) GetSellNo() int {
	return o.Sell
}

func (o *Orders) GetStartPriceDec() *types.Dec {
	num, err := types.NewDecFromStr(o.StartPrice)
	if err != nil {
		panic(err)
	}

	return &num
}

func (o *Orders) GetPriceStepDec() *types.Dec {
	num, err := types.NewDecFromStr(o.Step)
	if err != nil {
		panic(err)
	}

	return &num
}

func (o *Orders) GetOrderMinAmount() *types.Int {
	num := types.NewInt(o.MinAmount)

	return &num
}

func (o *Orders) GetOrderMaxAmount() *types.Int {
	num := types.NewInt(o.MaxAmount)

	return &num
}

func (o *Orders) GetSpreadSteps() *types.Int {
	num := types.NewInt(o.SpreadSteps)

	return &num
}
