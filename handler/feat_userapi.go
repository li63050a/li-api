package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"api-gateway/model"
)

func init() {
	http.HandleFunc("/api/user/tokens", userTokensHandler)
	http.HandleFunc("/api/user/usage", userUsageHandler)
	http.HandleFunc("/api/user/sessions", userSessionsHandler)
	http.HandleFunc("/api/user/2fa/recovery", user2FARecoveryHandler)
	http.HandleFunc("/api/user/2fa/codes", user2FACodesHandler)
}

// userAPICORS 处理 CORS 预检；OPTIONS 请求直接返回
func userAPICORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

// maskTokenKey 把令牌脱敏成 xxx****xxxx 形式展示
func maskTokenKey(key string) string {
	if len(key) <= 10 {
		return key[:1] + "***"
	}
	return key[:6] + "****" + key[len(key)-4:]
}

// userTokensHandler GET/POST/DELETE /api/user/tokens 用户自管理自己的令牌
func userTokensHandler(w http.ResponseWriter, r *http.Request) {
	if userAPICORS(w, r) {
		return
	}
	s, ok := requireSession(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch r.Method {
	case "GET":
		all, err := model.GetAllTokens()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "load tokens failed")
			return
		}
		out := []map[string]interface{}{}
		for _, t := range all {
			if t.Owner != s.Username {
				continue
			}
			out = append(out, map[string]interface{}{
				"key":        t.Key,
				"key_masked": maskTokenKey(t.Key),
				"name":       t.Name,
				"group":      t.Group,
				"quota":      t.Quota,
				"used":       t.Used,
				"unlimited":  t.Unlimited,
				"status":     t.Status,
				"models":     t.Models,
				"scope":      t.Scope,
				"created_at": t.CreatedAt,
				"expired_at": t.ExpiredAt,
			})
		}
		writeJSON(w, out)
	case "POST":
		var body struct {
			Name   string `json:"name"`
			Quota  *int64 `json:"quota"`
			Models string `json:"models"`
			Group  string `json:"group"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			writeErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		quota := int64(100000)
		if body.Quota != nil {
			quota = *body.Quota
		}
		group := body.Group
		if group == "" {
			group = model.GetUserGroup(s.Username)
		}
		t := &model.Token{
			Name:   body.Name,
			Owner:  s.Username,
			Group:  group,
			Quota:  quota,
			Models: body.Models,
			Scope:  "write",
		}
		if _, err := model.InsertToken(t); err != nil {
			writeErr(w, http.StatusInternalServerError, "create token failed")
			return
		}
		_ = model.AppendAudit(s.Username, "user_token_create", "创建令牌 "+t.Name)
		writeJSON(w, map[string]interface{}{
			"token": map[string]interface{}{
				"key":        t.Key,
				"key_masked": maskTokenKey(t.Key),
				"name":       t.Name,
				"group":      t.Group,
				"quota":      t.Quota,
				"models":     t.Models,
				"scope":      t.Scope,
			},
		})
	case "DELETE":
		key := r.URL.Query().Get("key")
		if key == "" {
			writeErr(w, http.StatusBadRequest, "key required")
			return
		}
		t, err := model.GetToken(key)
		if err != nil {
			writeErr(w, http.StatusNotFound, "token not found")
			return
		}
		if t.Owner != s.Username && !model.IsRoot(s.Username) {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		if err := model.DeleteToken(key); err != nil {
			writeErr(w, http.StatusInternalServerError, "delete token failed")
			return
		}
		_ = model.AppendAudit(s.Username, "user_token_delete", "删除令牌 "+t.Name)
		writeJSON(w, map[string]string{"success": "true"})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// userUsageHandler GET /api/user/usage 当前用户用量概览
func userUsageHandler(w http.ResponseWriter, r *http.Request) {
	if userAPICORS(w, r) {
		return
	}
	if r.Method != "GET" {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s, ok := requireSession(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	u, ok := model.GetUserByUsername(s.Username)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	remaining := int64(-1)
	if u.Quota >= 0 {
		remaining = u.Quota - u.Used
	}
	all, _ := model.GetAllTokens()
	tokenCount := 0
	for _, t := range all {
		if t.Owner == s.Username {
			tokenCount++
		}
	}
	writeJSON(w, map[string]interface{}{
		"username":    u.Username,
		"role":        u.Role,
		"email":       u.Email,
		"avatar":      u.Avatar,
		"group":       model.GetUserGroup(s.Username),
		"quota":       u.Quota,
		"used":        u.Used,
		"remaining":   remaining,
		"token_count": tokenCount,
	})
}

// userSessionsHandler GET/DELETE /api/user/sessions 会话列表与注销
func userSessionsHandler(w http.ResponseWriter, r *http.Request) {
	if userAPICORS(w, r) {
		return
	}
	s, ok := requireSession(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch r.Method {
	case "GET":
		writeJSON(w, map[string]interface{}{"sessions": model.ListSessions(s.Username)})
	case "DELETE":
		token := r.URL.Query().Get("token")
		if token == "" {
			writeErr(w, http.StatusBadRequest, "token required")
			return
		}
		model.DeleteSession(token)
		_ = model.AppendAudit(s.Username, "user_session_delete", "注销会话")
		writeJSON(w, map[string]string{"success": "true"})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// user2FARecoveryHandler POST /api/user/2fa/recovery 用恢复码登录并颁发会话
func user2FARecoveryHandler(w http.ResponseWriter, r *http.Request) {
	if userAPICORS(w, r) {
		return
	}
	if r.Method != "POST" {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req twoFARequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" || req.Code == "" {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	u, found := model.GetUserByUsername(req.Username)
	if !found || !verifyRecoveryCode(req.Username, req.Code) {
		writeErr(w, http.StatusForbidden, "invalid recovery code")
		return
	}
	tok := model.CreateSession(req.Username)
	_ = model.AppendAudit(req.Username, "user_2fa_recovery", "使用恢复码登录")
	writeJSON(w, map[string]interface{}{
		"token":    tok,
		"username": req.Username,
		"role":     u.Role,
	})
}

// user2FACodesHandler GET /api/user/2fa/codes 当前用户剩余未用恢复码
func user2FACodesHandler(w http.ResponseWriter, r *http.Request) {
	if userAPICORS(w, r) {
		return
	}
	if r.Method != "GET" {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s, ok := requireSession(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	stored, ok := model.GetUserRecovery(s.Username)
	list := []string{}
	if ok {
		for _, c := range strings.Split(stored, ",") {
			if c = strings.TrimSpace(c); c != "" {
				list = append(list, c)
			}
		}
	}
	writeJSON(w, map[string]interface{}{"codes": list})
}
