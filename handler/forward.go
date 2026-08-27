package handler

import (
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// buildForwardProxy 构建一个反向代理：把请求转发到 baseURL + targetPath，
// 并注入指定的上游密钥（authType: bearer / header / query）
func buildForwardProxy(orig *http.Request, baseURL, targetPath, authType, authKey, key string) *httputil.ReverseProxy {
	fullURL := baseURL + targetPath
	if orig.URL.RawQuery != "" {
		fullURL += "?" + orig.URL.RawQuery
	}
	parsed, err := url.Parse(fullURL)
	if err != nil {
		return &httputil.ReverseProxy{
			Director:    func(*http.Request) {},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, e error) {
				http.Error(w, "Invalid upstream URL", http.StatusInternalServerError)
			},
		}
	}

	proxy := httputil.NewSingleHostReverseProxy(parsed)
	proxy.Transport = sharedTransport
	proxy.FlushInterval = 100 * time.Millisecond // 支持 SSE / 流式响应

	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = parsed.Scheme
		req.URL.Host = parsed.Host
		req.URL.Path = parsed.Path
		req.URL.RawQuery = parsed.RawQuery
		req.Host = parsed.Host
		// 复制原始 Header
		for k, v := range orig.Header {
			req.Header[k] = v
		}
		// 清除用户自身的凭据，避免泄漏给上游
		req.Header.Del("X-API-Key")
		req.Header.Del("Authorization")
		// 注入上游认证
		switch authType {
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+key)
		case "header":
			req.Header.Set(authKey, key)
		case "query":
			q := req.URL.Query()
			q.Set(authKey, key)
			req.URL.RawQuery = q.Encode()
		}
	}
	return proxy
}

// countWriter 在透传响应体的同时统计字节数，并记录状态码（支持流式 Flush）
type countWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	bytes       int64
}

func newCountWriter(w http.ResponseWriter) *countWriter {
	return &countWriter{ResponseWriter: w, status: http.StatusOK}
}

func (c *countWriter) WriteHeader(code int) {
	if !c.wroteHeader {
		c.status = code
		c.wroteHeader = true
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *countWriter) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.status = http.StatusOK
		c.wroteHeader = true
	}
	n, err := c.ResponseWriter.Write(b)
	c.bytes += int64(n)
	return n, err
}

func (c *countWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ensure countWriter satisfies interfaces used by httputil
var _ io.Writer = (*countWriter)(nil)
var _ http.Flusher = (*countWriter)(nil)
