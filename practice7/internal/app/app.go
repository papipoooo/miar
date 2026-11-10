// Package app contains a small educational marketplace built as six separately
// deployed HTTP services. Data persistence is demonstrated by catalog/Postgres;
// other services use concurrency-safe in-memory repositories.
package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq"
	amqp "github.com/rabbitmq/amqp091-go"
)

var client = &http.Client{Timeout: 5 * time.Second}
var nextID atomic.Uint64

func id(prefix string, start uint64) string {
	return fmt.Sprintf("%s-%d", prefix, start+nextID.Add(1)-1)
}
func env(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}
func write(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload != nil {
		_ = json.NewEncoder(w).Encode(payload)
	}
}
func fail(w http.ResponseWriter, status int, code, message string) {
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "INVALID_JSON", err.Error())
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		fail(w, 400, "INVALID_JSON", "Ожидается один JSON-объект")
		return false
	}
	return true
}
func request(ctx context.Context, method, url string, body any, result any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return resp.StatusCode, fmt.Errorf("%s: %s", resp.Status, string(b))
	}
	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}
func register(m *http.ServeMux, route string, h http.HandlerFunc) { m.HandleFunc(route, h) }
func Run(service string) {
	m := http.NewServeMux()
	m.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		write(w, 200, map[string]string{"service": service, "status": "ok"})
	})
	switch service {
	case "accounts":
		accounts(m)
	case "catalog":
		catalog(m)
	case "carts":
		carts(m)
	case "delivery":
		delivery(m)
	case "payments":
		payments(m)
	case "orders":
		orders(m)
	default:
		log.Fatalf("unknown service: %s", service)
	}
	port := env("PORT", "8080")
	server := &http.Server{Addr: ":" + port, Handler: m, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("service=%s listening on :%s", service, port)
	log.Fatal(server.ListenAndServe())
}

