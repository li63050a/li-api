package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 登录失败锁定：同一用户名 5 次失败后锁定 15 分钟
const (
	authFailThreshold = 5
	authLockoutDur    = 15 * time.Minute
)

type authFailState struct {
	count       int
	lockedUntil time.Time
}

var (
	authFailMu sync.Mutex
	authFails  = map[string]authFailState{}
)

// authLoginLocked 返回该用户名当前是否处于锁定期
func authLoginLocked(username string) bool {
	authFailMu.Lock()
	defer authFailMu.Unlock()
	st, ok := authFails[username]
	return ok && time.Now().Before(st.lockedUntil)
}

// authLoginFail 记录一次登录失败；达到阈值则锁定 15 分钟
func authLoginFail(username string) {
	authFailMu.Lock()
	defer authFailMu.Unlock()
	st := authFails[username]
	st.count++
	if st.count >= authFailThreshold {
		st.lockedUntil = time.Now().Add(authLockoutDur)
	}
	authFails[username] = st
}

// authLoginReset 登录成功后清空失败记录
func authLoginReset(username string) {
	authFailMu.Lock()
	defer authFailMu.Unlock()
	delete(authFails, username)
}

// authJSONError 写 JSON 错误响应
func authJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

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
	if !VerifyTurnstile(r) {
		authJSONError(w, http.StatusBadRequest, "captcha required")
		return
	}
	if authLoginLocked(cred.Username) {
		authJSONError(w, http.StatusTooManyRequests, "too many failed attempts, try later")
		return
	}
	if u, ok := model.VerifyUser(cred.Username, cred.Password); ok {
		authLoginReset(cred.Username)
		recordLoginIP(cred.Username, clientIP(r))
		// 已启用 2FA 的用户：先不签发会话，返回 need_2fa，待 TOTP 校验后发会话
		if u.TwoFAEnabled == 1 {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"need_2fa": true,
				"username": u.Username,
			})
			return
		}
		tok := model.CreateSession(u.Username)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":    tok,
			"username": u.Username,
			"role":     u.Role,
		})
		return
	}
	authLoginFail(cred.Username)
	http.Error(w, "Invalid username or password", http.StatusUnauthorized)
}

// LogoutHandler POST /api/user/logout
func LogoutHandler(w http.ResponseWriter, r *http.Request) {
	model.DeleteSession(bearerToken(r))
	w.WriteHeader(http.StatusNoContent)
}

// RegisterHandler POST /api/user/register {username,password,invite?} -> {token}
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
		Invite   string `json:"invite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cred); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if !VerifyTurnstile(r) {
		authJSONError(w, http.StatusBadRequest, "captcha required")
		return
	}
	if len(cred.Username) < 3 || len(cred.Password) < 6 {
		http.Error(w, "用户名至少3位，密码至少6位", http.StatusBadRequest)
		return
	}
	// 仿 new-api：系统无任何用户时，首个注册用户自动成为 root 超级管理员
	role := "user"
	quota := int64(0)
	if model.CountUsers() == 0 {
		role = "root"
		quota = -1 // root 不限额度
	}
	if err := model.CreateUser(cred.Username, cred.Password, role, quota); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// 邀请码：兑换成功则给新用户发放额度，并给邀请人 10% 奖励
	if cred.Invite != "" {
		inv, err := model.RedeemInvite(cred.Invite, cred.Username)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		model.AddUserQuota(cred.Username, inv.Quota)
		_ = model.SetUserInvitedBy(cred.Username, inv.Inviter)
		model.AppendBilling(model.BillingEntry{
			User:    cred.Username,
			Type:    "invite",
			Amount:  inv.Quota,
			Balance: authUserQuota(cred.Username),
			Remark:  "invite:" + cred.Invite,
		})
		bonus := inv.Quota / 10
		if bonus > 0 {
			model.AddUserQuota(inv.Inviter, bonus)
			model.AppendBilling(model.BillingEntry{
				User:    inv.Inviter,
				Type:    "invite_bonus",
				Amount:  bonus,
				Balance: authUserQuota(inv.Inviter),
				Remark:  "invite bonus:" + cred.Invite,
			})
		}
	}
	tok := model.CreateSession(cred.Username)
	json.NewEncoder(w).Encode(map[string]interface{}{"token": tok, "username": cred.Username, "role": role})
}

// authUserQuota 返回用户当前额度
func authUserQuota(name string) int64 {
	if u, ok := model.GetUserByUsername(name); ok {
		return u.Quota
	}
	return 0
}

// ProfileHandler GET/PUT /api/user/profile
func ProfileHandler(w http.ResponseWriter, r *http.Request) {
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
		u, ok := model.GetUserByUsername(s.Username)
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"username": u.Username,
			"email":    u.Email,
			"avatar":   u.Avatar,
			"group":    model.GetUserGroup(s.Username),
		})
	case "PUT":
		var body struct {
			Avatar string `json:"avatar"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := model.SetUserAvatar(s.Username, body.Avatar); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"success": "true"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
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

func init() {
	http.HandleFunc("/api/user/iplog", IpLogHandler)
}

// recordLoginIP 登录成功后把本次登录 IP 追加到 KV "iplog.<username>"（最多保留最近 50 条）
func recordLoginIP(username, ip string) {
	key := "iplog." + username
	var entries []map[string]interface{}
	if raw, ok := model.KVGet(key); ok && raw != "" {
		_ = json.Unmarshal([]byte(raw), &entries)
	}
	entries = append(entries, map[string]interface{}{
		"time": time.Now().Format(time.RFC3339),
		"ip":   ip,
	})
	if len(entries) > 50 {
		entries = entries[len(entries)-50:]
	}
	if b, err := json.Marshal(entries); err == nil {
		_ = model.KVSet(key, string(b))
	}
}

// IpLogHandler GET /api/user/iplog 返回当前用户的登录 IP 记录；root 可通过 ?user= 查询指定用户
func IpLogHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	target := s.Username
	if u := r.URL.Query().Get("user"); u != "" {
		if !model.IsRoot(s.Username) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		target = u
	}
	var entries []map[string]interface{}
	if raw, ok := model.KVGet("iplog." + target); ok && raw != "" {
		_ = json.Unmarshal([]byte(raw), &entries)
	}
	if entries == nil {
		entries = []map[string]interface{}{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"iplog": entries})
}
