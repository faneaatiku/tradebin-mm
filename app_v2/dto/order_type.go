package dto

const (
	orderTypeBuy  = "buy"
	orderTypeSell = "sell"
)

type OrderType string

func (o OrderType) String() string {
	return string(o)
}

func (o OrderType) IsValid() bool {
	return o == orderTypeBuy || o == orderTypeSell
}

func (o OrderType) IsBuy() bool {
	return o == orderTypeBuy
}

func (o OrderType) IsSell() bool {
	return o == orderTypeSell
}

func (o OrderType) GetOpposite() OrderType {
	if o == orderTypeBuy {
		return orderTypeSell
	}

	return orderTypeBuy
}

func NewOrderType(orderType string) OrderType {
	return OrderType(orderType)
}

func NewOrderTypeBuy() OrderType {
	return orderTypeBuy
}

func NewOrderTypeSell() OrderType {
	return orderTypeSell
}
