package handler

import (
	"api-gateway/model"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

func init() {
	http.HandleFunc("/api/setting/oauth/wechat", WechatOAuthConfigHandler)
	http.HandleFunc("/oauth/wechat/authorize", WechatOAuthHandler)
	http.HandleFunc("/oauth/wechat/callback", WechatOAuthHandler)
}

// WechatOAuthConfigHandler GET/POST /api/setting/oauth/wechat（root）
// GET: 返回 appid 与是否已配置；POST: 写入 wechat.appid / wechat.secret
func WechatOAuthConfigHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
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
	case http.MethodGet:
		appid, _ := model.KVGet("wechat.appid")
		secret, _ := model.KVGet("wechat.secret")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"appid":         appid,
			"secret_masked": secret != "",
			"configured":    appid != "" && secret != "",
		})
	case http.MethodPost:
		var body struct {
			Appid  string `json:"appid"`
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		for _, kv := range [][2]string{
			{"wechat.appid", body.Appid},
			{"wechat.secret", body.Secret},
		} {
			if err := model.KVSet(kv[0], kv[1]); err != nil {
				http.Error(w, "Save failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// WechatOAuthHandler GET /oauth/wechat/authorize 与 GET /oauth/wechat/callback
// authorize: 已配置 appid 时 302 跳转微信扫码授权；callback: 尚未实现，返回 501
func WechatOAuthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/oauth/wechat/callback" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "wechat callback not implemented yet"})
		return
	}
	appid, ok := model.KVGet("wechat.appid")
	if !ok || appid == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "wechat not configured"})
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	redirectURI := scheme + "://" + r.Host + "/oauth/wechat/callback"
	target := fmt.Sprintf(
		"https://open.weixin.qq.com/connect/qrconnect?appid=%s&redirect_uri=%s&response_type=code&scope=snsapi_login",
		url.QueryEscape(appid), url.QueryEscape(redirectURI))
	http.Redirect(w, r, target, http.StatusFound)
}