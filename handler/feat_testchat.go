package handler

import (
	"api-gateway/model"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

func init() {
	http.HandleFunc("/api/test_chat", TestChatHandler)
}

// TestChatHandler POST /api/test_chat
// 管理员聊天测试窗口：root 会话调用，向 default 分组的首个可用渠道发起一次
// 非流式 /v1/chat/completions 请求，原样返回上游 JSON 响应。
func TestChatHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
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
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Model == "" || len(body.Messages) == 0 {
		http.Error(w, "model and messages required", http.StatusBadRequest)
		return
	}

	modelName := ResolveRedirect(body.Model)
	modelName = ResolveModel(modelName)
	targets := buildTargets("default", modelName)
	if len(targets) == 0 {
		authJSONError(w, http.StatusBadRequest, "no available channel for model")
		return
	}

	t := targets[0]
	upstream := applyModelMapping(t.channel, modelName)
	rawBody, _ := json.Marshal(body)
	payload := rewriteModel(rawBody, upstream)
	r.Body = io.NopCloser(bytes.NewReader(payload))
	r.ContentLength = int64(len(payload))

	bw := &bufferWriter{buf: &bytes.Buffer{}}
	var lastErr error
	proxy := buildForwardProxy(r, t.channel.BaseURL, "/v1/chat/completions", t.channel.AuthType, t.channel.AuthKey, t.key)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		lastErr = err
	}
	proxy.ServeHTTP(bw, r)

	if lastErr != nil || isChannelError(bw.status) {
		markChannelFailure(t.channel.ID)
		authJSONError(w, http.StatusBadGateway, "upstream error")
		return
	}
	markChannelSuccess(t.channel.ID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(bw.status)
	_, _ = w.Write(bw.buf.Bytes())
}
