package config

import "fmt"

func NewConfigError(msg string) error {
	return fmt.Errorf("config error: %s", msg)
}

type Client struct {
	Grpc       string `yaml:"grpc"`
	TLSEnabled bool   `yaml:"tls"`
}

func (c *Client) Validate() error {
	if c.Grpc == "" {
		return NewConfigError("grpc is required")
	}

	return nil
}

type Wallet struct {
	Mnemonic string `yaml:"mnemonic"`
}

func (w *Wallet) Validate() error {
	if w.Mnemonic == "" {
		return NewConfigError("mnemonic is required")
	}

	return nil
}

type Market struct {
	BaseDenom  string `yaml:"base_denom"`
	QuoteDenom string `yaml:"quote_denom"`
}

func (m *Market) Validate() error {
	if m.BaseDenom == "" {
		return NewConfigError("base_denom is required")
	}

	if m.QuoteDenom == "" {
		return NewConfigError("quote_denom is required")
	}

	return nil
}

func (m *Market) GetBaseDenom() string {
	return m.BaseDenom
}

func (m *Market) GetQuoteDenom() string {
	return m.QuoteDenom
}

func (m *Market) GetMarketId() string {
	return fmt.Sprintf("%s/%s", m.BaseDenom, m.QuoteDenom)
}

type Volume struct {
	Min           float64 `yaml:"min"`
	Max           float64 `yaml:"max"`
	TradeInterval float64 `yaml:"trade_interval"`
	ExtraMin      float64 `yaml:"extra_min"`
	ExtraMax      float64 `yaml:"extra_max"`
	ExtraEvery    int64   `yaml:"extra_every"`
	Strategy      string  `yaml:"strategy"`
	HoldBack      int     `yaml:"hold_back_interval"`
}

func (v *Volume) GetMin() int64 {
	return int64(v.Min)
}

func (v *Volume) GetMax() int64 {
	return int64(v.Max)
}

func (v *Volume) GetTradeInterval() int {
	return int(v.TradeInterval)
}

func (v *Volume) GetExtraMin() int64 {
	return int64(v.ExtraMin)
}

func (v *Volume) GetExtraMax() int64 {
	return int64(v.ExtraMax)
}

func (v *Volume) GetExtraEvery() int64 {
	return v.ExtraEvery
}

func (v *Volume) GetStrategy() string { return v.Strategy }

func (v *Volume) GetHoldBackSeconds() int {
	return v.HoldBack
}

func (v *Volume) Validate() error {
	if v.Min <= 0 {
		return NewConfigError("min is required")
	}

	if v.Max < 0 {
		return NewConfigError("max is required")
	}

	if v.TradeInterval < 30 {
		return NewConfigError("trade_interval is required to be higher than 30 or equal")
	}

	if v.Min > v.Max {
		return NewConfigError("min volume is greater than max volume ")
	}

	if v.ExtraEvery <= 0 {
		return NewConfigError("extra_every must be higher than 0")
	}

	if v.HoldBack <= 0 {
		v.HoldBack = v.GetTradeInterval()
	}

	return nil
}

type Logging struct {
	Level string `yaml:"level"`
}

type Config struct {
	Orders      Orders      `yaml:"orders"`
	Market      Market      `yaml:"market"`
	Volume      Volume      `yaml:"volume"`
	Wallet      Wallet      `yaml:"wallet"`
	Client      Client      `yaml:"client"`
	Logging     Logging     `yaml:"logging"`
	Transaction Transaction `yaml:"transaction"`
}

func (c *Config) Validate() error {
	if err := c.Orders.Validate(); err != nil {
		return err
	}

	if err := c.Market.Validate(); err != nil {
		return err
	}

	if err := c.Volume.Validate(); err != nil {
		return err
	}

	if err := c.Wallet.Validate(); err != nil {
		return err
	}

	if err := c.Client.Validate(); err != nil {
		return err
	}

	if err := c.Transaction.Validate(); err != nil {
		return err
	}

	return nil
}
