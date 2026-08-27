package handler

import (
	"api-gateway/model"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var notifyHTTPClient = &http.Client{Timeout: 5 * time.Second}

// NotifyEvent 将消息推送到所有已配置的通知渠道（webhook / Telegram / 钉钉 / 飞书 / 邮件）。
// 每种渠道在独立 goroutine 中发送，5s 超时，失败仅记录日志，不阻塞调用方。
func NotifyEvent(kind, message string) error {
	webhook, _ := model.KVGet("notify.webhook")
	token, _ := model.KVGet("notify.telegram_bot_token")
	chatID, _ := model.KVGet("notify.telegram_chat_id")
	dingtalk, _ := model.KVGet("notify.dingtalk_webhook")
	feishu, _ := model.KVGet("notify.feishu_webhook")
	emailTo, _ := model.KVGet("notify.email_to")

	if webhook != "" {
		go notifyJSON("webhook", webhook, map[string]string{"text": message})
	}
	if token != "" && chatID != "" {
		go notifyTelegram(token, chatID, message)
	}
	if dingtalk != "" {
		go notifyJSON("dingtalk", dingtalk, map[string]interface{}{
			"msgtype": "text",
			"text":    map[string]string{"content": message},
		})
	}
	if feishu != "" {
		go notifyJSON("feishu", feishu, map[string]interface{}{
			"msg_type": "text",
			"content":  map[string]string{"text": message},
		})
	}
	if emailTo != "" {
		go func() {
			if err := sendEmail(emailTo, "Gateway notification", message); err != nil {
				log.Printf("notify[%s] email to %s failed: %v", kind, emailTo, err)
			}
		}()
	}
	return nil
}

// notifyTelegram 发送 Telegram Bot 消息（application/x-www-form-urlencoded）
func notifyTelegram(token, chatID, message string) {
	form := url.Values{}
	form.Set("chat_id", chatID)
	form.Set("text", message)
	rawURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	notifyPost("telegram", rawURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
}

// notifyJSON 发送 JSON 载荷的 POST 通知
func notifyJSON(tag, rawURL string, payload interface{}) {
	buf, err := json.Marshal(payload)
	if err != nil {
		log.Printf("notify[%s] marshal failed: %v", tag, err)
		return
	}
	notifyPost(tag, rawURL, "application/json", bytes.NewReader(buf))
}

// notifyPost 执行一次带超时的 POST，非 2xx 视为失败并记录响应体
func notifyPost(tag, rawURL, contentType string, body io.Reader) {
	req, err := http.NewRequest(http.MethodPost, rawURL, body)
	if err != nil {
		log.Printf("notify[%s] build request failed: %v", tag, err)
		return
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := notifyHTTPClient.Do(req)
	if err != nil {
		log.Printf("notify[%s] to %s failed: %v", tag, rawURL, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		log.Printf("notify[%s] to %s status %d: %s", tag, rawURL, resp.StatusCode, strings.TrimSpace(string(b)))
	}
}

// SendTestNotifyHandler POST /api/setting/notify/test 发送测试通知（root）
func SendTestNotifyHandler(w http.ResponseWriter, r *http.Request) {
	notifyCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, ok := requireSession(r)
	if !ok || !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	_ = NotifyEvent("test", "这是一条测试通知 test notification")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}
