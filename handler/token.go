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

	// 管理会话校验
	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	isRoot := model.IsRoot(s.Username)

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
		if !isRoot {
			own := list[:0]
			for _, t := range list {
				if t.Owner == s.Username {
					own = append(own, t)
				}
			}
			list = own
		}
		json.NewEncoder(w).Encode(list)

	case "POST":
		var t model.Token
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		t.Owner = s.Username
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
		if !isRoot {
			if cur, err := model.GetToken(key); err != nil || cur.Owner != s.Username {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
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
		if !isRoot {
			if cur, err := model.GetToken(key); err != nil || cur.Owner != s.Username {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
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
