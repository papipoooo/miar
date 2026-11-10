package practice8

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Integration tests require the services from practical work 7 to run in Docker.
// They check HTTP communication, Catalog/PostgreSQL and asynchronous RabbitMQ events.
var httpClient = &http.Client{Timeout: 8 * time.Second}

const (
	accountsURL = "http://localhost:8081"
	catalogURL  = "http://localhost:8082"
	cartsURL    = "http://localhost:8083"
	deliveryURL = "http://localhost:8084"
	paymentsURL = "http://localhost:8085"
	ordersURL   = "http://localhost:8086"
)

func call(t *testing.T, method, endpoint string, payload any, wantCode int, result any) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v (check docker compose ps)", method, endpoint, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantCode {
		t.Fatalf("%s %s: HTTP %d, expected %d; body: %s", method, endpoint, resp.StatusCode, wantCode, strings.TrimSpace(string(data)))
	}
	if result != nil {
		if err := json.Unmarshal(data, result); err != nil {
			t.Fatalf("invalid JSON: %s; %v", data, err)
		}
	}
}

func TestIntegrationHealth(t *testing.T) {
	services := []struct{ name, endpoint string }{
		{"accounts", accountsURL}, {"catalog", catalogURL},
		{"carts", cartsURL}, {"delivery", deliveryURL},
		{"payments", paymentsURL}, {"orders", ordersURL},
	}
	for _, svc := range services {
		t.Run(svc.name, func(t *testing.T) {
			var res struct{ Service, Status string }
			call(t, http.MethodGet, svc.endpoint+"/health", nil, 200, &res)
			if res.Service != svc.name || res.Status != "ok" {
				t.Fatalf("unexpected health response: %+v", res)
			}
			t.Logf("%s: HTTP 200, status=ok", svc.name)
		})
	}
}

func TestIntegrationAccounts(t *testing.T) {
	email := fmt.Sprintf("pr8_%d@example.test", time.Now().UnixNano())
	var user struct{ UserID, Email, Name string }
	call(t, "POST", accountsURL+"/users", map[string]any{
		"email": email, "password": "training123", "name": "Student",
	}, 201, &user)
	if user.UserID == "" || user.Email != email {
		t.Fatalf("invalid user: %+v", user)
	}
	var login struct{ AccessToken string }
	call(t, "POST", accountsURL+"/auth/login", map[string]any{
		"email": email, "password": "training123",
	}, 200, &login)
	if login.AccessToken == "" {
		t.Fatal("login did not return access token")
	}
	call(t, "POST", accountsURL+"/auth/login", map[string]any{
		"email": email, "password": "invalid",
	}, 401, nil)
	t.Logf("user=%s; registration HTTP 201, login HTTP 200, invalid password HTTP 401", user.UserID)
}

func TestIntegrationCatalogPostgreSQL(t *testing.T) {
	var item struct {
		ProductID, Name, Category string
		Price                     float64
		Stock                     int
	}
	call(t, "GET", catalogURL+"/products/p-7", nil, 200, &item)
	if item.ProductID != "p-7" || item.Price != 1500 || item.Stock < 2 {
		t.Fatalf("unexpected PostgreSQL item: %+v", item)
	}
	var listing struct {
		Items []json.RawMessage
		Total int
	}
	call(t, "GET", catalogURL+"/products?q="+url.QueryEscape(item.Name), nil, 200, &listing)
	if listing.Total < 1 || len(listing.Items) == 0 {
		t.Fatal("catalog search returned no database rows")
	}
	call(t, "GET", catalogURL+"/products/missing-pr8", nil, 404, nil)
	t.Logf("PostgreSQL-backed catalog: product=%s, price=%.0f; HTTP 200/404 verified", item.ProductID, item.Price)
}