// ---- Accounts: four routes from practical #6 ----
type User struct {
	UserID   string `json:"userId"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Phone    string `json:"phone,omitempty"`
	Password string `json:"-"`
}

func accounts(m *http.ServeMux) {
	var mu sync.Mutex
	users := map[string]User{}
	tokens := map[string]string{}
	register(m, "POST /users", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Email    string `json:"email"`
			Password string `json:"password"`
			Name     string `json:"name"`
		}
		if !decode(w, r, &p) {
			return
		}
		if p.Email == "" || len(p.Password) < 4 || p.Name == "" {
			fail(w, 400, "INVALID_USER", "Укажите email, пароль (от 4 символов) и имя")
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, u := range users {
			if u.Email == p.Email {
				fail(w, 409, "EMAIL_EXISTS", "Email уже зарегистрирован")
				return
			}
		}
		u := User{UserID: id("u", 101), Email: p.Email, Name: p.Name, Password: p.Password}
		users[u.UserID] = u
		write(w, 201, u)
	})
	register(m, "POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if !decode(w, r, &p) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, u := range users {
			if u.Email == p.Email && u.Password == p.Password {
				b := make([]byte, 24)
				if _, err := rand.Read(b); err != nil {
					fail(w, 500, "RANDOM_ERROR", err.Error())
					return
				}
				token := hex.EncodeToString(b)
				tokens[token] = u.UserID
				write(w, 200, map[string]any{"accessToken": token, "expiresIn": 3600})
				return
			}
		}
		fail(w, 401, "BAD_CREDENTIALS", "Неверный email или пароль")
	})
	authorized := func(w http.ResponseWriter, r *http.Request, userid string) bool {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		owner := tokens[token]
		mu.Unlock()
		if token == "" || owner == "" {
			fail(w, 401, "UNAUTHORIZED", "Нужен Bearer token")
			return false
		}
		if owner != userid {
			fail(w, 403, "FORBIDDEN", "Нет доступа к чужому профилю")
			return false
		}
		return true
	}
	register(m, "GET /users/{userId}", func(w http.ResponseWriter, r *http.Request) {
		uid := r.PathValue("userId")
		if !authorized(w, r, uid) {
			return
		}
		mu.Lock()
		u, ok := users[uid]
		mu.Unlock()
		if !ok {
			fail(w, 404, "NOT_FOUND", "Пользователь не найден")
			return
		}
		write(w, 200, u)
	})
	register(m, "PATCH /users/{userId}", func(w http.ResponseWriter, r *http.Request) {
		uid := r.PathValue("userId")
		if !authorized(w, r, uid) {
			return
		}
		var p struct {
			Name  *string `json:"name"`
			Phone *string `json:"phone"`
		}
		if !decode(w, r, &p) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		u, ok := users[uid]
		if !ok {
			fail(w, 404, "NOT_FOUND", "Пользователь не найден")
			return
		}
		if p.Name != nil {
			if *p.Name == "" {
				fail(w, 400, "INVALID_NAME", "Имя не может быть пустым")
				return
			}
			u.Name = *p.Name
		}
		if p.Phone != nil {
			u.Phone = *p.Phone
		}
		users[uid] = u
		write(w, 200, u)
	})
}

// ---- Catalog: actual relational database storage, PostgreSQL ----
type Product struct {
	ProductID string  `json:"productId"`
	Name      string  `json:"name"`
	Category  string  `json:"category"`
	Price     float64 `json:"price"`
	Stock     int     `json:"stock"`
}

func catalog(m *http.ServeMux) {
	db, err := sql.Open("postgres", env("DATABASE_URL", "postgres://miar:miar_local_only@localhost:5432/marketplace?sslmode=disable"))
	if err != nil {
		log.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = db.PingContext(ctx)
		cancel()
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		log.Fatalf("postgres unavailable: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS products (
        id TEXT PRIMARY KEY, name TEXT NOT NULL, category TEXT NOT NULL,
        price NUMERIC(12,2) NOT NULL CHECK (price>=0), stock INT NOT NULL CHECK (stock>=0))`)
	if err != nil {
		log.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO products(id,name,category,price,stock) VALUES
        ('p-7','Наушники','electronics',1500,12),
        ('p-8','Книга','books',800,25) ON CONFLICT (id) DO NOTHING`)
	if err != nil {
		log.Fatal(err)
	}
	scan := func(rows *sql.Rows) ([]Product, error) {
		defer rows.Close()
		result := []Product{}
		for rows.Next() {
			var p Product
			if e := rows.Scan(&p.ProductID, &p.Name, &p.Category, &p.Price, &p.Stock); e != nil {
				return nil, e
			}
			result = append(result, p)
		}
		return result, rows.Err()
	}
	register(m, "GET /products", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		if page < 1 {
			page = 1
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit < 1 || limit > 100 {
			limit = 20
		}
		term := "%" + q.Get("q") + "%"
		cat := q.Get("category")
		var total int
		err := db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM products WHERE name ILIKE $1 AND ($2='' OR category=$2)`, term, cat).Scan(&total)
		if err != nil {
			fail(w, 500, "DB_ERROR", err.Error())
			return
		}
		rows, err := db.QueryContext(r.Context(), `SELECT id,name,category,price,stock FROM products WHERE name ILIKE $1 AND ($2='' OR category=$2) ORDER BY id LIMIT $3 OFFSET $4`, term, cat, limit, (page-1)*limit)
		if err != nil {
			fail(w, 500, "DB_ERROR", err.Error())
			return
		}
		products, err := scan(rows)
		if err != nil {
			fail(w, 500, "DB_ERROR", err.Error())
			return
		}
		write(w, 200, map[string]any{"items": products, "total": total, "page": page})
	})
	register(m, "GET /products/{productId}", func(w http.ResponseWriter, r *http.Request) {
		var p Product
		err := db.QueryRowContext(r.Context(), `SELECT id,name,category,price,stock FROM products WHERE id=$1`, r.PathValue("productId")).Scan(&p.ProductID, &p.Name, &p.Category, &p.Price, &p.Stock)
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, 404, "NOT_FOUND", "Товар не найден")
			return
		}
		if err != nil {
			fail(w, 500, "DB_ERROR", err.Error())
			return
		}
		write(w, 200, p)
	})
	register(m, "POST /products", func(w http.ResponseWriter, r *http.Request) {
		var p Product
		if !decode(w, r, &p) {
			return
		}
		if p.Name == "" || p.Category == "" || p.Price < 0 || p.Stock < 0 {
			fail(w, 400, "INVALID_PRODUCT", "Некорректные поля товара")
			return
		}
		p.ProductID = id("p", 100)
		_, err := db.ExecContext(r.Context(), `INSERT INTO products(id,name,category,price,stock) VALUES($1,$2,$3,$4,$5)`, p.ProductID, p.Name, p.Category, p.Price, p.Stock)
		if err != nil {
			fail(w, 500, "DB_ERROR", err.Error())
			return
		}
		write(w, 201, p)
	})
	register(m, "PATCH /products/{productId}", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Name  *string  `json:"name"`
			Price *float64 `json:"price"`
			Stock *int     `json:"stock"`
		}
		if !decode(w, r, &p) {
			return
		}
		var orig Product
		err := db.QueryRowContext(r.Context(), `SELECT id,name,category,price,stock FROM products WHERE id=$1`, r.PathValue("productId")).Scan(&orig.ProductID, &orig.Name, &orig.Category, &orig.Price, &orig.Stock)
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, 404, "NOT_FOUND", "Товар не найден")
			return
		}
		if err != nil {
			fail(w, 500, "DB_ERROR", err.Error())
			return
		}
		if p.Name != nil {
			orig.Name = *p.Name
		}
		if p.Price != nil {
			orig.Price = *p.Price
		}
		if p.Stock != nil {
			orig.Stock = *p.Stock
		}
		if orig.Name == "" || orig.Price < 0 || orig.Stock < 0 {
			fail(w, 400, "INVALID_PRODUCT", "Некорректные поля")
			return
		}
		_, err = db.ExecContext(r.Context(), `UPDATE products SET name=$2,price=$3,stock=$4 WHERE id=$1`, orig.ProductID, orig.Name, orig.Price, orig.Stock)
		if err != nil {
			fail(w, 500, "DB_ERROR", err.Error())
			return
		}
		write(w, 200, orig)
	})
}

