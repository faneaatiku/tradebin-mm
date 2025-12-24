package dto

type MarketPair struct {
	base  string
	quote string
}

func NewMarketPair(base, quote string) *MarketPair {
	return &MarketPair{base, quote}
}

func (p *MarketPair) GetBase() string {
	return p.base
}

func (p *MarketPair) GetQuote() string {
	return p.quote
}
