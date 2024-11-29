package config

type Transaction struct {
	Gas           uint64  `yaml:"gas"`
	Fee           float64 `yaml:"fee"`
	GasAdjustment float64 `yaml:"gas_adjustment"`
	GasPrices     string  `yaml:"gas_prices"`
	ChainId       string  `yaml:"chain_id"`
}

func (t *Transaction) GetGas() uint64 {
	return t.Gas
}

func (t *Transaction) GetGasPrices() string {
	return t.GasPrices
}

func (t *Transaction) GetGasAdjustment() float64 {
	return t.GasAdjustment
}

func (t *Transaction) GetChainId() string {
	return t.ChainId
}

func (t *Transaction) Validate() error {
	if t.Gas == 0 {
		return NewConfigError("gas is required")
	}

	if t.Fee == 0 {
		return NewConfigError("fee is required")
	}

	if t.GasAdjustment == 0 {
		return NewConfigError("gas_adjustment is required")
	}

	if t.GasPrices == "" {
		return NewConfigError("gas_prices is required")
	}

	if t.ChainId == "" {
		return NewConfigError("chain_id is required")
	}

	return nil
}