// ---- Cart: REST call to Catalog when a product is added ----
type CartItem struct {
	ProductID string  `json:"productId"`
	Quantity  int     `json:"quantity"`
	UnitPrice float64 `json:"unitPrice"`
}
type Cart struct {
	UserID string     `json:"userId"`
	Items  []CartItem `json:"items"`
	Total  float64    `json:"total"`
}

func cartSnapshot(uid string, items map[string]CartItem) Cart {
	result := Cart{UserID: uid, Items: []CartItem{}}
	for _, it := range items {
		result.Items = append(result.Items, it)
		result.Total += it.UnitPrice * float64(it.Quantity)
	}
	return result
}
func carts(m *http.ServeMux) {
	var mu sync.Mutex
	all := map[string]map[string]CartItem{}
	register(m, "GET /carts/{userId}", func(w http.ResponseWriter, r *http.Request) {
		uid := r.PathValue("userId")
		mu.Lock()
		c := cartSnapshot(uid, all[uid])
		mu.Unlock()
		write(w, 200, c)
	})
	register(m, "POST /carts/{userId}/items", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			ProductID string `json:"productId"`
			Quantity  int    `json:"quantity"`
		}
		if !decode(w, r, &p) {
			return
		}
		if p.ProductID == "" || p.Quantity <= 0 {
			fail(w, 400, "INVALID_ITEM", "productId и quantity>0 обязательны")
			return
		}
		var product Product
		_, err := request(r.Context(), "GET", env("CATALOG_URL", "http://localhost:8082")+"/products/"+p.ProductID, nil, &product)
		if err != nil {
			fail(w, 502, "CATALOG_ERROR", err.Error())
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if all[r.PathValue("userId")] == nil {
			all[r.PathValue("userId")] = map[string]CartItem{}
		}
		userItems := all[r.PathValue("userId")]
		item := userItems[p.ProductID]
		if item.Quantity+p.Quantity > product.Stock {
			fail(w, 409, "NOT_ENOUGH_STOCK", "Недостаточный остаток товара")
			return
		}
		item = CartItem{ProductID: p.ProductID, Quantity: item.Quantity + p.Quantity, UnitPrice: product.Price}
		userItems[p.ProductID] = item
		write(w, 200, cartSnapshot(r.PathValue("userId"), userItems))
	})
	register(m, "DELETE /carts/{userId}/items/{productId}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		items := all[r.PathValue("userId")]
		if _, ok := items[r.PathValue("productId")]; !ok {
			fail(w, 404, "NOT_FOUND", "Позиция не найдена")
			return
		}
		delete(items, r.PathValue("productId"))
		w.WriteHeader(204)
	})
	register(m, "DELETE /carts/{userId}/items", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		delete(all, r.PathValue("userId"))
		mu.Unlock()
		w.WriteHeader(204)
	})
}