func TestIntegrationDelivery(t *testing.T) {
	var options struct{ Options []json.RawMessage }
	call(t, "GET", deliveryURL+"/delivery/options?city=Moscow", nil, 200, &options)
	if len(options.Options) != 2 {
		t.Fatalf("want 2 delivery methods, got %d", len(options.Options))
	}
	var quote struct {
		DeliveryFee float64
		EtaDays     int
	}
	call(t, "POST", deliveryURL+"/delivery/quotes", map[string]any{
		"address":  map[string]string{"city": "Moscow", "street": "Tverskaya", "house": "1"},
		"methodId": "courier",
		"items":    []map[string]any{{"productId": "p-7", "quantity": 2, "unitPrice": 1500}},
	}, 200, &quote)
	if quote.DeliveryFee != 350 || quote.EtaDays != 2 {
		t.Fatalf("incorrect delivery quote: %+v", quote)
	}
	t.Logf("delivery: methods=%d, fee=%.0f RUB, ETA=%d days", len(options.Options), quote.DeliveryFee, quote.EtaDays)
}

func TestIntegrationCartCatalog(t *testing.T) {
	uid := fmt.Sprintf("pr8-cart-%d", time.Now().UnixNano())
	var cart struct {
		UserID string
		Total  float64
		Items  []json.RawMessage
	}
	call(t, "POST", cartsURL+"/carts/"+uid+"/items", map[string]any{
		"productId": "p-7", "quantity": 2,
	}, 200, &cart)
	if cart.Total != 3000 || len(cart.Items) != 1 {
		t.Fatalf("cart/catalog HTTP integration failed: %+v", cart)
	}
	call(t, "GET", cartsURL+"/carts/"+uid, nil, 200, &cart)
	if cart.Total != 3000 {
		t.Fatalf("cart was not saved: %+v", cart)
	}
	t.Logf("Cart -> Catalog HTTP: user=%s, total=%.0f RUB", uid, cart.Total)
}

func TestIntegrationOrderPaymentRabbitMQ(t *testing.T) {
	uid := fmt.Sprintf("pr8-order-%d", time.Now().UnixNano())
	call(t, "POST", cartsURL+"/carts/"+uid+"/items", map[string]any{
		"productId": "p-7", "quantity": 2,
	}, 200, nil)
	var order struct {
		OrderID, UserID, Status, PaymentID, DeliveryID string
		Total                                          float64
	}
	call(t, "POST", ordersURL+"/orders", map[string]any{
		"userId":           uid,
		"address":          map[string]string{"city": "Moscow", "street": "Tverskaya", "house": "1"},
		"deliveryMethodId": "courier",
	}, 201, &order)
	if order.OrderID == "" || order.PaymentID == "" || order.DeliveryID == "" || order.Status != "awaiting_payment" || order.Total != 3350 {
		t.Fatalf("order sync integration failed: %+v", order)
	}
	var delivery struct{ DeliveryID, OrderID string }
	call(t, "GET", deliveryURL+"/deliveries/"+order.DeliveryID, nil, 200, &delivery)
	if delivery.OrderID != order.OrderID {
		t.Fatalf("delivery was not linked to order: %+v", delivery)
	}
	t.Logf("Order -> Cart/Delivery/Payment via HTTP: order=%s, invoice=%s, total=%.0f", order.OrderID, order.PaymentID, order.Total)

	var payment struct{ Status, PaymentID string }
	call(t, "POST", paymentsURL+"/payments/"+order.PaymentID+"/confirm", map[string]string{
		"providerTransactionId": fmt.Sprintf("pr8-%d", time.Now().UnixNano()),
	}, 200, &payment)
	if payment.Status != "confirmed" {
		t.Fatalf("payment not confirmed: %+v", payment)
	}

	waitStatus(t, ordersURL+"/orders/"+order.OrderID, "paid")
	t.Log("RabbitMQ payment.confirmed: order status changed to paid")

	var cancelled struct{ Status string }
	call(t, "POST", ordersURL+"/orders/"+order.OrderID+"/cancel", map[string]string{
		"reason": "Integration test cancellation",
	}, 200, &cancelled)
	if cancelled.Status != "cancelled" {
		t.Fatalf("order cancellation failed: %+v", cancelled)
	}
	waitStatus(t, paymentsURL+"/payments/"+order.PaymentID, "refunded")
	t.Log("RabbitMQ order.cancelled: payment status changed to refunded")
}

func waitStatus(t *testing.T, endpoint, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		var current struct{ Status string }
		call(t, "GET", endpoint, nil, 200, &current)
		last = current.Status
		if last == want {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for status=%s, last=%s, endpoint=%s", want, last, endpoint)
}
