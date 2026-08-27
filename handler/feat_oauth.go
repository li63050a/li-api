package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"api-gateway/model"
)

const (
	kvGithubClientID     = "oauth.github.client_id"
	kvGithubClientSecret = "oauth.github.client_secret"
	kvGoogleClientID     = "oauth.google.client_id"
	kvGoogleClientSecret = "oauth.google.client_secret"
)

var oauthHTTPClient = &http.Client{Timeout: 15 * time.Second}

func init() {
	http.HandleFunc("/oauth/", OAuthHandler)
	http.HandleFunc("/api/setting/oauth", OAuthConfigHandler)
}

func setOAuthCORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

func writeOAuthJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeOAuthErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{"error": msg})
}

func randomHexBytes(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// oauthRedirectURI 由当前请求推导回调地址（尊重反代 X-Forwarded-Proto）
func oauthRedirectURI(r *http.Request, provider string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd == "http" || fwd == "https" {
		scheme = fwd
	}
	return scheme + "://" + r.Host + "/oauth/" + provider + "/callback"
}

// oauthResultPage 向父窗口 postMessage 结果后关闭弹窗
func oauthResultPage(w http.ResponseWriter, payload map[string]interface{}) {
	data, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<html><body><script>window.opener.postMessage(%s,"*");window.close();</script></body></html>`, data)
}

func oauthFailPage(w http.ResponseWriter, msg string) {
	oauthResultPage(w, map[string]interface{}{"type": "oauth", "error": msg})
}

// OAuthHandler GET /oauth/<provider>[/callback] 第三方 OAuth 登录
func OAuthHandler(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/oauth/")
	rest = strings.TrimSuffix(rest, "/")
	parts := strings.SplitN(rest, "/", 2)
	provider := parts[0]
	if provider != "github" && provider != "google" {
		writeOAuthErr(w, http.StatusNotFound, "unknown oauth provider")
		return
	}
	if len(parts) == 2 && parts[1] == "callback" {
		oauthCallback(w, r, provider)
		return
	}
	oauthStart(w, r, provider)
}

// oauthStart 发起授权：校验配置并重定向到第三方
func oauthStart(w http.ResponseWriter, r *http.Request, provider string) {
	clientID, ok := model.KVGet("oauth." + provider + ".client_id")
	if !ok || clientID == "" {
		writeOAuthErr(w, http.StatusBadRequest, "oauth not configured")
		return
	}
	state, err := randomHexBytes(8)
	if err != nil {
		writeOAuthErr(w, http.StatusInternalServerError, "state generation failed")
		return
	}
	redirectURI := oauthRedirectURI(r, provider)
	values := url.Values{
		"client_id":    {clientID},
		"redirect_uri": {redirectURI},
		"state":        {state},
	}
	var endpoint string
	if provider == "github" {
		endpoint = "https://github.com/login/oauth/authorize"
		values.Set("scope", "read:user user:email")
	} else {
		endpoint = "https://accounts.google.com/o/oauth2/v2/auth"
		values.Set("response_type", "code")
		values.Set("scope", "email profile")
	}
	http.Redirect(w, r, endpoint+"?"+values.Encode(), http.StatusFound)
}

// oauthCallback 回调：兑换 token、拉取用户信息、注册或登录并建立会话
func oauthCallback(w http.ResponseWriter, r *http.Request, provider string) {
	code := r.URL.Query().Get("code")
	if code == "" {
		oauthFailPage(w, "no code returned")
		return
	}
	clientID, _ := model.KVGet("oauth." + provider + ".client_id")
	clientSecret, _ := model.KVGet("oauth." + provider + ".client_secret")
	if clientID == "" || clientSecret == "" {
		oauthFailPage(w, "oauth not configured")
		return
	}
	token, err := oauthExchangeToken(provider, clientID, clientSecret, code, oauthRedirectURI(r, provider))
	if err != nil {
		oauthFailPage(w, err.Error())
		return
	}
	username, email, err := oauthFetchUser(provider, token)
	if err != nil {
		oauthFailPage(w, err.Error())
		return
	}
	if username == "" {
		oauthFailPage(w, "could not determine username")
		return
	}
	if _, exists := model.GetUserByUsername(username); !exists {
		pw, err := randomHexBytes(16)
		if err != nil {
			oauthFailPage(w, "password generation failed")
			return
		}
		_ = model.CreateUser(username, pw, "user", 0)
	}
	if email != "" {
		_ = model.SetUserEmail(username, email)
	}
	tok := model.CreateSession(username)
	_ = model.AppendAudit(username, "oauth_login", "通过 "+provider+" 登录")
	oauthResultPage(w, map[string]interface{}{"type": "oauth", "token": tok, "username": username})
}

// oauthExchangeToken 用 code 兑换 access_token
func oauthExchangeToken(provider, clientID, clientSecret, code, redirectURI string) (string, error) {
	var endpoint string
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}
	if provider == "github" {
		endpoint = "https://github.com/login/oauth/access_token"
	} else {
		endpoint = "https://oauth2.googleapis.com/token"
		form.Set("grant_type", "authorization_code")
	}
	req, err := http.NewRequest("POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", errors.New("invalid token response")
	}
	if tok, _ := out["access_token"].(string); tok != "" {
		return tok, nil
	}
	if e, _ := out["error"].(string); e != "" {
		return "", errors.New(e)
	}
	return "", errors.New("no access_token in response")
}

// oauthFetchUser 拉取用户信息，返回 (username, email)
func oauthFetchUser(provider, token string) (string, string, error) {
	var endpoint string
	if provider == "github" {
		endpoint = "https://api.github.com/user"
	} else {
		endpoint = "https://openidconnect.googleapis.com/v1/userinfo"
	}
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", errors.New("userinfo request failed: " + resp.Status)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", errors.New("invalid userinfo response")
	}
	username := ""
	email, _ := out["email"].(string)
	if provider == "github" {
		username, _ = out["login"].(string)
	} else {
		if i := strings.IndexByte(email, '@'); i > 0 {
			username = email[:i]
		}
	}
	return username, email, nil
}

// OAuthConfigHandler GET/POST /api/setting/oauth 管理第三方 OAuth 配置（仅 root 可写）
func OAuthConfigHandler(w http.ResponseWriter, r *http.Request) {
	if setOAuthCORS(w, r) {
		return
	}
	switch r.Method {
	case "GET":
		githubID, _ := model.KVGet(kvGithubClientID)
		githubSecret, _ := model.KVGet(kvGithubClientSecret)
		googleID, _ := model.KVGet(kvGoogleClientID)
		googleSecret, _ := model.KVGet(kvGoogleClientSecret)
		writeOAuthJSON(w, map[string]interface{}{
			"github_client_id":  githubID,
			"github_configured": githubID != "" && githubSecret != "",
			"google_client_id":  googleID,
			"google_configured": googleID != "" && googleSecret != "",
		})
	case "POST":
		s, ok := requireSession(r)
		if !ok || !model.IsRoot(s.Username) {
			writeOAuthErr(w, http.StatusForbidden, "forbidden")
			return
		}
		var req struct {
			GithubClientID     string `json:"github_client_id"`
			GithubClientSecret string `json:"github_client_secret"`
			GoogleClientID     string `json:"google_client_id"`
			GoogleClientSecret string `json:"google_client_secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeOAuthErr(w, http.StatusBadRequest, "invalid request")
			return
		}
		_ = model.KVSet(kvGithubClientID, req.GithubClientID)
		_ = model.KVSet(kvGithubClientSecret, req.GithubClientSecret)
		_ = model.KVSet(kvGoogleClientID, req.GoogleClientID)
		_ = model.KVSet(kvGoogleClientSecret, req.GoogleClientSecret)
		_ = model.AppendAudit(s.Username, "oauth_config", "更新第三方 OAuth 配置")
		writeOAuthJSON(w, map[string]interface{}{"ok": true})
	default:
		writeOAuthErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}