// ---- Delivery: quote calculation + validation against Orders ----
type Address struct {
	City   string `json:"city"`
	Street string `json:"street"`
	House  string `json:"house"`
}
type Delivery struct {
	DeliveryID string  `json:"deliveryId"`
	OrderID    string  `json:"orderId"`
	Address    Address `json:"address"`
	MethodID   string  `json:"methodId"`
	Status     string  `json:"status"`
}

func quote(method string, items []CartItem) (float64, int, error) {
	if len(items) == 0 {
		return 0, 0, fmt.Errorf("пустой список товаров")
	}
	for _, i := range items {
		if i.Quantity <= 0 {
			return 0, 0, fmt.Errorf("количество должно быть положительным")
		}
	}
	switch method {
	case "courier":
		return 350, 2, nil
	case "pickup":
		return 0, 3, nil
	default:
		return 0, 0, fmt.Errorf("неизвестный способ: %s", method)
	}
}
func delivery(m *http.ServeMux) {
	var mu sync.Mutex
	all := map[string]Delivery{}
	register(m, "GET /delivery/options", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("city") == "" {
			fail(w, 400, "CITY_REQUIRED", "Укажите city")
			return
		}
		write(w, 200, map[string]any{"options": []map[string]string{{"methodId": "courier", "name": "Курьер"}, {"methodId": "pickup", "name": "Самовывоз"}}})
	})
	register(m, "POST /delivery/quotes", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Address  Address    `json:"address"`
			MethodID string     `json:"methodId"`
			Items    []CartItem `json:"items"`
		}
		if !decode(w, r, &p) {
			return
		}
		if p.Address.City == "" {
			fail(w, 400, "CITY_REQUIRED", "Укажите город")
			return
		}
		fee, days, err := quote(p.MethodID, p.Items)
		if err != nil {
			fail(w, 422, "INVALID_DELIVERY", err.Error())
			return
		}
		write(w, 200, map[string]any{"deliveryFee": fee, "etaDays": days})
	})
	register(m, "POST /deliveries", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			OrderID  string  `json:"orderId"`
			Address  Address `json:"address"`
			MethodID string  `json:"methodId"`
		}
		if !decode(w, r, &p) {
			return
		}
		if p.OrderID == "" || p.Address.City == "" || (p.MethodID != "courier" && p.MethodID != "pickup") {
			fail(w, 400, "INVALID_DELIVERY", "Неверные orderId, address или methodId")
			return
		}
		// Third independent interservice interaction: Delivery checks Orders.
		_, err := request(r.Context(), "GET", env("ORDERS_URL", "http://localhost:8086")+"/orders/"+p.OrderID, nil, &map[string]any{})
		if err != nil {
			fail(w, 502, "ORDER_LOOKUP_FAILED", err.Error())
			return
		}
		d := Delivery{DeliveryID: id("d", 501), OrderID: p.OrderID, Address: p.Address, MethodID: p.MethodID, Status: "created"}
		mu.Lock()
		all[d.DeliveryID] = d
		mu.Unlock()
		write(w, 201, d)
	})
	register(m, "GET /deliveries/{deliveryId}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		d, ok := all[r.PathValue("deliveryId")]
		mu.Unlock()
		if !ok {
			fail(w, 404, "NOT_FOUND", "Доставка не найдена")
			return
		}
		write(w, 200, d)
	})
}

