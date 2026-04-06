package exchange

import "context"

// OrderResult — результат исполнения ордера на любой бирже.
type OrderResult struct {
	OrderID string
	Price   float64
	Qty     float64
}

// RestClient — общий интерфейс REST-клиента биржи.
type RestClient interface {
	PlaceMarketOrder(ctx context.Context, symbol, side string, qty float64) (OrderResult, error)
	CancelOrder(ctx context.Context, symbol, orderID string) error
}
