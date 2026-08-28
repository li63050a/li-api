package handler

import (
	"api-gateway/model"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/v1/messages", SensitiveMiddleware(GuardMiddleware(AnthropicHandler)))
}

// anthropicRequest 是 Anthropic /v1/messages 请求体的精简描述
type anthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	Messages      []anthropicMessage `json:"messages"`
	System        string             `json:"system"`
	Temperature   *float64           `json:"temperature"`
	TopP          *float64           `json:"top_p"`
	Stream        bool               `json:"stream"`
	StopSequences []string           `json:"stop_sequences"`
	Tools         []anthropicTool    `json:"tools"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// AnthropicHandler 把 Claude 风格客户端（POST /v1/messages）转换为 OpenAI 协议后转发，
// 并把上游响应还原为 Anthropic 格式。
func AnthropicHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, anthropic-version")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	key := extractToken(r)
	tok, err := model.GetToken(key)
	if err != nil {
		http.Error(w, "Invalid API Key", http.StatusUnauthorized)
		return
	}
	if tok.IsExpired() {
		http.Error(w, "Token Expired", http.StatusForbidden)
		return
	}
	if tok.Unlimited == 0 && tok.Quota >= 0 && tok.Used >= tok.Quota {
		http.Error(w, "Quota Exceeded", http.StatusForbidden)
		return
	}
	if !model.UserQuotaAllowed(tok.Owner) {
		http.Error(w, "User Quota Exceeded", http.StatusForbidden)
		return
	}
	group := tok.Group
	if group == "" {
		group = "default"
	}
	if u, ok := model.GetUserByUsername(tok.Owner); ok {
		if !userRateAllowed(tok.Owner, u.RateLimit) {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
	}

	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		http.Error(w, "empty request body", http.StatusBadRequest)
		return
	}
	var areq anthropicRequest
	if err := json.Unmarshal(body, &areq); err != nil {
		http.Error(w, "invalid anthropic request", http.StatusBadRequest)
		return
	}
	anthropicModel := areq.Model
	if anthropicModel == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}
	openaiModel := ResolveModel(anthropicModel)
	if !model.TokenModelAllowed(tok.Models, openaiModel) {
		http.Error(w, "Model not allowed for this token", http.StatusForbidden)
		return
	}

	openAIReq := anthropicToOpenAI(&areq)
	if openAIReq == nil {
		http.Error(w, "failed to convert request", http.StatusInternalServerError)
		return
	}

	targets := buildTargets(group, openaiModel)
	if len(targets) == 0 {
		http.Error(w, "No available channel for model: "+openaiModel, http.StatusBadGateway)
		return
	}

	start := time.Now()
	if areq.Stream {
		anthropicStream(w, r, openAIReq, openaiModel, anthropicModel, targets[0], key, tok, group, start)
		return
	}

	// 非流式：缓冲响应，支持跨渠道 / 多密钥故障转移
	var respBody []byte
	var respStatus int
	var respHeader http.Header
	lastErr := error(nil)
	for _, t := range targets {
		upstream := applyModelMapping(t.channel, openaiModel)
		nb := rewriteModel(openAIReq, upstream)
		r.Body = io.NopCloser(bytes.NewReader(nb))
		r.ContentLength = int64(len(nb))
		bw := &bufferWriter{buf: &bytes.Buffer{}}
		proxy := buildForwardProxy(r, t.channel.BaseURL, "/v1/chat/completions", t.channel.AuthType, t.channel.AuthKey, t.key)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			lastErr = err
		}
		proxy.ServeHTTP(bw, r)
		failed := lastErr != nil || isChannelError(bw.status)
		if !failed {
			markChannelSuccess(t.channel.ID)
			respStatus = bw.status
			respHeader = bw.header
			respBody = bw.buf.Bytes()
			break
		}
		markChannelFailure(t.channel.ID)
	}
	if lastErr != nil {
		http.Error(w, "Bad Gateway: "+lastErr.Error(), http.StatusBadGateway)
		return
	}
	if respStatus == 0 {
		respStatus = http.StatusBadGateway
	}
	out := respBody
	if respStatus >= 200 && respStatus < 300 {
		out = openAIReplyToAnthropic(respBody, anthropicModel)
	}
	for k, vs := range respHeader {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(respStatus)
	_, _ = w.Write(out)

	prompt, comp := parseUsageParts(respBody)
	if prompt == 0 && comp == 0 {
		if total := parseUsage(respBody); total > 0 {
			prompt = total
		}
	}
	if prompt == 0 && comp == 0 {
		prompt = maxInt64(1, int64(len(respBody))/4)
	}
	cost := model.ModelCost(openaiModel, prompt, comp)
	_ = model.UseToken(key, cost)
	model.AddUserUsed(tok.Owner, cost)
	recordStats(cost)
	logAccess(map[string]interface{}{
		"time": start.Format(time.RFC3339), "method": r.Method, "path": "/v1/messages",
		"model": openaiModel, "group": group, "token": maskToken(key),
		"status": respStatus, "stream": false, "cost": cost,
		"duration": time.Since(start).Milliseconds(),
	})
}

// anthropicStream 转发并实时把 OpenAI SSE 流转换为 Anthropic SSE 事件
func anthropicStream(w http.ResponseWriter, r *http.Request, openAIReq []byte, openaiModel, anthropicModel string, t target, key string, tok *model.Token, group string, start time.Time) {
	cw := newCountWriter(w)
	upstream := applyModelMapping(t.channel, openaiModel)
	nb := rewriteModel(openAIReq, upstream)
	r.Body = io.NopCloser(bytes.NewReader(nb))
	r.ContentLength = int64(len(nb))
	sw := newAnthropicStreamWriter(cw, anthropicModel, maxInt64(1, int64(len(openAIReq))/4))
	lastErr := error(nil)
	proxy := buildForwardProxy(r, t.channel.BaseURL, "/v1/chat/completions", t.channel.AuthType, t.channel.AuthKey, t.key)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		lastErr = err
		if sw, ok := w.(*anthropicStreamWriter); ok {
			sw.writeError(err)
			return
		}
		http.Error(w, "Bad Gateway: "+err.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(sw, r)

	if lastErr == nil && !isChannelError(cw.status) {
		markChannelSuccess(t.channel.ID)
	} else {
		markChannelFailure(t.channel.ID)
	}
	// 流式按字节计费
	prompt := maxInt64(1, int64(len(openAIReq))/4)
	comp := maxInt64(1, cw.bytes/4)
	cost := model.ModelCost(openaiModel, prompt, comp)
	_ = model.UseToken(key, cost)
	model.AddUserUsed(tok.Owner, cost)
	recordStats(cost)
	logAccess(map[string]interface{}{
		"time": start.Format(time.RFC3339), "method": r.Method, "path": "/v1/messages",
		"model": openaiModel, "group": group, "token": maskToken(key),
		"status": cw.status, "stream": true, "cost": cost,
		"duration": time.Since(start).Milliseconds(),
	})
}

// anthropicStreamWriter 拦截上游 OpenAI SSE 流，逐事件转换为 Anthropic SSE
type anthropicStreamWriter struct {
	http.ResponseWriter
	model       string
	inputEst    int64
	buf         []byte
	status      int
	passthrough bool
	sentStart   bool
	sentBlock   bool
	id          string
	outBytes    int64
	usageIn     int64
	usageOut    int64
}

func newAnthropicStreamWriter(w http.ResponseWriter, model string, inputEst int64) *anthropicStreamWriter {
	return &anthropicStreamWriter{ResponseWriter: w, model: model, inputEst: inputEst, status: http.StatusOK}
}

func (s *anthropicStreamWriter) WriteHeader(code int) {
	s.status = code
	if code != http.StatusOK {
		s.passthrough = true
	}
	if !s.passthrough {
		s.Header().Set("Content-Type", "text/event-stream")
		s.Header().Set("Cache-Control", "no-cache")
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *anthropicStreamWriter) Write(p []byte) (int, error) {
	if s.passthrough {
		return s.ResponseWriter.Write(p)
	}
	s.process(p)
	return len(p), nil
}

func (s *anthropicStreamWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *anthropicStreamWriter) writeEvent(event string, payload interface{}) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	var out bytes.Buffer
	out.WriteString("event: ")
	out.WriteString(event)
	out.WriteString("\ndata: ")
	out.Write(b)
	out.WriteString("\n\n")
	_, _ = s.ResponseWriter.Write(out.Bytes())
	s.Flush()
}

func (s *anthropicStreamWriter) writeError(err error) {
	if s.passthrough {
		return
	}
	s.writeEvent("error", map[string]interface{}{
		"type":  "error",
		"error": map[string]interface{}{"type": "api_error", "message": err.Error()},
	})
}

func (s *anthropicStreamWriter) msgID() string {
	return "msg_" + shortID(s.id)
}

func (s *anthropicStreamWriter) process(data []byte) {
	s.buf = append(s.buf, data...)
	for {
		idx := bytes.IndexByte(s.buf, '\n')
		if idx < 0 {
			return
		}
		line := strings.TrimRight(string(s.buf[:idx]), "\r")
		s.buf = s.buf[idx+1:]
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			s.handleDone()
			continue
		}
		s.handleChunk(payload)
	}
}

func (s *anthropicStreamWriter) handleChunk(payload string) {
	var c struct {
		ID    string `json:"id"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		return
	}
	if c.ID != "" {
		s.id = c.ID
	}
	if c.Usage.PromptTokens > 0 {
		s.usageIn = c.Usage.PromptTokens
	}
	if c.Usage.CompletionTokens > 0 {
		s.usageOut = c.Usage.CompletionTokens
	}
	if !s.sentStart {
		input := s.usageIn
		if input == 0 {
			input = s.inputEst
		}
		s.writeEvent("message_start", map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":      s.msgID(),
				"type":    "message",
				"role":    "assistant",
				"content": []interface{}{},
				"model":   s.model,
				"usage":   map[string]interface{}{"input_tokens": input, "output_tokens": 0},
			},
		})
		s.sentStart = true
	}
	if len(c.Choices) == 0 {
		return
	}
	if text := c.Choices[0].Delta.Content; text != "" {
		if !s.sentBlock {
			s.writeEvent("content_block_start", map[string]interface{}{
				"type":          "content_block_start",
				"index":         0,
				"content_block": map[string]interface{}{"type": "text", "text": ""},
			})
			s.sentBlock = true
		}
		s.writeEvent("content_block_delta", map[string]interface{}{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]interface{}{"type": "text_delta", "text": text},
		})
		s.outBytes += int64(len(text))
	}
}