// ---- RabbitMQ async events ----
type Event struct {
	Type      string `json:"type"`
	OrderID   string `json:"orderId"`
	PaymentID string `json:"paymentId,omitempty"`
	At        string `json:"at"`
}

func publish(event Event) error {
	conn, err := amqp.Dial(env("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"))
	if err != nil {
		return err
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err = ch.ExchangeDeclare("marketplace.events", "fanout", true, false, false, false, nil); err != nil {
		return err
	}
	b, _ := json.Marshal(event)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return ch.PublishWithContext(ctx, "marketplace.events", "", false, false, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, Body: b})
}
func subscribe(queue string, callback func(Event) error) {
	// Do not expose the HTTP API until the first binding exists: otherwise
	// the initial payment-confirmed event could be lost by the fanout exchange.
	ready := make(chan struct{})
	go func() {
		connected := false
		for {
			conn, err := amqp.Dial(env("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"))
			if err != nil {
				log.Printf("rabbit connect: %v", err)
				time.Sleep(3 * time.Second)
				continue
			}
			ch, err := conn.Channel()
			if err != nil {
				_ = conn.Close()
				time.Sleep(3 * time.Second)
				continue
			}
			err = ch.ExchangeDeclare("marketplace.events", "fanout", true, false, false, false, nil)
			if err == nil {
				_, err = ch.QueueDeclare(queue, true, false, false, false, nil)
			}
			if err == nil {
				err = ch.QueueBind(queue, "", "marketplace.events", false, nil)
			}
			var msgs <-chan amqp.Delivery
			if err == nil {
				msgs, err = ch.Consume(queue, "", false, false, false, false, nil)
			}
			if err != nil {
				log.Printf("rabbit subscribe: %v", err)
				_ = ch.Close()
				_ = conn.Close()
				time.Sleep(3 * time.Second)
				continue
			}
			log.Printf("RabbitMQ consuming queue=%s", queue)
			if !connected {
				connected = true
				close(ready)
			}
			for d := range msgs {
				var e Event
				if err := json.Unmarshal(d.Body, &e); err != nil {
					log.Printf("invalid event: %v", err)
					_ = d.Ack(false)
					continue
				}
				if err := callback(e); err != nil {
					log.Printf("event processing error: %v", err)
					_ = d.Nack(false, true)
				} else {
					_ = d.Ack(false)
				}
			}
			_ = ch.Close()
			_ = conn.Close()
			time.Sleep(3 * time.Second)
		}
	}()
	<-ready
}

// ---- Payments: invoice REST API + event publisher / refund consumer ----
type Payment struct {
	PaymentID    string  `json:"paymentId"`
	OrderID      string  `json:"orderId"`
	Amount       float64 `json:"amount"`
	Currency     string  `json:"currency"`
	Status       string  `json:"status"`
	RefundStatus string  `json:"refundStatus,omitempty"`
}

