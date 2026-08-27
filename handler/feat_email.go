package handler

import (
	"api-gateway/model"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

const (
	emailCodeTTL  = 10 * time.Minute
	resetTokenTTL = 30 * time.Minute
)

// codeEntry 邮箱验证码条目
type codeEntry struct {
	email  string
	code   string
	expiry time.Time
}

// resetEntry 密码重置令牌条目
type resetEntry struct {
	username string
	expiry   time.Time
}

var (
	emailCodesMu sync.Mutex
	emailCodes   = map[string]codeEntry{}

	resetTokensMu sync.Mutex
	resetTokens   = map[string]resetEntry{}
)

func init() {
	http.HandleFunc("/api/user/email", EmailHandler)
	http.HandleFunc("/api/user/email/verify", EmailHandler)
	http.HandleFunc("/api/user/reset/send", ResetPwdHandler)
	http.HandleFunc("/api/user/reset", ResetPwdHandler)
}

func setEmailCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// EmailHandler POST /api/user/email 绑定邮箱（发送 6 位验证码）；POST /api/user/email/verify 校验并绑定
func EmailHandler(w http.ResponseWriter, r *http.Request) {
	setEmailCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "POST" {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	s, ok := authorizedUser(r, req.Username)
	if !ok {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if strings.HasSuffix(r.URL.Path, "/verify") {
		emailVerify(w, s, req.Username, req.Code)
		return
	}
	emailSendCode(w, s, req.Username, req.Email)
}

// emailSendCode 生成 6 位验证码并发送到目标邮箱
func emailSendCode(w http.ResponseWriter, s *model.Session, username, email string) {
	if email == "" {
		writeErr(w, http.StatusBadRequest, "email required")
		return
	}
	if model.GetSetting().SMTPHost == "" {
		writeErr(w, http.StatusInternalServerError, "smtp not configured")
		return
	}
	code, err := randomDigits(6)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "generate code failed")
		return
	}
	emailCodesMu.Lock()
	purgeExpiredCodesLocked()
	emailCodes[username+"|"+code] = codeEntry{email: email, code: code, expiry: time.Now().Add(emailCodeTTL)}
	emailCodesMu.Unlock()

	if err := emailSend(email, "Verification code", "Your verification code is: "+code+"\r\n\r\nThis code expires in 10 minutes."); err != nil {
		writeErr(w, http.StatusInternalServerError, "send failed: "+err.Error())
		return
	}
	_ = model.AppendAudit(s.Username, "email_send", "向 "+username+" 发送邮箱验证码")
	writeJSON(w, map[string]interface{}{"success": true, "sent": true})
}

// emailVerify 校验验证码并绑定邮箱
func emailVerify(w http.ResponseWriter, s *model.Session, username, code string) {
	if code == "" {
		writeErr(w, http.StatusBadRequest, "code required")
		return
	}
	emailCodesMu.Lock()
	purgeExpiredCodesLocked()
	entry, ok := emailCodes[username+"|"+code]
	if ok {
		delete(emailCodes, username+"|"+code)
	}
	emailCodesMu.Unlock()
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid or expired code")
		return
	}
	if err := model.SetUserEmail(username, entry.email); err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	_ = model.AppendAudit(s.Username, "email_verify", "绑定邮箱 "+entry.email)
	writeJSON(w, map[string]interface{}{"success": true, "email": entry.email})
}

// purgeExpiredCodesLocked 惰性清理已过期的验证码（调用方需持锁）
func purgeExpiredCodesLocked() {
	now := time.Now()
	for k, v := range emailCodes {
		if now.After(v.expiry) {
			delete(emailCodes, k)
		}
	}
}

