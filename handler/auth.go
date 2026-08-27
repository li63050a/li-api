package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"strings"
)

// LoginHandler POST /api/user/login {username,password} -> {token}
func LoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var cred struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cred); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if u, ok := model.VerifyUser(cred.Username, cred.Password); ok {
		tok := model.CreateSession(u.Username)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":    tok,
			"username": u.Username,
			"role":     u.Role,
		})
		return
	}
	http.Error(w, "Invalid username or password", http.StatusUnauthorized)
}

// LogoutHandler POST /api/user/logout
func LogoutHandler(w http.ResponseWriter, r *http.Request) {
	model.DeleteSession(bearerToken(r))
	w.WriteHeader(http.StatusNoContent)
}

// RegisterHandler POST /api/user/register {username,password} -> {token}
func RegisterHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !model.GetSetting().OpenRegister {
		http.Error(w, "Registration is closed", http.StatusForbidden)
		return
	}
	var cred struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cred); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if len(cred.Username) < 3 || len(cred.Password) < 6 {
		http.Error(w, "用户名至少3位，密码至少6位", http.StatusBadRequest)
		return
	}
	if err := model.InsertUser(cred.Username, cred.Password); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tok := model.CreateSession(cred.Username)
	json.NewEncoder(w).Encode(map[string]interface{}{"token": tok, "username": cred.Username, "role": "user"})
}

// SelfHandler GET /api/user/self
func SelfHandler(w http.ResponseWriter, r *http.Request) {
	s, ok := model.GetSession(bearerToken(r))
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"username": s.Username,
		"role":     map[bool]string{true: "root", false: "user"}[model.IsRoot(s.Username)],
	})
}

// requireSession 校验管理会话，失败返回 false 并写 401
func requireSession(r *http.Request) (*model.Session, bool) {
	return model.GetSession(bearerToken(r))
}

// bearerToken 从 Authorization: Bearer 或 X-Admin-Token 取令牌
func bearerToken(r *http.Request) string {
	ah := r.Header.Get("Authorization")
	if strings.HasPrefix(ah, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(ah, "Bearer "))
	}
	return r.Header.Get("X-Admin-Token")
}
