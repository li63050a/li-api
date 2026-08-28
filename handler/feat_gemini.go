package handler

import (
	"api-gateway/model"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/v1beta/", SensitiveMiddleware(GuardMiddleware(GeminiHandler)))
}

func setGeminiCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
}

// GeminiHandler 将 Google Gemini 原生 REST 请求转换为 OpenAI 协议后转发：
//
//	POST /v1beta/models/{model}:generateContent
//	POST /v1beta/models/{model}:streamGenerateContent?alt=sse
//	POST /v1beta/models/{model}:embedContent
//
// 认证 / 选渠道 / 计费逻辑与 RelayHandler 保持一致。
func GeminiHandler(w http.ResponseWriter, r *http.Request) {
	setGeminiCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
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

	modelName, op := parseGeminiPath(r.URL.Path)
	if modelName == "" || op == "" {
		http.NotFound(w, r)
		return
	}
	switch op {
	case "generateContent", "streamGenerateContent", "embedContent":
	default:
		http.NotFound(w, r)
		return
	}

	modelName = ResolveModel(modelName)
	if !model.TokenModelAllowed(tok.Models, modelName) {
		http.Error(w, "Model not allowed for this token", http.StatusForbidden)
		return
	}

	targets := buildTargets(group, modelName)
	if len(targets) == 0 {
		http.Error(w, "No available channel for model: "+modelName, http.StatusBadGateway)
		return
	}

	var bodyBuf []byte
	if r.Body != nil {
		bodyBuf, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
	}
	var probe map[string]interface{}
	if json.Unmarshal(bodyBuf, &probe) != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	start := time.Now()
	isStream := op == "streamGenerateContent"
	targetPath := "/v1/chat/completions"
	if op == "embedContent" {
		targetPath = "/v1/embeddings"
	}

	if isStream {
		// 流式：实时转换转发，不缓冲（不做跨渠道故障转移），镜像 RelayHandler 流式分支
		t := targets[0]
		upstream := applyModelMapping(t.channel, modelName)
		nb, err := geminiToOpenAI(bodyBuf, upstream, true)
		if err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(nb))
		r.ContentLength = int64(len(nb))

		lastErr := error(nil)
		cw := newGeminiStreamWriter(w, maxInt64(1, int64(len(bodyBuf))/4))
		proxy := buildForwardProxy(r, t.channel.BaseURL, targetPath, t.channel.AuthType, t.channel.AuthKey, t.key)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			lastErr = err
			if !cw.wroteHeader {
				http.Error(w, "Bad Gateway: "+err.Error(), http.StatusBadGateway)
			}
		}
		proxy.ServeHTTP(cw, r)
		if lastErr == nil && !isChannelError(cw.status) {
			markChannelSuccess(t.channel.ID)
		} else {
			markChannelFailure(t.channel.ID)
		}
		cost := relayEndpointCost(targetPath, bodyBuf, make([]byte, cw.bytes), modelName, "stream")
		cost = model.ModelCost(modelName, cost, 0)
		_ = model.UseToken(key, cost)
		model.AddUserUsed(tok.Owner, cost)
		recordStats(cost)
		logAccess(map[string]interface{}{
			"time": start.Format(time.RFC3339), "method": r.Method, "path": r.URL.Path,
			"model": modelName, "group": group, "token": maskToken(key),
			"status": cw.status, "stream": true, "cost": cost,
			"duration": time.Since(start).Milliseconds(),
		})
		return
	}

	// 非流式：缓冲响应，支持跨渠道 / 多密钥故障转移
	var respBody []byte
	var respStatus int
	var respHeader http.Header
	var rawOpenAI []byte
	for _, t := range targets {
		lastErr := error(nil)
		upstream := applyModelMapping(t.channel, modelName)
		var nb []byte
		var err error
		if op == "embedContent" {
			nb, err = geminiEmbedToOpenAI(bodyBuf, upstream)
		} else {
			nb, err = geminiToOpenAI(bodyBuf, upstream, false)
		}
		if err != nil {
			break
		}
		r.Body = io.NopCloser(bytes.NewReader(nb))
		r.ContentLength = int64(len(nb))
		bw := &bufferWriter{buf: &bytes.Buffer{}}
		proxy := buildForwardProxy(r, t.channel.BaseURL, targetPath, t.channel.AuthType, t.channel.AuthKey, t.key)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			lastErr = err
		}
		proxy.ServeHTTP(bw, r)
		if lastErr != nil || isChannelError(bw.status) {
			markChannelFailure(t.channel.ID)
			continue
		}
		markChannelSuccess(t.channel.ID)
		respStatus = bw.status
		respHeader = bw.header
		rawOpenAI = bw.buf.Bytes()
		if op == "embedContent" {
			respBody = openAIEmbedToGemini(rawOpenAI)
		} else {
			respBody = openAIChatToGemini(rawOpenAI)
		}
		break
	}
	if respStatus == 0 {
		http.Error(w, "Bad Gateway: no channel succeeded", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	for k, vs := range respHeader {
		switch k {
		case "Content-Type", "Content-Length", "Content-Encoding", "Transfer-Encoding":
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(respStatus)
	_, _ = w.Write(respBody)

	cost := relayEndpointCost(targetPath, bodyBuf, rawOpenAI, modelName, "normal")
	cost = model.ModelCost(modelName, cost, 0)
	_ = model.UseToken(key, cost)
	model.AddUserUsed(tok.Owner, cost)
	recordStats(cost)
	logAccess(map[string]interface{}{
		"time": start.Format(time.RFC3339), "method": r.Method, "path": r.URL.Path,
		"model": modelName, "group": group, "token": maskToken(key),
		"status": respStatus, "stream": false, "cost": cost,
		"duration": time.Since(start).Milliseconds(),
	})
}

// parseGeminiPath 从 /v1beta/models/{model}:{op} 中提取模型名与操作名。
func parseGeminiPath(path string) (modelName, op string) {
	const prefix = "/v1beta/models/"
	if !strings.HasPrefix(path, prefix) {
		return "", ""
	}
	rest := strings.TrimPrefix(path, prefix)
	i := strings.IndexByte(rest, ':')
	if i <= 0 || i >= len(rest)-1 {
		return "", ""
	}
	return rest[:i], rest[i+1:]
}

// geminiToOpenAI 将 generateContent / streamGenerateContent 请求转换为 OpenAI chat/completions 请求体。
func geminiToOpenAI(body []byte, upstreamModel string, stream bool) ([]byte, error) {
	var g struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text       string `json:"text"`
				InlineData *struct {
					MimeType string `json:"mime_type"`
					Data     string `json:"data"`
				} `json:"inline_data"`
			} `json:"parts"`
		} `json:"contents"`
		SystemInstruction *struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
		GenerationConfig *struct {
			Temperature     *float64 `json:"temperature"`
			MaxOutputTokens int      `json:"maxOutputTokens"`
			TopP            *float64 `json:"topP"`
			StopSequences   []string `json:"stopSequences"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, err
	}

	var messages []map[string]interface{}
	if g.SystemInstruction != nil && len(g.SystemInstruction.Parts) > 0 {
		var texts []string
		for _, p := range g.SystemInstruction.Parts {
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		if len(texts) > 0 {
			messages = append(messages, map[string]interface{}{
				"role": "system", "content": strings.Join(texts, "\n"),
			})
		}
	}
	for _, c := range g.Contents {
		role := c.Role
		switch role {
		case "":
			role = "user"
		case "model":
			role = "assistant"
		case "function", "system", "tool":
			// 保留原生角色
		default:
			role = "user"
		}
		var texts []string
		for _, p := range c.Parts {
			switch {
			case p.Text != "":
				texts = append(texts, p.Text)
			case p.InlineData != nil:
				texts = append(texts, "[image]")
			}
		}
		messages = append(messages, map[string]interface{}{
			"role": role, "content": strings.Join(texts, "\n"),
		})
	}
	if len(messages) == 0 {
		messages = append(messages, map[string]interface{}{"role": "user", "content": ""})
	}

	o := map[string]interface{}{
		"model":    upstreamModel,
		"messages": messages,
	}
	if g.GenerationConfig != nil {
		if g.GenerationConfig.Temperature != nil {
			o["temperature"] = *g.GenerationConfig.Temperature
		}
		if g.GenerationConfig.MaxOutputTokens > 0 {
			o["max_tokens"] = g.GenerationConfig.MaxOutputTokens
		}
		if g.GenerationConfig.TopP != nil {
			o["top_p"] = *g.GenerationConfig.TopP
		}
		if len(g.GenerationConfig.StopSequences) > 0 {
			o["stop"] = g.GenerationConfig.StopSequences
		}
	}
	if stream {
		o["stream"] = true
	}
	return json.Marshal(o)
}

// geminiEmbedToOpenAI 将 embedContent 请求转换为 OpenAI embeddings 请求体。
func geminiEmbedToOpenAI(body []byte, upstreamModel string) ([]byte, error) {
	var g struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, err
	}
	var texts []string
	for _, p := range g.Content.Parts {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	o := map[string]interface{}{
		"model": upstreamModel,
		"input": strings.Join(texts, "\n"),
	}
	return json.Marshal(o)
}

// openAIChatToGemini 将 OpenAI chat/completions 响应转换为 generateContent 响应。
func openAIChatToGemini(body []byte) []byte {
	var r struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return body
	}
	text := ""
	finish := "STOP"
	if len(r.Choices) > 0 {
		text = r.Choices[0].Message.Content
		switch r.Choices[0].FinishReason {
		case "length":
			finish = "MAX_TOKENS"
		case "content_filter":
			finish = "SAFETY"
		}
	}
	out := map[string]interface{}{
		"candidates": []map[string]interface{}{{
			"content": map[string]interface{}{
				"role":  "model",
				"parts": []map[string]interface{}{{"text": text}},
			},
			"finishReason": finish,
		}},
		"usageMetadata": map[string]interface{}{
			"promptTokenCount":     r.Usage.PromptTokens,
			"totalTokenCount":      r.Usage.PromptTokens + r.Usage.CompletionTokens,
			"candidatesTokenCount": r.Usage.CompletionTokens,
		},
	}
	b, _ := json.Marshal(out)
	return b
}

// openAIEmbedToGemini 将 OpenAI embeddings 响应转换为 embedContent 响应。
func openAIEmbedToGemini(body []byte) []byte {
	var r struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return body
	}
	values := []float64{}
	if len(r.Data) > 0 {
		values = r.Data[0].Embedding
	}
	out := map[string]interface{}{
		"embedding": map[string]interface{}{"values": values},
	}
	b, _ := json.Marshal(out)
	return b
}

// openAIStreamChunk OpenAI SSE 流式分片。
type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

// geminiStreamWriter 拦截上游 OpenAI SSE 流，逐行转换为 Gemini SSE 流。
type geminiStreamWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	bytes       int64
	promptEst   int64
	textLen     int64
	pending     []byte
}

func newGeminiStreamWriter(w http.ResponseWriter, promptEst int64) *geminiStreamWriter {
	return &geminiStreamWriter{ResponseWriter: w, status: http.StatusOK, promptEst: promptEst}
}

func (g *geminiStreamWriter) WriteHeader(code int) {
	if !g.wroteHeader {
		g.status = code
		g.wroteHeader = true
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *geminiStreamWriter) Write(p []byte) (int, error) {
	g.bytes += int64(len(p))
	if g.status >= 400 {
		// 上游错误响应（非 SSE）：原样透传
		return g.ResponseWriter.Write(p)
	}
	data := make([]byte, 0, len(g.pending)+len(p))
	data = append(data, g.pending...)
	data = append(data, p...)
	g.pending = g.pending[:0]
	for {
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			g.pending = append(g.pending, data...)
			break
		}
		line := data[:idx]
		data = data[idx+1:]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		g.handleLine(line)
	}
	return len(p), nil
}

func (g *geminiStreamWriter) Flush() {
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *geminiStreamWriter) handleLine(line []byte) {
	payload := strings.TrimSpace(strings.TrimPrefix(string(line), "data:"))
	if payload == "" || payload == "[DONE]" {
		return
	}
	var ch openAIStreamChunk
	if json.Unmarshal([]byte(payload), &ch) != nil || len(ch.Choices) == 0 {
		return
	}
	delta := ch.Choices[0].Delta.Content
	finish := ch.Choices[0].FinishReason
	if delta == "" && finish != "stop" {
		return
	}
	g.textLen += int64(len(delta))
	out := map[string]interface{}{
		"candidates": []map[string]interface{}{{
			"content": map[string]interface{}{
				"role":  "model",
				"parts": []map[string]interface{}{{"text": delta}},
			},
		}},
	}
	if finish == "stop" {
		prompt := g.promptEst
		comp := maxInt64(1, g.textLen/4)
		if ch.Usage != nil {
			if ch.Usage.PromptTokens > 0 {
				prompt = ch.Usage.PromptTokens
			}
			if ch.Usage.CompletionTokens > 0 {
				comp = ch.Usage.CompletionTokens
			}
		}
		out["usageMetadata"] = map[string]interface{}{
			"promptTokenCount":     prompt,
			"totalTokenCount":      prompt + comp,
			"candidatesTokenCount": comp,
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	_, _ = g.ResponseWriter.Write([]byte("data: " + string(b) + "\n\n"))
}
