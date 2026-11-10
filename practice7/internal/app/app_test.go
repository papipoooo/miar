package app

import "testing"

func TestQuoteCourier(t *testing.T) {
	fee, days, err := quote("courier", []CartItem{{ProductID: "p-7", Quantity: 2, UnitPrice: 1500}})
	if err != nil || fee != 350 || days != 2 {
		t.Fatalf("courier quote = %v, %v, %v", fee, days, err)
	}
}
func TestQuotePickup(t *testing.T) {
	fee, days, err := quote("pickup", []CartItem{{ProductID: "p-7", Quantity: 1, UnitPrice: 1500}})
	if err != nil || fee != 0 || days != 3 {
		t.Fatalf("pickup quote = %v, %v, %v", fee, days, err)
	}
}
func TestQuoteRejectsInvalid(t *testing.T) {
	cases := []struct {
		name, method string
		items        []CartItem
	}{
		{"unknown method", "rocket", []CartItem{{Quantity: 1}}},
		{"empty cart", "courier", nil},
		{"zero quantity", "courier", []CartItem{{Quantity: 0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := quote(tc.method, tc.items)
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
func TestCartSnapshot(t *testing.T) {
	cart := cartSnapshot("u-101", map[string]CartItem{
		"p-7": {ProductID: "p-7", Quantity: 2, UnitPrice: 1500},
		"p-8": {ProductID: "p-8", Quantity: 1, UnitPrice: 800},
	})
	if cart.UserID != "u-101" || len(cart.Items) != 2 || cart.Total != 3800 {
		t.Fatalf("unexpected cart: %+v", cart)
	}
}
