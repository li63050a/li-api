package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"api-gateway/model"
)

// 渠道标签 / 批量管理 / 模型同步相关接口（均仅 root）：
//
//	POST /admin/channels/sync    同步渠道模型列表（从上游 /v1/models 拉取合并）
//	POST /admin/channels/batch   批量启用 / 禁用 / 删除渠道，可按标签过滤
//	GET  /admin/channels/tags    返回全部渠道的去重标签列表
//
// 通过 init() 注册路由，避免修改 main.go。
func init() {
	http.HandleFunc("/admin/channels/sync", SyncModelsHandler)
	http.HandleFunc("/admin/channels/batch", ChannelBatchHandler)
	http.HandleFunc("/admin/channels/tags", ChannelTagHandler)
}

func setSyncCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// SyncModelsHandler POST /admin/channels/sync
// 请求体 {ids:[]int}，ids 为空表示全部启用渠道。
// 逐个渠道请求 BaseURL+"/v1/models"（Authorization: Bearer 第一个密钥，8s 超时），
// 解析 {"data":[{id}]} 并把返回的模型 id 去重合并进 channel.Models（跳过 "*"，保持顺序）。
func SyncModelsHandler(w http.ResponseWriter, r *http.Request) {
	setSyncCORS(w)
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

	var req struct {
		IDs []int `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	var chans []model.Channel
	if len(req.IDs) == 0 {
		cs, err := model.GetAllChannels()
		if err != nil {
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		chans = cs
	} else {
		for _, id := range req.IDs {
			if c, ok := model.GetChannel(id); ok {
				chans = append(chans, c)
			}
		}
	}

	type syncResult struct {
		ID    int    `json:"id"`
		Name  string `json:"name"`
		Added int    `json:"added"`
		Error string `json:"error"`
	}
	results := make([]syncResult, 0, len(chans))

	for _, c := range chans {
		res := syncResult{ID: c.ID, Name: c.Name}
		models, err := fetchChannelModels(c)
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		merged, added := mergeChannelModels(c.Models, models)
		c.Models = merged
		if err := model.UpdateChannel(c.ID, &c); err != nil {
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		res.Added = added
		results = append(results, res)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"results": results})
}

// fetchChannelModels 请求上游 /v1/models 并返回模型 id 列表
func fetchChannelModels(c model.Channel) ([]string, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	target := base + "/v1/models"

	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if keys := c.KeyList(); len(keys) > 0 {
		req.Header.Set("Authorization", "Bearer "+keys[0])
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &syncHTTPError{status: resp.StatusCode}
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

type syncHTTPError struct {
	status int
}

func (e *syncHTTPError) Error() string {
	return "upstream returned HTTP " + http.StatusText(e.status)
}

// mergeChannelModels 把 fetched 中的模型 id 去重合并进 existing 逗号列表（跳过 "*"，保持顺序）
func mergeChannelModels(existing string, fetched []string) (string, int) {
	seen := make(map[string]bool)
	merged := make([]string, 0)
	for _, m := range strings.Split(existing, ",") {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		merged = append(merged, m)
	}
	added := 0
	for _, m := range fetched {
		m = strings.TrimSpace(m)
		if m == "" || m == "*" || seen[m] {
			continue
		}
		seen[m] = true
		merged = append(merged, m)
		added++
	}
	return strings.Join(merged, ","), added
}

// ChannelBatchHandler POST /admin/channels/batch
// 请求体 {ids:[]int, action:"enable"|"disable"|"delete", tag:""}，
// tag 非空时 ids 被替换为该标签下的全部渠道。
func ChannelBatchHandler(w http.ResponseWriter, r *http.Request) {
	setSyncCORS(w)
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

	var req struct {
		IDs    []int  `json:"ids"`
		Action string `json:"action"`
		Tag    string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Tag != "" {
		cs := model.GetChannelsByTag(req.Tag)
		req.IDs = make([]int, 0, len(cs))
		for _, c := range cs {
			req.IDs = append(req.IDs, c.ID)
		}
	}

	count := 0
	for _, id := range req.IDs {
		switch req.Action {
		case "enable", "disable":
			c, ok := model.GetChannel(id)
			if !ok {
				continue
			}
			if req.Action == "enable" {
				c.Status = 1
			} else {
				c.Status = 0
			}
			if err := model.UpdateChannel(id, &c); err != nil {
				continue
			}
		case "delete":
			if err := model.DeleteChannel(id); err != nil {
				continue
			}
		default:
			http.Error(w, "Invalid action", http.StatusBadRequest)
			return
		}
		count++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "count": count})
}

// ChannelTagHandler GET /admin/channels/tags
// 返回全部渠道（含禁用）的去重标签列表。
func ChannelTagHandler(w http.ResponseWriter, r *http.Request) {
	setSyncCORS(w)
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

	cs, err := model.GetAllChannelsRaw()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	seen := make(map[string]bool)
	tags := make([]string, 0)
	for _, c := range cs {
		for _, t := range strings.Split(c.Tags, ",") {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			key := strings.ToLower(t)
			if seen[key] {
				continue
			}
			seen[key] = true
			tags = append(tags, t)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"tags": tags})
}
