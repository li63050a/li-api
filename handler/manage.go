package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func parseUID(path, prefix string) (int, bool) {
	n := strings.TrimPrefix(path, prefix)
	n = strings.Trim(n, "/")
	id, err := strconv.Atoi(n)
	if err != nil {
		return 0, false
	}
	return id, true
}

// UsersHandler /admin/users 用户管理（仅 root）
func UsersHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
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
	path := strings.TrimPrefix(r.URL.Path, "/admin/users")

	if strings.HasSuffix(path, "/2fa-reset") {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id, ok := parseUID(strings.TrimSuffix(path, "/2fa-reset"), "/")
		if !ok {
			http.Error(w, "Invalid ID", http.StatusBadRequest)
			return
		}
		var username string
		for _, u := range model.GetAllUsers() {
			if u.ID == id {
				username = u.Username
				break
			}
		}
		if username == "" {
			http.Error(w, "user not found", http.StatusBadRequest)
			return
		}
		if err := model.SetUser2FA(username, "", false); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"success": "true"})
		return
	}

	switch r.Method {
	case "GET":
		json.NewEncoder(w).Encode(model.GetAllUsers())
	case "POST":
		var u struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Role     string `json:"role"`
			Quota    int64  `json:"quota"`
		}
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if u.Username == "" || u.Password == "" {
			http.Error(w, "用户名和密码必填", http.StatusBadRequest)
			return
		}
		role := u.Role
		if role == "" {
			role = "user"
		}
		if err := model.CreateUser(u.Username, u.Password, role, u.Quota); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"success": "true"})
	case "PUT":
		id, ok := parseUID(path, "/")
		if !ok {
			http.Error(w, "Invalid ID", http.StatusBadRequest)
			return
		}
		var body struct {
			Username string `json:"username"`
			Role     string `json:"role"`
			Status   int    `json:"status"`
			Quota    int64  `json:"quota"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		patch := model.User{
			Username: body.Username,
			Role:     body.Role,
			Status:   body.Status,
			Quota:    body.Quota,
		}
		if body.Password != "" {
			patch.PasswordHash = model.HashPassword(body.Password)
		}
		if err := model.UpdateUser(id, patch); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"success": "true"})
	case "DELETE":
		id, ok := parseUID(path, "/")
		if !ok {
			http.Error(w, "Invalid ID", http.StatusBadRequest)
			return
		}
		if err := model.DeleteUser(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// RedemptionHandler /admin/redemptions 充值码管理（仅 root）
func RedemptionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
		json.NewEncoder(w).Encode(model.GetAllRedemptions())
	case "POST":
		var req struct {
			Quota int64 `json:"quota"`
			Count int   `json:"count"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		codes, err := model.CreateRedemptions(req.Quota, req.Count, s.Username)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "codes": codes})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// ModelPresetsHandler GET /api/model_presets 返回内置模型倍率预设
func ModelPresetsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"model_ratio":      model.DefaultModelRatio(),
		"completion_ratio": model.DefaultCompletionRatio(),
	})
}

// RedeemHandler POST /api/redemption/redeem 用户兑换充值码
func RedeemHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := model.Redeem(req.Code, s.Username); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "兑换成功，额度已增加"})
}