func (s *anthropicStreamWriter) handleDone() {
	out := s.usageOut
	if out == 0 {
		out = maxInt64(1, s.outBytes/4)
	}
	s.writeEvent("message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": map[string]interface{}{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]interface{}{"output_tokens": out},
	})
	s.writeEvent("message_stop", map[string]interface{}{"type": "message_stop"})
}

// anthropicToOpenAI 把 Anthropic 请求转换为 OpenAI chat/completions 请求体
func anthropicToOpenAI(a *anthropicRequest) []byte {
	req := map[string]interface{}{}
	if a.Model != "" {
		req["model"] = a.Model
	}
	if a.MaxTokens > 0 {
		req["max_tokens"] = a.MaxTokens
	}
	if a.Temperature != nil {
		req["temperature"] = *a.Temperature
	}
	if a.TopP != nil {
		req["top_p"] = *a.TopP
	}
	if a.Stream {
		req["stream"] = true
	}
	if len(a.StopSequences) > 0 {
		req["stop"] = a.StopSequences
	}
	if len(a.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(a.Tools))
		for _, t := range a.Tools {
			fn := map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
			}
			if len(t.InputSchema) > 0 {
				var params interface{}
				if json.Unmarshal(t.InputSchema, &params) == nil {
					fn["parameters"] = params
				}
			}
			tools = append(tools, map[string]interface{}{"type": "function", "function": fn})
		}
		req["tools"] = tools
	}
	var msgs []map[string]interface{}
	if a.System != "" {
		msgs = append(msgs, map[string]interface{}{"role": "system", "content": a.System})
	}
	for _, m := range a.Messages {
		if m.Role == "" {
			continue
		}
		msgs = append(msgs, map[string]interface{}{
			"role":    m.Role,
			"content": anthropicContentToString(m.Content),
		})
	}
	req["messages"] = msgs
	b, err := json.Marshal(req)
	if err != nil {
		return nil
	}
	return b
}

