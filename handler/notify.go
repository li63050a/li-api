package handler

import (
	"api-gateway/model"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
)

// sendEmail 通过配置好的 SMTP 发送一封邮件（使用 STARTTLS）
func sendEmail(to, subject, body string) error {
	s := model.GetSetting()
	if s.SMTPHost == "" || s.SMTPUser == "" {
		return fmt.Errorf("SMTP 未配置")
	}
	if s.SMTPPort == 0 {
		return fmt.Errorf("SMTP 端口未配置")
	}
	addr := fmt.Sprintf("%s:%d", s.SMTPHost, s.SMTPPort)
	auth := smtp.PlainAuth("", s.SMTPUser, s.SMTPPass, s.SMTPHost)
	from := s.SMTPFrom
	if from == "" {
		from = s.SMTPUser
	}
	msg := []byte("From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" +
		body)
	return smtp.SendMail(addr, auth, from, []string{to}, msg)
}

// notifyChannelDisabled 渠道被自动禁用时发送通知
func notifyChannelDisabled(id int) {
	s := model.GetSetting()
	if s.NotifyEmail == "" {
		return
	}
	_ = sendEmail(s.NotifyEmail, "渠道自动禁用通知",
		fmt.Sprintf("渠道 #%d 因连续失败已被自动禁用，请检查上游可用性。", id))
}

// TestEmailHandler POST /api/setting/test_email 发送测试邮件（root）
func TestEmailHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
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
	var req struct {
		To string `json:"to"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	to := req.To
	if to == "" {
		to = model.GetSetting().NotifyEmail
	}
	if to == "" {
		http.Error(w, "未指定接收邮箱", http.StatusBadRequest)
		return
	}
	if err := sendEmail(to, "api-gateway 测试邮件", "这是一封来自 api-gateway 的测试邮件，说明 SMTP 配置可用。"); err != nil {
		http.Error(w, "发送失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "测试邮件已发送至 " + to})
}
