package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/meoyawn/clis/tastycoffee/internal/domain"
)

type Client struct {
	http  *http.Client
	token string
}

func NewClient(httpClient *http.Client, session Session) (*Client, error) {
	if httpClient == nil {
		return nil, fmt.Errorf("HTTP client must not be nil")
	}
	token, err := session.Token()
	if err != nil {
		return nil, err
	}
	client := *httpClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &client, token: token}, nil
}

func (c *Client) get(ctx context.Context, path string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Origin+"/api/v2"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", Origin+"/order-histories")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("saved session rejected by Tasty Coffee (HTTP 401); sign in in Chrome and run auth import")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("request to Tasty Coffee %s returned HTTP %d", path, resp.StatusCode)
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		return fmt.Errorf("request to Tasty Coffee %s returned a non-JSON page; open %s in Chrome and check the session", path, Origin)
	}
	const maxBody = 16 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("read Tasty Coffee response: %w", err)
	}
	if len(body) > maxBody {
		return fmt.Errorf("response from Tasty Coffee exceeded 16 MiB; request a smaller --limit")
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("request to Tasty Coffee %s returned invalid JSON", path)
	}
	return nil
}

func (c *Client) Account(ctx context.Context) (domain.Account, error) {
	var account domain.Account
	err := c.get(ctx, "/me?with_legal=true", &account)
	if err == nil && account.ID <= 0 {
		err = fmt.Errorf("response from Tasty Coffee did not contain an account ID")
	}
	return account, err
}

type orderResponse struct {
	ID         int64   `json:"id"`
	CreatedAt  string  `json:"created_at"`
	TotalPrice float64 `json:"total_price"`
	IsPaid     bool    `json:"is_paid"`
	Status     struct {
		Name string `json:"name"`
	} `json:"status"`
	Products []struct {
		ProductID      int64   `json:"product_id"`
		ProductName    string  `json:"product_name"`
		Count          int     `json:"count"`
		Price          float64 `json:"price"`
		ProductOptions []struct {
			ProductOptionValueID int64  `json:"product_option_value_id"`
			Name                 string `json:"name"`
			Value                string `json:"value"`
		} `json:"product_options"`
	} `json:"products"`
	Prices   []domain.PriceLine `json:"prices"`
	Delivery struct {
		Code        string  `json:"code"`
		Name        string  `json:"name"`
		FullAddress string  `json:"full_address"`
		Method      string  `json:"method"`
		TrackURL    *string `json:"track_url"`
		DateSending string  `json:"date_sending"`
	} `json:"delivery"`
}

func (c *Client) Orders(ctx context.Context, page, limit int, current bool) (domain.OrderPage, error) {
	if page < 1 {
		return domain.OrderPage{}, fmt.Errorf("invalid page %d; must be positive", page)
	}
	if limit < 1 || limit > 100 {
		return domain.OrderPage{}, fmt.Errorf("invalid limit %d; use 1 through 100", limit)
	}
	query := url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(limit)}}
	if current {
		query.Set("current", "true")
	}
	var response struct {
		Data []orderResponse `json:"data"`
		Meta struct {
			CurrentPage int `json:"current_page"`
			LastPage    int `json:"last_page"`
			Total       int `json:"total"`
		} `json:"meta"`
	}
	if err := c.get(ctx, "/account/orders?"+query.Encode(), &response); err != nil {
		return domain.OrderPage{}, err
	}
	if response.Data == nil || response.Meta.CurrentPage < 1 || response.Meta.LastPage < 1 {
		return domain.OrderPage{}, fmt.Errorf("response from Tasty Coffee did not contain orders and pagination")
	}
	result := domain.OrderPage{Orders: make([]domain.Order, 0, len(response.Data)), Page: response.Meta.CurrentPage, Pages: response.Meta.LastPage, Total: response.Meta.Total}
	for _, raw := range response.Data {
		order := domain.Order{
			ID: raw.ID, CreatedAt: raw.CreatedAt, Status: raw.Status.Name, Total: raw.TotalPrice, Paid: raw.IsPaid,
			Products: []domain.OrderProduct{}, Prices: raw.Prices,
			Delivery: domain.Delivery{Code: raw.Delivery.Code, Name: raw.Delivery.Name, Address: raw.Delivery.FullAddress,
				Method: raw.Delivery.Method, TrackURL: raw.Delivery.TrackURL, DateSending: raw.Delivery.DateSending},
		}
		for _, product := range raw.Products {
			item := domain.OrderProduct{ID: product.ProductID, Name: product.ProductName, Quantity: product.Count, Price: product.Price, Options: []domain.ProductOption{}}
			for _, option := range product.ProductOptions {
				item.Options = append(item.Options, domain.ProductOption{ID: option.ProductOptionValueID, Name: option.Name, Value: option.Value})
			}
			order.Products = append(order.Products, item)
		}
		result.Orders = append(result.Orders, order)
	}
	return result, nil
}

// The API separates active and historical orders into distinct lists.
func (c *Client) Latest(ctx context.Context) (domain.Order, error) {
	var newest domain.Order
	var newestTime time.Time
	for _, current := range []bool{true, false} {
		page, err := c.Orders(ctx, 1, 1, current)
		if err != nil {
			return domain.Order{}, err
		}
		for _, order := range page.Orders {
			created, err := time.Parse(time.RFC3339Nano, order.CreatedAt)
			if err != nil {
				return domain.Order{}, fmt.Errorf("order %d has an invalid created_at timestamp", order.ID)
			}
			if newest.ID == 0 || created.After(newestTime) {
				newest, newestTime = order, created
			}
		}
	}
	if newest.ID == 0 {
		return domain.Order{}, fmt.Errorf("no orders found")
	}
	return newest, nil
}
