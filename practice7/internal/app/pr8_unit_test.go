package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These tests exercise actual application functions without PostgreSQL,
// RabbitMQ, network calls or other external dependencies.
func TestUnitDeliveryQuoteTable(t *testing.T) {
	cases := []struct {
		name      string
		method    string
		items     []CartItem
		wantFee   float64
		wantDays  int
		wantError bool
	}{
		{"courier", "courier", []CartItem{{ProductID: "p-7", Quantity: 2, UnitPrice: 1500}}, 350, 2, false},
		{"pickup", "pickup", []CartItem{{ProductID: "p-8", Quantity: 1, UnitPrice: 800}}, 0, 3, false},
		{"unknown method", "drone", []CartItem{{Quantity: 1}}, 0, 0, true},
		{"empty cart", "courier", nil, 0, 0, true},
		{"zero quantity", "courier", []CartItem{{Quantity: 0}}, 0, 0, true},
		{"negative quantity", "courier", []CartItem{{Quantity: -2}}, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fee, days, err := quote(tc.method, tc.items)
			if (err != nil) != tc.wantError {
				t.Fatalf("quote error = %v, expected error: %v", err, tc.wantError)
			}
			if !tc.wantError && (fee != tc.wantFee || days != tc.wantDays) {
				t.Fatalf("quote() = (%v, %d), want (%v, %d)", fee, days, tc.wantFee, tc.wantDays)
			}
		})
	}
}

func TestUnitCartSnapshotTotal(t *testing.T) {
	got := cartSnapshot("user-test", map[string]CartItem{
		"p-7": {ProductID: "p-7", Quantity: 2, UnitPrice: 1500},
		"p-8": {ProductID: "p-8", Quantity: 3, UnitPrice: 800},
	})
	if got.UserID != "user-test" || len(got.Items) != 2 || got.Total != 5400 {
		t.Fatalf("cartSnapshot() = %+v, want total=5400 with two items", got)
	}
}

func TestUnitCartSnapshotEmpty(t *testing.T) {
	got := cartSnapshot("user-empty", nil)
	if got.Total != 0 || len(got.Items) != 0 || got.UserID != "user-empty" {
		t.Fatalf("empty cart = %+v", got)
	}
}

func TestUnitDecodeValidation(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		wantOK bool
	}{
		{"valid JSON", `{"name":"Student"}`, true},
		{"unknown field", `{"unexpected":123}`, false},
		{"two JSON objects", `{"name":"A"}{"name":"B"}`, false},
		{"malformed JSON", `{"name":`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(tc.body))
			var parsed struct {
				Name string `json:"name"`
			}
			ok := decode(w, r, &parsed)
			if ok != tc.wantOK {
				t.Fatalf("decode(%q) = %v, wanted %v; response=%s", tc.body, ok, tc.wantOK, w.Body.String())
			}
			if !tc.wantOK && w.Code != http.StatusBadRequest {
				t.Fatalf("expected HTTP 400, got %d", w.Code)
			}
		})
	}
}
