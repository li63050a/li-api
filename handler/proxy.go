package handler

import (
	"api-gateway/cache"
	"api-gateway/model"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// sharedTransport 全局复用连接，避免每次请求新建 Transport 导致连接泄漏与内存上涨
var sharedTransport = &http.Transport{
	MaxIdleConns:        200,
	MaxIdleConnsPerHost: 100,
	IdleConnTimeout:     90 * time.Second,
}

// 每个路由的限流器缓存（按 route id）
var limiters = make(map[int]*rate.Limiter)
var limiterMu sync.RWMutex

// 每个路由的上游密钥轮询计数器
var keyCounters sync.Map // map[int]*uint64

// 访问日志（JSONL，写入 data/access.log）
var (
	logMu sync.Mutex
	logF  *os.File
)

// statusRecorder 记录响应状态码，同时透传 Flush 以支持流式
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(c int) {
	if !s.wrote {
		s.status = c
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(c)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logAccess 追加一条访问日志到 data/access.log
func logAccess(entry map[string]interface{}) {
	logMu.Lock()
	defer logMu.Unlock()
	if logF == nil {
		f, err := os.OpenFile(filepath.Join(model.DataDir(), "access.log"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		logF = f
	}
	b, _ := json.Marshal(entry)
	_, _ = logF.Write(append(b, '\n'))
	NotifyRequestCompletion(entry)
}

// ProxyHandler 处理所有 /proxy/ 前缀的请求
func ProxyHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if !strings.HasPrefix(path, "/proxy/") {
		http.NotFound(w, r)
		return
	}
	targetPath := strings.TrimPrefix(path, "/proxy")

	// 1. 匹配路由
	route, ok := cache.GetRoute(targetPath)
	if !ok {
		writeError(w, http.StatusNotFound, "No matching route", "invalid_request_error")
		return
	}

	// 2. 下游路径白名单
	if route.AllowedPaths != "" {
		allowed := strings.Split(route.AllowedPaths, ",")
		allowedMap := make(map[string]bool)
		for _, p := range allowed {
			allowedMap[strings.TrimSpace(p)] = true
		}
		if !allowedMap[targetPath] {
			writeError(w, http.StatusForbidden, "Path not allowed", "invalid_request_error")
			return
		}
	}

	// 3. 入站令牌校验（用户侧 Key，可带额度）
	if route.NeedAPIKey {
		if err := model.CheckAndUse(extractToken(r)); err != nil {
			switch err {
			case model.ErrTokenInvalid:
				writeError(w, http.StatusUnauthorized, "Invalid API Key", "authentication_error")
			case model.ErrQuotaExceeded:
				writeError(w, http.StatusForbidden, "Quota Exceeded", "insufficient_quota")
			default:
				writeError(w, http.StatusUnauthorized, "Unauthorized", "authentication_error")
			}
			return
		}
	}

	// 4. 限流（按 route ID，rate_limit 为每分钟次数，允许突发到上限）
	if route.RateLimit > 0 {
		limiterMu.Lock()
		lim, exists := limiters[route.ID]
		if !exists {
			lim = rate.NewLimiter(rate.Limit(route.RateLimit)/60.0, route.RateLimit)
			limiters[route.ID] = lim
		}
		limiterMu.Unlock()
		if !lim.Allow() {
			writeError(w, http.StatusTooManyRequests, "Too Many Requests", "rate_limit_error")
			return
		}
	}

	// 5. 选取起始上游密钥（轮询），并准备故障转移
	keys := route.Keys()
	remaining := strings.TrimPrefix(targetPath, route.Prefix)
	var startIdx int
	if len(keys) > 1 {
		v, _ := keyCounters.LoadOrStore(route.ID, new(uint64))
		c := v.(*uint64)
		startIdx = int(atomic.AddUint64(c, 1) - 1)
	}

	timeout := time.Duration(route.Timeout) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	start := time.Now()

	var tries int
	var serve func(curKey string)
	serve = func(curKey string) {
		tries++
		proxy := buildForwardProxy(r, route.UpstreamURL, remaining, route.AuthType, route.AuthKey, curKey)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			// 上游连接失败且仍有备用密钥时，自动故障转移到下一个
			if len(keys) > 1 && tries < len(keys) {
				next := keys[(startIdx+tries)%len(keys)]
				serve(next)
				return
			}
			writeError(w, http.StatusBadGateway, "Bad Gateway: "+err.Error(), "server_error")
		}
		proxy.ServeHTTP(rw, r.WithContext(ctx))
	}

	first := ""
	if len(keys) > 0 {
		first = keys[startIdx%len(keys)]
	}
	serve(first)

	logAccess(map[string]interface{}{
		"time":     start.Format(time.RFC3339),
		"method":   r.Method,
		"path":     targetPath,
		"route":    route.Prefix,
		"token":    maskToken(extractToken(r)),
		"status":   rw.status,
		"tries":    tries,
		"upstream": route.UpstreamURL,
		"duration": time.Since(start).Milliseconds(),
	})
}

// maskToken 仅保留令牌前后各 4 位，便于审计又不泄露完整密钥
func maskToken(t string) string {
	if len(t) <= 8 {
		return "****"
	}
	return t[:4] + "****" + t[len(t)-4:]
}

// extractToken 从请求中提取用户令牌（支持 X-API-Key 或 Authorization: Bearer）
func extractToken(r *http.Request) string {
	if k := r.Header.Get("X-API-Key"); k != "" {
		return k
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	return ""
}
