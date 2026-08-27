package handler

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"net/http"
)

// bufferWriter 缓冲上游响应（用于非流式故障转移，不立即写回客户端）
type bufferWriter struct {
	status int
	header http.Header
	buf    *bytes.Buffer
}

func (b *bufferWriter) Header() http.Header {
	if b.header == nil {
		b.header = make(http.Header)
	}
	return b.header
}

func (b *bufferWriter) WriteHeader(c int) {
	if b.status == 0 {
		b.status = c
	}
}

func (b *bufferWriter) Write(p []byte) (int, error) {
	return b.buf.Write(p)
}

func (b *bufferWriter) Flush() {}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// parseUsage 从响应体解析已消耗的 token 数（优先 total_tokens）
func parseUsage(body []byte) int64 {
	var r struct {
		Usage struct {
			TotalTokens       int64 `json:"total_tokens"`
			PromptTokens      int64 `json:"prompt_tokens"`
			CompletionTokens  int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0
	}
	if r.Usage.TotalTokens > 0 {
		return r.Usage.TotalTokens
	}
	return r.Usage.PromptTokens + r.Usage.CompletionTokens
}

// parseUsageParts 分别解析 prompt / completion 的 token 数（用于分倍率计费）
func parseUsageParts(body []byte) (prompt, completion int64) {
	var r struct {
		Usage struct {
			TotalTokens      int64 `json:"total_tokens"`
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, 0
	}
	return r.Usage.PromptTokens, r.Usage.CompletionTokens
}

func randInt(n int) int {
	if n <= 0 {
		return 0
	}
	return rand.Intn(n)
}
