package handler

import (
	"api-gateway/model"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/admin/channels/test", ChannelTestFormHandler)
	http.HandleFunc("/api/models_list", ModelsListHandler)
	http.HandleFunc("/admin/users/batch", BatchCreateUsersHandler)
}

// ChannelTestFormHandler POST /admin/channels/test（root）
// 保存前测试连通性：body {"base_url","auth_type","auth_key","key"}，
// 向 base_url+/v1/models（query 认证追加 ?api-version=<auth_key>）发探测请求，
// 返回 {"ok","status","latency_ms","models_count","error"}。
func ChannelTestFormHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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
	var body struct {
		BaseURL  string `json:"base_url"`
		AuthType string `json:"auth_type"`
		AuthKey  string `json:"auth_key"`
		Key      string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.BaseURL) == "" {
		http.Error(w, "base_url 必填", http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(testChannelConnectivity(body.BaseURL, body.AuthType, body.AuthKey, body.Key))
}

func testChannelConnectivity(baseURL, authType, authKey, key string) map[string]interface{} {
	target := strings.TrimRight(baseURL, "/") + "/v1/models"
	if authType == "query" && authKey != "" {
		target += "?api-version=" + url.QueryEscape(authKey)
	}
	req, err := http.NewRequest("GET", target, nil)
	if err != nil {
		return map[string]interface{}{"ok": false, "status": 0, "latency_ms": 0, "models_count": 0, "error": err.Error()}
	}
	switch authType {
	case "header":
		if authKey != "" {
			req.Header.Set(authKey, key)
		}
	default:
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	client := &http.Client{Timeout: 8 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return map[string]interface{}{"ok": false, "status": 0, "latency_ms": int(latency), "models_count": 0, "error": err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	status := resp.StatusCode
	ok := status >= 200 && status < 300
	modelsCount := 0
	var parsed struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &parsed) == nil {
		modelsCount = len(parsed.Data)
	}
	errorStr := ""
	if !ok {
		errorStr = strings.TrimSpace(string(raw))
		if len(errorStr) > 300 {
			errorStr = errorStr[:300]
		}
	}
	return map[string]interface{}{
		"ok":           ok,
		"status":       status,
		"latency_ms":   int(latency),
		"models_count": modelsCount,
		"error":        errorStr,
	}
}

// ModelsListHandler GET /api/models_list（root）
// 汇总所有可用模型：渠道 Models（跳过 "*"）、别名（alias. 键）、
// 虚拟模型展示名、全局重定向目标（redirect. 值），去重排序后返回 {"models":[...]}。
func ModelsListHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, ok := requireSession(r)
	if !ok || !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	set := map[string]bool{}
	all, _ := model.GetAllChannelsRaw()
	for _, c := range all {
		if c.Models == "" || c.Models == "*" {
			continue
		}
		for _, m := range strings.Split(c.Models, ",") {
			if m = strings.TrimSpace(m); m != "" {
				set[m] = true
			}
		}
	}
	for k := range model.KVGetAll("alias.") {
		set[k] = true
	}
	for _, vm := range model.GetVModels() {
		set[vm.Display] = true
	}
	for _, v := range model.KVGetAll("redirect.") {
		if v = strings.TrimSpace(v); v != "" {
			set[v] = true
		}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"models": keysOf(set)})
}

// BatchCreateUsersHandler POST /admin/users/batch（root）
// 批量建号：body {"usernames":["a","b"],"password":"...","quota":0}
// （usernames 也可为 "\n" 分隔的字符串），逐个 model.CreateUser 创建，
// 返回 {"created":n,"failed":[{username,error}]}。
func BatchCreateUsersHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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
	var body struct {
		Usernames json.RawMessage `json:"usernames"`
		Password  string          `json:"password"`
		Quota     int64           `json:"quota"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}
	if body.Password == "" {
		http.Error(w, "password 必填", http.StatusBadRequest)
		return
	}
	var names []string
	if len(body.Usernames) == 0 {
		http.Error(w, "usernames 必填", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(body.Usernames, &names); err != nil {
		var s string
		if err := json.Unmarshal(body.Usernames, &s); err != nil {
			http.Error(w, "usernames 需为数组或字符串", http.StatusBadRequest)
			return
		}
		for _, line := range strings.Split(s, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				names = append(names, line)
			}
		}
	}
	created := 0
	failed := []map[string]string{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if err := model.CreateUser(name, body.Password, "user", body.Quota); err != nil {
			failed = append(failed, map[string]string{"username": name, "error": err.Error()})
			continue
		}
		created++
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"created": created,
		"failed":  failed,
	})
}