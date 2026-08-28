package handler

import (
	"api-gateway/model"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/api/wallet", WalletHandler)
	http.HandleFunc("/api/pay/order", CreateOrderHandler)
	http.HandleFunc("/api/pay/callback/", PayCallbackHandler)
	http.HandleFunc("/api/setting/pay", PaySettingsHandler)
}

type PayOrder struct {
	ID        string `json:"id"`
	User      string `json:"user"`
	Amount    int64  `json:"amount"`
	Gateway   string `json:"gateway"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	PaidAt    string `json:"paid_at"`
}

func payCORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

func kvFlag(k string) bool {
	v, _ := model.KVGet(k)
	return v == "1"
}

func kvNonEmpty(k string) bool {
	v, ok := model.KVGet(k)
	return ok && strings.TrimSpace(v) != ""
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func newPayID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func savePayOrder(o PayOrder) {
	if o.CreatedAt == "" {
		o.CreatedAt = time.Now().Format(time.RFC3339)
	}
	if b, err := json.Marshal(o); err == nil {
		_ = model.KVSet("order."+o.ID, string(b))
	}
}

func loadPayOrder(id string) (PayOrder, bool) {
	raw, ok := model.KVGet("order." + id)
	if !ok || raw == "" {
		return PayOrder{}, false
	}
	var o PayOrder
	if json.Unmarshal([]byte(raw), &o) != nil {
		return PayOrder{}, false
	}
	return o, true
}

func creditPaidOrder(o PayOrder) bool {
	if o.Status != "paid" {
		return false
	}
	if credited, _ := model.KVGet("pay.credit." + o.ID); credited == "1" {
		return true
	}
	model.AddUserQuota(o.User, o.Amount)
	var balance int64
	if u, found := model.GetUserByUsername(o.User); found {
		balance = u.Quota
	}
	model.AppendBilling(model.BillingEntry{
		User:    o.User,
		Type:    "recharge",
		Amount:  o.Amount,
		Balance: balance,
		Remark:  "mock pay " + o.Gateway,
	})
	_ = model.KVSet("pay.credit."+o.ID, "1")
	return true
}

func WalletHandler(w http.ResponseWriter, r *http.Request) {
	if payCORS(w, r) {
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	var quota, used int64
	if u, found := model.GetUserByUsername(s.Username); found {
		quota, used = u.Quota, u.Used
	}
	var orders []PayOrder
	for _, raw := range model.KVGetAll("order.") {
		var o PayOrder
		if json.Unmarshal([]byte(raw), &o) == nil && o.User == s.Username && o.ID != "" {
			orders = append(orders, o)
		}
	}
	sort.Slice(orders, func(i, j int) bool {
		return orders[i].CreatedAt > orders[j].CreatedAt
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"balance": quota - used,
		"quota":   quota,
		"used":    used,
		"orders":  orders,
	})
}

func CreateOrderHandler(w http.ResponseWriter, r *http.Request) {
	if payCORS(w, r) {
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Amount  int64  `json:"amount"`
		Gateway string `json:"gateway"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if req.Amount <= 0 {
		http.Error(w, "amount must be positive", http.StatusBadRequest)
		return
	}
	if req.Gateway == "" {
		req.Gateway = "mock"
	}
	o := PayOrder{
		ID:        newPayID(),
		User:      s.Username,
		Amount:    req.Amount,
		Gateway:   req.Gateway,
		Status:    "pending",
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	if req.Gateway == "mock" && kvFlag("pay.mock_auto") {
		o.Status = "paid"
		o.PaidAt = time.Now().Format(time.RFC3339)
	}
	savePayOrder(o)
	creditPaidOrder(o)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"order":   o,
		"pay_url": "/api/pay/callback/mock/" + o.ID,
	})
}

func PayCallbackHandler(w http.ResponseWriter, r *http.Request) {
	if payCORS(w, r) {
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/api/pay/callback/")
	parts := strings.Split(suffix, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	gateway, id := parts[0], parts[1]
	if gateway != "mock" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "not_configured",
			"gateway": gateway,
		})
		return
	}
	o, found := loadPayOrder(id)
	if !found {
		http.Error(w, "Order not found", http.StatusNotFound)
		return
	}
	if o.Status == "pending" {
		o.Status = "paid"
		o.PaidAt = time.Now().Format(time.RFC3339)
		savePayOrder(o)
		creditPaidOrder(o)
	}
	http.Redirect(w, r, "/#wallet", http.StatusFound)
}

func PaySettingsHandler(w http.ResponseWriter, r *http.Request) {
	if payCORS(w, r) {
		return
	}
	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	switch r.Method {
	case "GET":
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"mock_enabled":       kvFlag("pay.mock_enabled"),
			"mock_auto":          kvFlag("pay.mock_auto"),
			"stripe_configured":  kvNonEmpty("pay.stripe.secret") && kvNonEmpty("pay.stripe.public"),
			"epay_configured":    kvNonEmpty("pay.epay.pid") && kvNonEmpty("pay.epay.key"),
			"pancake_configured": kvNonEmpty("pay.pancake.token"),
		})
	case "POST":
		var req struct {
			MockEnabled     *bool  `json:"mock_enabled"`
			MockAuto        *bool  `json:"mock_auto"`
			StripeSecret    string `json:"stripe_secret"`
			StripePublicKey string `json:"stripe_public_key"`
			EpayPID         string `json:"epay_pid"`
			EpayKey         string `json:"epay_key"`
			PancakeToken    string `json:"pancake_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if req.MockEnabled != nil {
			_ = model.KVSet("pay.mock_enabled", boolStr(*req.MockEnabled))
		}
		if req.MockAuto != nil {
			_ = model.KVSet("pay.mock_auto", boolStr(*req.MockAuto))
		}
		if req.StripeSecret != "" {
			_ = model.KVSet("pay.stripe.secret", req.StripeSecret)
		}
		if req.StripePublicKey != "" {
			_ = model.KVSet("pay.stripe.public", req.StripePublicKey)
		}
		if req.EpayPID != "" {
			_ = model.KVSet("pay.epay.pid", req.EpayPID)
		}
		if req.EpayKey != "" {
			_ = model.KVSet("pay.epay.key", req.EpayKey)
		}
		if req.PancakeToken != "" {
			_ = model.KVSet("pay.pancake.token", req.PancakeToken)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
