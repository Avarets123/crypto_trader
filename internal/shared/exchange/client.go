package exchange

import "context"

// OrderResult — результат исполнения ордера на любой бирже.
type OrderResult struct {
	OrderID string
	Price   float64
	Qty     float64
}

// OpenOrder — активный (неисполненный) ордер.
type OpenOrder struct {
	OrderID string
	Symbol  string
	Side    string  // "BUY" / "SELL"
	Price   float64
	Qty     float64
}

// RestClient — общий интерфейс REST-клиента биржи.
type RestClient interface {
	PlaceMarketOrder(ctx context.Context, symbol, side string, qty float64) (OrderResult, error)
	CancelOrder(ctx context.Context, symbol, orderID string) error
	PlaceLimitOrder(ctx context.Context, symbol, side string, qty, price float64, postOnly bool) (OrderResult, error)
	GetOpenOrders(ctx context.Context, symbol string) ([]OpenOrder, error)
	GetFreeUSDT(ctx context.Context) (float64, error)
}
