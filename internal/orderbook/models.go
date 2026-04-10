package orderbook

import "time"

// Entry — одна запись стакана (цена + количество).
type Entry struct {
	Price string `json:"price"`
	Qty   string `json:"qty"`
}

// OrderBook — снимок стакана по символу.
type OrderBook struct {
	Symbol    string    `json:"symbol"`
	Bids      []Entry   `json:"bids"`
	Asks      []Entry   `json:"asks"`
	UpdatedAt time.Time `json:"updated_at"`
}