// ResetPwdHandler POST /api/user/reset/send 发送重置链接；POST /api/user/reset 凭令牌重置密码
func ResetPwdHandler(w http.ResponseWriter, r *http.Request) {
	setEmailCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "POST" {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if strings.HasSuffix(r.URL.Path, "/send") {
		resetSendLink(w, r, req.Email)
		return
	}
	resetApply(w, req.Token, req.Password)
}

// resetSendLink 若存在绑定该邮箱的用户，则生成一次性令牌并通过邮件发送重置链接
func resetSendLink(w http.ResponseWriter, r *http.Request, email string) {
	if email == "" {
		writeErr(w, http.StatusBadRequest, "email required")
		return
	}
	username := ""
	for _, u := range model.GetAllUsers() {
		if u.Email == email {
			username = u.Username
			break
		}
	}
	if username == "" {
		writeJSON(w, map[string]interface{}{"success": true, "sent": false})
		return
	}
	if model.GetSetting().SMTPHost == "" {
		writeErr(w, http.StatusInternalServerError, "smtp not configured")
		return
	}
	token, err := randomToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "generate token failed")
		return
	}
	resetTokensMu.Lock()
	purgeExpiredTokensLocked()
	resetTokens[token] = resetEntry{username: username, expiry: time.Now().Add(resetTokenTTL)}
	resetTokensMu.Unlock()

	base := r.Header.Get("Origin")
	if base == "" {
		base = "http://" + r.Host
	}
	link := base + "/api/user/reset?token=" + token
	body := "Password reset link: " + link + "\r\n\r\nThis link expires in 30 minutes. If you did not request this, ignore this email."
	if err := emailSend(email, "Password reset", body); err != nil {
		writeErr(w, http.StatusInternalServerError, "send failed: "+err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"success": true, "sent": true})
}

// resetApply 校验一次性令牌并重置用户密码
func resetApply(w http.ResponseWriter, token, password string) {
	if token == "" || password == "" {
		writeErr(w, http.StatusBadRequest, "token and password required")
		return
	}
	resetTokensMu.Lock()
	purgeExpiredTokensLocked()
	entry, ok := resetTokens[token]
	if ok {
		delete(resetTokens, token)
	}
	resetTokensMu.Unlock()
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid or expired token")
		return
	}
	u, found := model.GetUserByUsername(entry.username)
	if !found {
		writeErr(w, http.StatusBadRequest, "invalid or expired token")
		return
	}
	if err := model.UpdateUser(u.ID, model.User{PasswordHash: model.HashPassword(password)}); err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	_ = model.AppendAudit(entry.username, "reset_pwd", "通过邮箱重置密码")
	writeJSON(w, map[string]interface{}{"success": true})
}

// purgeExpiredTokensLocked 惰性清理已过期的重置令牌（调用方需持锁）
func purgeExpiredTokensLocked() {
	now := time.Now()
	for k, v := range resetTokens {
		if now.After(v.expiry) {
			delete(resetTokens, k)
		}
	}
}

// randomDigits 生成 n 位随机数字串
func randomDigits(n int) (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	v := int(buf[0])<<24 | int(buf[1])<<16 | int(buf[2])<<8 | int(buf[3])
	if v < 0 {
		v = -v
	}
	return fmt.Sprintf("%0*d", n, v%1000000), nil
}

// randomToken 生成 32 位十六进制随机令牌
func randomToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// emailSend 通过全局配置的 SMTP 发送邮件（SMTPUser 为空时不使用认证）
func emailSend(to, subject, body string) error {
	s := model.GetSetting()
	if s.SMTPHost == "" {
		return fmt.Errorf("smtp not configured")
	}
	port := s.SMTPPort
	if port == 0 {
		port = 25
	}
	from := s.SMTPFrom
	if from == "" {
		from = s.SMTPUser
	}
	if from == "" {
		from = to
	}
	var auth smtp.Auth
	if s.SMTPUser != "" {
		auth = smtp.PlainAuth("", s.SMTPUser, s.SMTPPass, s.SMTPHost)
	}
	addr := fmt.Sprintf("%s:%d", s.SMTPHost, port)
	subj := mime.QEncoding.Encode("UTF-8", subject)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", from, to, subj, body)
	return smtp.SendMail(addr, auth, from, []string{to}, []byte(msg))
}