func payments(m *http.ServeMux) {
	var mu sync.Mutex
	all := map[string]Payment{}
	subscribe("payments.order-cancelled", func(e Event) error {
		if e.Type != "order.cancelled" {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		for id, p := range all {
			if p.OrderID == e.OrderID && p.Status == "confirmed" {
				p.Status = "refunded"
				p.RefundStatus = "completed"
				all[id] = p
				log.Printf("Refund completed for %s", id)
			}
		}
		return nil
	})
	register(m, "POST /payments/invoices", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			OrderID  string  `json:"orderId"`
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
		}
		if !decode(w, r, &p) {
			return
		}
		if p.OrderID == "" || p.Amount <= 0 || p.Currency != "RUB" {
			fail(w, 400, "INVALID_INVOICE", "Укажите orderId, положительный amount и currency=RUB")
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, existing := range all {
			if existing.OrderID == p.OrderID {
				fail(w, 409, "DUPLICATE_INVOICE", "Счёт уже создан")
				return
			}
		}
		payment := Payment{PaymentID: id("pay", 501), OrderID: p.OrderID, Amount: p.Amount, Currency: p.Currency, Status: "pending"}
		all[payment.PaymentID] = payment
		write(w, 201, payment)
	})
	register(m, "GET /payments/{paymentId}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		p, ok := all[r.PathValue("paymentId")]
		mu.Unlock()
		if !ok {
			fail(w, 404, "NOT_FOUND", "Платёж не найден")
			return
		}
		write(w, 200, p)
	})
	register(m, "POST /payments/{paymentId}/confirm", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProviderTransactionID string `json:"providerTransactionId"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.ProviderTransactionID == "" {
			fail(w, 400, "INVALID_TRANSACTION", "Укажите providerTransactionId")
			return
		}
		mu.Lock()
		p, ok := all[r.PathValue("paymentId")]
		if !ok {
			mu.Unlock()
			fail(w, 404, "NOT_FOUND", "Платёж не найден")
			return
		}
		if p.Status != "pending" {
			mu.Unlock()
			fail(w, 409, "INVALID_STATUS", "Платёж уже обработан")
			return
		}
		p.Status = "confirmed"
		all[p.PaymentID] = p
		mu.Unlock()
		e := Event{Type: "payment.confirmed", OrderID: p.OrderID, PaymentID: p.PaymentID, At: time.Now().UTC().Format(time.RFC3339)}
		if err := publish(e); err != nil {
			mu.Lock()
			p.Status = "pending"
			all[p.PaymentID] = p
			mu.Unlock()
			fail(w, 502, "RABBITMQ_ERROR", err.Error())
			return
		}
		write(w, 200, p)
	})
	register(m, "POST /payments/{paymentId}/refund", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reason string `json:"reason"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.Reason == "" {
			fail(w, 400, "REASON_REQUIRED", "Укажите причину возврата")
			return
		}
		mu.Lock()
		defer mu.Unlock()
		p, ok := all[r.PathValue("paymentId")]
		if !ok {
			fail(w, 404, "NOT_FOUND", "Платёж не найден")
			return
		}
		if p.Status != "confirmed" {
			fail(w, 409, "INVALID_STATUS", "Возврат возможен только подтверждённого платежа")
			return
		}
		p.RefundStatus = "accepted"
		all[p.PaymentID] = p
		write(w, 202, map[string]string{"paymentId": p.PaymentID, "refundStatus": p.RefundStatus})
	})
}

// ---- Orders: sync calls to Carts, Delivery, Payments + RabbitMQ consumer ----
type Order struct {
	OrderID    string     `json:"orderId"`
	UserID     string     `json:"userId"`
	Items      []CartItem `json:"items"`
	Status     string     `json:"status"`
	Total      float64    `json:"total"`
	PaymentID  string     `json:"paymentId"`
	DeliveryID string     `json:"deliveryId,omitempty"`
}

