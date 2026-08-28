package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"api-gateway/model"
)

const totpPeriod = 30

func init() {
	http.HandleFunc("/api/user/2fa/verify", TwoFAVerifyHandler)
	http.HandleFunc("/api/user/2fa/enable", TwoFAHandler)
	http.HandleFunc("/api/user/2fa/confirm", TwoFAHandler)
	http.HandleFunc("/api/user/2fa/disable", TwoFAHandler)
}

func setTwoFACORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

type twoFARequest struct {
	Username string `json:"username"`
	Code     string `json:"code"`
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{"error": msg})
}

// generateTOTPSecret 生成 20 字节随机密钥，base32 编码无填充
func generateTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// totpCode 计算指定时间窗口的 HMAC-SHA1 TOTP 6 位动态码（RFC 6238）
func totpCode(secret string, t time.Time) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return ""
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(t.Unix()/totpPeriod))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%06d", code%1000000)
}

// verifyTOTP 校验 6 位动态码，容忍 ±1 个时间窗口，允许空格分隔
func verifyTOTP(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(strings.ReplaceAll(code, " ", ""))
	if len(code) != 6 {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	for i := -1; i <= 1; i++ {
		expected := totpCode(secret, now.Add(time.Duration(i)*totpPeriod*time.Second))
		if expected != "" && hmac.Equal([]byte(expected), []byte(code)) {
			return true
		}
	}
	return false
}

// authorizedUser 校验管理会话：会话用户名须与目标一致，或为 root
func authorizedUser(r *http.Request, username string) (*model.Session, bool) {
	s, ok := requireSession(r)
	if !ok {
		return nil, false
	}
	if s.Username != username && !model.IsRoot(s.Username) {
		return nil, false
	}
	return s, true
}

// TwoFAHandler POST /api/user/2fa/{enable,confirm,disable} 管理 TOTP 双因素认证
func TwoFAHandler(w http.ResponseWriter, r *http.Request) {
	if setTwoFACORS(w, r) {
		return
	}
	if r.Method != "POST" {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req twoFARequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	s, ok := authorizedUser(r, req.Username)
	if !ok {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/enable"):
		twoFAEnable(w, s, req)
	case strings.HasSuffix(r.URL.Path, "/confirm"):
		twoFAConfirm(w, s, req)
	case strings.HasSuffix(r.URL.Path, "/disable"):
		twoFADisable(w, s, req)
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

// twoFAEnable 生成新密钥并暂存（未启用），返回 base32 密钥与 otpauth 链接
func twoFAEnable(w http.ResponseWriter, s *model.Session, req twoFARequest) {
	if _, enabled := model.GetUser2FA(req.Username); enabled {
		writeErr(w, http.StatusBadRequest, "2fa already enabled")
		return
	}
	secret, err := generateTOTPSecret()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "generate secret failed")
		return
	}
	if err := model.SetUser2FA(req.Username, secret, false); err != nil {
		writeErr(w, http.StatusInternalServerError, "store failed")
		return
	}
	_ = model.AppendAudit(s.Username, "2fa_enable", "开启双因素认证")
	writeJSON(w, map[string]interface{}{
		"secret":  secret,
		"otpauth": fmt.Sprintf("otpauth://totp/Gateway:%s?secret=%s&issuer=Gateway", url.PathEscape(req.Username), secret),
		"success": true,
	})
}

// generateRecoveryCodes 生成 n 个 8 位十六进制恢复码
func generateRecoveryCodes(n int) ([]string, error) {
	codes := make([]string, n)
	for i := range codes {
		b := make([]byte, 4)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		codes[i] = hex.EncodeToString(b)
	}
	return codes, nil
}

// verifyRecoveryCode 校验并消费一个恢复码：匹配则从用户存储列表中移除该码
func verifyRecoveryCode(username, code string) bool {
	stored, ok := model.GetUserRecovery(username)
	if !ok || stored == "" {
		return false
	}
	codes := strings.Split(stored, ",")
	idx := -1
	for i, c := range codes {
		if strings.TrimSpace(c) == strings.TrimSpace(code) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	rest := append(codes[:idx], codes[idx+1:]...)
	_ = model.SetUserRecovery(username, strings.Join(rest, ","))
	return true
}

// twoFAConfirm 校验动态码后正式启用双因素
func twoFAConfirm(w http.ResponseWriter, s *model.Session, req twoFARequest) {
	secret, enabled := model.GetUser2FA(req.Username)
	if enabled {
		writeErr(w, http.StatusBadRequest, "2fa already enabled")
		return
	}
	if secret == "" || !verifyTOTP(secret, req.Code, time.Now()) {
		writeErr(w, http.StatusBadRequest, "invalid code")
		return
	}
	if err := model.SetUser2FA(req.Username, secret, true); err != nil {
		writeErr(w, http.StatusInternalServerError, "store failed")
		return
	}
	codes, err := generateRecoveryCodes(10)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "generate recovery codes failed")
		return
	}
	if err := model.SetUserRecovery(req.Username, strings.Join(codes, ",")); err != nil {
		writeErr(w, http.StatusInternalServerError, "store failed")
		return
	}
	_ = model.AppendAudit(s.Username, "2fa_confirm", "确认启用双因素认证")
	writeJSON(w, map[string]interface{}{"success": true, "recovery_codes": codes})
}

// twoFADisable 校验动态码后关闭双因素
func twoFADisable(w http.ResponseWriter, s *model.Session, req twoFARequest) {
	secret, enabled := model.GetUser2FA(req.Username)
	if !enabled {
		writeErr(w, http.StatusBadRequest, "2fa not enabled")
		return
	}
	if !verifyTOTP(secret, req.Code, time.Now()) {
		writeErr(w, http.StatusBadRequest, "invalid code")
		return
	}
	if err := model.SetUser2FA(req.Username, "", false); err != nil {
		writeErr(w, http.StatusInternalServerError, "store failed")
		return
	}
	_ = model.AppendAudit(s.Username, "2fa_disable", "关闭双因素认证")
	writeJSON(w, map[string]interface{}{"success": true})
}

// TwoFAVerifyHandler POST /api/user/2fa/verify 登录第二步：校验 TOTP 并颁发会话
func TwoFAVerifyHandler(w http.ResponseWriter, r *http.Request) {
	if setTwoFACORS(w, r) {
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
	if !found {
		writeErr(w, http.StatusForbidden, "invalid 2fa code")
		return
	}
	secret, enabled := model.GetUser2FA(req.Username)
	validTOTP := enabled && secret != "" && verifyTOTP(secret, req.Code, time.Now())
	if !validTOTP && !verifyRecoveryCode(req.Username, req.Code) {
		writeErr(w, http.StatusForbidden, "invalid 2fa code")
		return
	}
	tok := model.CreateSession(req.Username)
	writeJSON(w, map[string]interface{}{
		"token":    tok,
		"username": req.Username,
		"role":     u.Role,
	})
}
