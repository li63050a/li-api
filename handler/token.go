package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"strings"
)

// TokenHandler 处理 /admin/tokens 的 CRUD
func TokenHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 管理口令校验
	if token := adminToken(); token != "" {
		if r.URL.Query().Get("token") != token && r.Header.Get("X-Admin-Token") != token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	// 解析 /admin/tokens/{key}
	key := strings.TrimPrefix(r.URL.Path, "/admin/tokens")
	key = strings.Trim(key, "/")

	switch r.Method {
	case "GET":
		list, err := model.GetAllTokens()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(list)

	case "POST":
		var t model.Token
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if _, err := model.InsertToken(&t); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(t)

	case "PUT":
		if key == "" {
			http.Error(w, "Missing key", http.StatusBadRequest)
			return
		}
		var t model.Token
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := model.UpdateToken(key, &t); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(t)

	case "DELETE":
		if key == "" {
			http.Error(w, "Missing key", http.StatusBadRequest)
			return
		}
		if err := model.DeleteToken(key); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