func orders(m *http.ServeMux) {
	var mu sync.Mutex
	all := map[string]Order{}
	subscribe("orders.payment-confirmed", func(e Event) error {
		if e.Type != "payment.confirmed" {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		o, ok := all[e.OrderID]
		if !ok {
			return fmt.Errorf("order %s is not present yet", e.OrderID)
		}
		if o.Status == "awaiting_payment" {
			o.Status = "paid"
			all[e.OrderID] = o
			log.Printf("Order %s marked paid", e.OrderID)
		}
		return nil
	})
	register(m, "POST /orders", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			UserID           string  `json:"userId"`
			Address          Address `json:"address"`
			DeliveryMethodID string  `json:"deliveryMethodId"`
		}
		if !decode(w, r, &p) {
			return
		}
		if p.UserID == "" || p.Address.City == "" || p.DeliveryMethodID == "" {
			fail(w, 400, "INVALID_ORDER", "Укажите userId, address и deliveryMethodId")
			return
		}
		ctx := r.Context()
		var cart Cart
		_, err := request(ctx, "GET", env("CARTS_URL", "http://localhost:8083")+"/carts/"+p.UserID, nil, &cart)
		if err != nil {
			fail(w, 502, "CART_ERROR", err.Error())
			return
		}
		if len(cart.Items) == 0 {
			fail(w, 409, "EMPTY_CART", "Корзина пуста")
			return
		}
		var q struct {
			DeliveryFee float64 `json:"deliveryFee"`
			EtaDays     int     `json:"etaDays"`
		}
		_, err = request(ctx, "POST", env("DELIVERY_URL", "http://localhost:8084")+"/delivery/quotes", map[string]any{"address": p.Address, "methodId": p.DeliveryMethodID, "items": cart.Items}, &q)
		if err != nil {
			fail(w, 502, "DELIVERY_ERROR", err.Error())
			return
		}
		order := Order{OrderID: id("o", 501), UserID: p.UserID, Items: cart.Items, Status: "awaiting_payment", Total: cart.Total + q.DeliveryFee}
		var invoice Payment
		_, err = request(ctx, "POST", env("PAYMENTS_URL", "http://localhost:8085")+"/payments/invoices", map[string]any{"orderId": order.OrderID, "amount": order.Total, "currency": "RUB"}, &invoice)
		if err != nil {
			fail(w, 502, "PAYMENT_ERROR", err.Error())
			return
		}
		order.PaymentID = invoice.PaymentID
		mu.Lock()
		all[order.OrderID] = order
		mu.Unlock()
		var deliveryResult Delivery
		_, err = request(ctx, "POST", env("DELIVERY_URL", "http://localhost:8084")+"/deliveries", map[string]any{"orderId": order.OrderID, "address": p.Address, "methodId": p.DeliveryMethodID}, &deliveryResult)
		if err != nil {
			log.Printf("delivery creation failed for %s: %v", order.OrderID, err)
		} else {
			order.DeliveryID = deliveryResult.DeliveryID
			mu.Lock()
			all[order.OrderID] = order
			mu.Unlock()
		}
		_, err = request(ctx, "DELETE", env("CARTS_URL", "http://localhost:8083")+"/carts/"+p.UserID+"/items", nil, nil)
		if err != nil {
			log.Printf("cart clear failed: %v", err)
		}
		write(w, 201, order)
	})
	register(m, "GET /orders/{orderId}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		o, ok := all[r.PathValue("orderId")]
		mu.Unlock()
		if !ok {
			fail(w, 404, "NOT_FOUND", "Заказ не найден")
			return
		}
		write(w, 200, o)
	})
	register(m, "GET /orders", func(w http.ResponseWriter, r *http.Request) {
		userID := r.URL.Query().Get("userId")
		if userID == "" {
			fail(w, 400, "USER_REQUIRED", "Укажите userId")
			return
		}
		status := r.URL.Query().Get("status")
		mu.Lock()
		result := []Order{}
		for _, o := range all {
			if o.UserID == userID && (status == "" || o.Status == status) {
				result = append(result, o)
			}
		}
		mu.Unlock()
		write(w, 200, map[string]any{"orders": result, "total": len(result)})
	})
	register(m, "POST /orders/{orderId}/cancel", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reason string `json:"reason"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.Reason == "" {
			fail(w, 400, "REASON_REQUIRED", "Укажите причину отмены")
			return
		}
		mu.Lock()
		o, ok := all[r.PathValue("orderId")]
		if !ok {
			mu.Unlock()
			fail(w, 404, "NOT_FOUND", "Заказ не найден")
			return
		}
		if o.Status == "cancelled" {
			mu.Unlock()
			fail(w, 409, "INVALID_STATUS", "Заказ уже отменён")
			return
		}
		if o.Status == "paid" {
			e := Event{Type: "order.cancelled", OrderID: o.OrderID, PaymentID: o.PaymentID, At: time.Now().UTC().Format(time.RFC3339)}
			if err := publish(e); err != nil {
				mu.Unlock()
				fail(w, 502, "RABBITMQ_ERROR", err.Error())
				return
			}
		}
		o.Status = "cancelled"
		all[o.OrderID] = o
		mu.Unlock()
		write(w, 200, o)
	})
}
