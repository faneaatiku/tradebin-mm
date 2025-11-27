package config

type Transaction struct {
	GasAdjustment float64 `yaml:"gas_adjustment"`
	GasPrices     string  `yaml:"gas_prices"`
	ChainId       string  `yaml:"chain_id"`
	AddressPrefix string  `yaml:"address_prefix"`
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

func (t *Transaction) GetAddressPrefix() string {
	if t.AddressPrefix == "" {
		return "bze"
	}

	return t.AddressPrefix
}

func (t *Transaction) Validate() error {

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
