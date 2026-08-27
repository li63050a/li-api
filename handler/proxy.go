package handler

import (
	"api-gateway/cache"
	"api-gateway/model"
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
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
		http.Error(w, "No matching route", http.StatusNotFound)
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
			http.Error(w, "Path not allowed", http.StatusForbidden)
			return
		}
	}

	// 3. 入站令牌校验（用户侧 Key，可带额度）
	if route.NeedAPIKey {
		if err := model.CheckAndUse(extractToken(r)); err != nil {
			switch err {
			case model.ErrTokenInvalid:
				http.Error(w, "Invalid API Key", http.StatusUnauthorized)
			case model.ErrQuotaExceeded:
				http.Error(w, "Quota Exceeded", http.StatusForbidden)
			default:
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
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
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
	}

	// 5. 选取起始上游密钥（轮询），并准备故障转移
	keys := route.Keys()
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

	var attempt int
	var serve func(curKey string)
	serve = func(curKey string) {
		proxy := buildProxy(route, curKey, targetPath, r)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			// 上游连接失败且仍有备用密钥时，自动故障转移到下一个
			attempt++
			if len(keys) > 1 && attempt < len(keys) {
				next := keys[(startIdx+attempt)%len(keys)]
				serve(next)
				return
			}
			http.Error(w, "Bad Gateway: "+err.Error(), http.StatusBadGateway)
		}
		proxy.ServeHTTP(w, r.WithContext(ctx))
	}

	first := ""
	if len(keys) > 0 {
		first = keys[startIdx%len(keys)]
	}
	serve(first)
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

// buildProxy 为指定上游密钥构建一个反向代理
func buildProxy(route *model.Route, key, targetPath string, orig *http.Request) *httputil.ReverseProxy {
	remaining := strings.TrimPrefix(targetPath, route.Prefix)
	fullURL := route.UpstreamURL + remaining
	if orig.URL.RawQuery != "" {
		fullURL += "?" + orig.URL.RawQuery
	}
	parsed, err := url.Parse(fullURL)
	if err != nil {
		// 解析失败时返回一个直接报错的代理
		return &httputil.ReverseProxy{
			Director: func(*http.Request) {},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, e error) {
				http.Error(w, "Invalid upstream URL", http.StatusInternalServerError)
			},
		}
	}

	proxy := httputil.NewSingleHostReverseProxy(parsed)
	proxy.Transport = sharedTransport
	proxy.FlushInterval = 100 * time.Millisecond // 支持 SSE / 流式响应边收边发

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
		// 注入上游认证
		switch route.AuthType {
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+key)
		case "header":
			req.Header.Set(route.AuthKey, key)
		case "query":
			q := req.URL.Query()
			q.Set(route.AuthKey, key)
			req.URL.RawQuery = q.Encode()
		}
	}
	return proxy
}
