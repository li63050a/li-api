package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
)

func init() {
	http.HandleFunc("/api/prices", PriceMatrixHandler)
	http.HandleFunc("/admin/prices", FixedPriceHandler)
}

func setBilling3CORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func PriceMatrixHandler(w http.ResponseWriter, r *http.Request) {
	setBilling3CORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session, ok := requireSession(r)
	if !ok || !model.IsRoot(session.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	type fixedPrice struct {
		Model string `json:"model"`
		Fixed int64  `json:"fixed"`
	}
	fixedPrices := []fixedPrice{}
	for modelName, v := range model.KVGetAll("fixedprice.") {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			fixedPrices = append(fixedPrices, fixedPrice{Model: modelName, Fixed: n})
		}
	}
	sort.Slice(fixedPrices, func(i, j int) bool { return fixedPrices[i].Model < fixedPrices[j].Model })

	settings := model.GetSetting()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"fixed_prices": fixedPrices,
		"vmodels":      model.GetVModels(),
		"ratios":       settings.ModelRatio,
	})
}

func FixedPriceHandler(w http.ResponseWriter, r *http.Request) {
	setBilling3CORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	session, ok := requireSession(r)
	if !ok || !model.IsRoot(session.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case "POST":
		var req struct {
			Model string `json:"model"`
			Fixed int64  `json:"fixed"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if req.Model == "" || req.Fixed < 0 {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if err := model.KVSet("fixedprice."+req.Model, strconv.FormatInt(req.Fixed, 10)); err != nil {
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "model": req.Model, "fixed": req.Fixed})
	case "DELETE":
		m := r.URL.Query().Get("model")
		if m == "" {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if err := model.KVDel("fixedprice." + m); err != nil {
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "model": m})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}