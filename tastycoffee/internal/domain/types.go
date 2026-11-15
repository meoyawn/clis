package domain

// Money values are rubles. Option IDs are the product-specific value IDs
// accepted in cart item strings, not the underlying catalog option IDs.
type ProductOption struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type OrderProduct struct {
	ID       int64           `json:"id"`
	Name     string          `json:"name"`
	Quantity int             `json:"quantity"`
	Price    float64         `json:"price"`
	Options  []ProductOption `json:"options"`
}

type PriceLine struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Delivery struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Address     string  `json:"address"`
	Method      string  `json:"method"`
	TrackURL    *string `json:"track_url,omitempty"`
	DateSending string  `json:"date_sending,omitempty"`
}

type Order struct {
	ID        int64          `json:"id"`
	CreatedAt string         `json:"created_at"`
	Status    string         `json:"status"`
	Total     float64        `json:"total"`
	Paid      bool           `json:"paid"`
	Products  []OrderProduct `json:"products"`
	Prices    []PriceLine    `json:"prices"`
	Delivery  Delivery       `json:"delivery"`
}

type OrderPage struct {
	Orders []Order `json:"orders"`
	Page   int     `json:"page"`
	Pages  int     `json:"pages"`
	Total  int     `json:"total"`
}

type Account struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	PersonalDiscount float64 `json:"personal_discount"`
	OrdersCount      int     `json:"orders_count"`
}
