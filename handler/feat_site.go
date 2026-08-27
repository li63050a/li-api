package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
)

func init() {
	http.HandleFunc("/api/feat/site", SiteHandler)
}

// SiteHandler GET/PUT /api/feat/site
// GET: 任意登录用户可读；PUT: 仅 root 可写
func SiteHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case "GET":
		json.NewEncoder(w).Encode(model.GetSite())
	case "PUT":
		if !model.IsRoot(s.Username) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		var cfg model.SiteConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := model.SaveSite(cfg); err != nil {
			http.Error(w, "Save failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
