package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
)

func init() {
	http.HandleFunc("/api/setting/notify", NotifySettingsHandler)
	http.HandleFunc("/api/setting/notify/test", SendTestNotifyHandler)
}

func notifyCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// NotifySettingsHandler GET/POST /api/setting/notify 读写通知渠道配置
// GET: 返回当前配置（telegram_bot_token 打码为 ***+后4位）；POST: 仅 root 可写，逐项写入 KV
func NotifySettingsHandler(w http.ResponseWriter, r *http.Request) {
	notifyCORS(w)
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
		webhook, _ := model.KVGet("notify.webhook")
		token, _ := model.KVGet("notify.telegram_bot_token")
		chatID, _ := model.KVGet("notify.telegram_chat_id")
		dingtalk, _ := model.KVGet("notify.dingtalk_webhook")
		feishu, _ := model.KVGet("notify.feishu_webhook")
		emailTo, _ := model.KVGet("notify.email_to")
		masked := ""
		if len(token) >= 4 {
			masked = "***" + token[len(token)-4:]
		} else if token != "" {
			masked = "***"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"webhook":            webhook,
			"telegram_bot_token": masked,
			"telegram_chat_id":   chatID,
			"dingtalk_webhook":   dingtalk,
			"feishu_webhook":     feishu,
			"email_to":           emailTo,
		})
	case "POST":
		if !model.IsRoot(s.Username) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		var body struct {
			Webhook        string `json:"webhook"`
			TelegramBot    string `json:"telegram_bot_token"`
			TelegramChatID string `json:"telegram_chat_id"`
			Dingtalk       string `json:"dingtalk_webhook"`
			Feishu         string `json:"feishu_webhook"`
			EmailTo        string `json:"email_to"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		for k, v := range map[string]string{
			"notify.webhook":            body.Webhook,
			"notify.telegram_bot_token": body.TelegramBot,
			"notify.telegram_chat_id":   body.TelegramChatID,
			"notify.dingtalk_webhook":   body.Dingtalk,
			"notify.feishu_webhook":     body.Feishu,
			"notify.email_to":           body.EmailTo,
		} {
			if err := model.KVSet(k, v); err != nil {
				http.Error(w, "Save failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