// anthropicContentToString 把 content（字符串或块数组）归一化为单个文本字符串
func anthropicContentToString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return string(raw)
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case "image":
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "")
}

// openAIReplyToAnthropic 把 OpenAI chat/completions 响应转换为 Anthropic message 格式
func openAIReplyToAnthropic(body []byte, model string) []byte {
	var r struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return body
	}
	var content []map[string]interface{}
	if len(r.Choices) > 0 {
		if r.Choices[0].Message.Content != "" {
			content = append(content, map[string]interface{}{"type": "text", "text": r.Choices[0].Message.Content})
		}
		for _, tc := range r.Choices[0].Message.ToolCalls {
			var input interface{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
				input = tc.Function.Arguments
			}
			content = append(content, map[string]interface{}{
				"type":  "tool_use",
				"id":    tc.ID,
				"name":  tc.Function.Name,
				"input": input,
			})
		}
	}
	if len(content) == 0 {
		content = []map[string]interface{}{{"type": "text", "text": ""}}
	}
	out := map[string]interface{}{
		"id":          "msg_" + shortID(r.ID),
		"type":        "message",
		"role":        "assistant",
		"model":       model,
		"content":     content,
		"stop_reason": "end_turn",
		"usage": map[string]interface{}{
			"input_tokens":  r.Usage.PromptTokens,
			"output_tokens": r.Usage.CompletionTokens,
		},
	}
	b, err := json.Marshal(out)
	if err != nil {
		return body
	}
	return b
}

// shortID 从上游 id 派生一个短的 msg 后缀；为空时生成随机十六进制
func shortID(s string) string {
	s = strings.TrimPrefix(s, "chatcmpl-")
	if s != "" {
		s = strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				return r
			}
			return -1
		}, s)
		if len(s) > 12 {
			s = s[:12]
		}
		if s != "" {
			return s
		}
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
