package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"api-gateway/model"
)

// ChannelHealthHandler GET /api/feat/channels/health
// 遍历所有渠道，向 BaseURL+"/v1/models" 发起带超时探测，返回健康状态。
// 通过 init() 注册路由，避免修改 main.go。
func init() {
	http.HandleFunc("/api/feat/channels/health", ChannelHealthHandler)
}

// channelHealth 单条渠道健康探测结果
type channelHealth struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Group     string `json:"group"`
	Status    string `json:"status"`     // 启用 / 禁用
	HTTPStatus int   `json:"http_status"` // 0 表示探测失败未拿到响应
	LatencyMs int64  `json:"latency_ms"`
	Error     string `json:"error"`
}

// channelHealthResp 响应体
type channelHealthResp struct {
	Channels []channelHealth `json:"channels"`
}

// ChannelHealthHandler 处理渠道健康监控请求（需登录）
func ChannelHealthHandler(w http.ResponseWriter, r *http.Request) {
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

	if _, ok := requireSession(r); !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// 使用 GetAllChannelsRaw 以包含禁用渠道，使 status 字段有意义
	channels, err := model.GetAllChannelsRaw()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	out := make([]channelHealth, 0, len(channels))
	for _, c := range channels {
		out = append(out, probeChannel(c))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(channelHealthResp{Channels: out})
}

// probeChannel 对单个渠道发起健康探测
func probeChannel(c model.Channel) channelHealth {
	res := channelHealth{
		ID:     c.ID,
		Name:   c.Name,
		Group:  c.Group,
		Status: "禁用",
	}
	if c.Status == 1 {
		res.Status = "启用"
	}

	// 构造探测地址 BaseURL + /v1/models
	base := strings.TrimRight(c.BaseURL, "/")
	target := base + "/v1/models"

	key := ""
	if keys := strings.Split(c.Keys, ","); len(keys) > 0 {
		key = strings.TrimSpace(keys[0])
	}

	switch c.AuthType {
	case "query":
		if key != "" && c.AuthKey != "" {
			u, err := url.Parse(target)
			if err == nil {
				q := u.Query()
				q.Set(c.AuthKey, key)
				u.RawQuery = q.Encode()
				target = u.String()
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		target,
		nil,
	)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	switch c.AuthType {
	case "bearer":
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	case "header":
		if key != "" && c.AuthKey != "" {
			req.Header.Set(c.AuthKey, key)
		}
	}

	client := &http.Client{Timeout: 3 * time.Second}
	start := time.Now()
	resp, derr := client.Do(req)
	res.LatencyMs = time.Since(start).Milliseconds()
	if derr != nil {
		msg := derr.Error()
		if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") {
			res.Error = "timeout"
		} else {
			res.Error = "connection failed"
		}
		return res
	}
	defer resp.Body.Close()
	res.HTTPStatus = resp.StatusCode
	return res
}
