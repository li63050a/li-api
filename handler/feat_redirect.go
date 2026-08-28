package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"strings"
)

// RedirectHandler 管理全局模型重定向（KV redirect.<name>：公开模型名 → 真实模型名）
func RedirectHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	s, ok := requireSession(r)
	if !ok || !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	switch r.Method {
	case "GET":
		json.NewEncoder(w).Encode(map[string]interface{}{
			"redirects":   model.KVGetAll("redirect."),
			"regex_rules": model.KVGetAll("redirect_re."),
		})
	case "POST":
		var body struct {
			Name  string `json:"name"`
			Model string `json:"model"`
			Regex bool   `json:"regex"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Model) == "" {
			http.Error(w, "name/model 必填", http.StatusBadRequest)
			return
		}
		key := "redirect." + body.Name
		if body.Regex {
			key = "redirect_re." + body.Name
		}
		if err := model.KVSet(key, body.Model); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"success": "true"})
	case "DELETE":
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name 必填", http.StatusBadRequest)
			return
		}
		_ = model.KVDel("redirect." + name)
		_ = model.KVDel("redirect_re." + name)
		json.NewEncoder(w).Encode(map[string]string{"success": "true"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}