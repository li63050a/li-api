package handler

import (
    "api-gateway/cache"
    "context"
    "net/http"
    "net/http/httputil"
    "net/url"
    "strings"
    "sync"
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

// ProxyHandler 处理所有 /proxy/ 前缀的请求
func ProxyHandler(w http.ResponseWriter, r *http.Request) {
    path := r.URL.Path
    // 去掉 /proxy/ 前缀
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

    // 2. 检查下游路径白名单
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

    // 3. 下游 API Key 校验
    if route.NeedAPIKey {
        key := r.Header.Get("X-API-Key")
        if key == "" {
            http.Error(w, "Missing X-API-Key", http.StatusUnauthorized)
            return
        }
        // 此处可扩展为从数据库验证 key 合法性
        _ = key
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

    // 5. 构建上游 URL
    remaining := strings.TrimPrefix(targetPath, route.Prefix)
    fullURL := route.UpstreamURL + remaining
    if r.URL.RawQuery != "" {
        fullURL += "?" + r.URL.RawQuery
    }
    parsed, err := url.Parse(fullURL)
    if err != nil {
        http.Error(w, "Invalid upstream URL", http.StatusInternalServerError)
        return
    }

    // 6. 创建反向代理（复用全局 Transport，开启流式刷新）
    proxy := httputil.NewSingleHostReverseProxy(parsed)
    proxy.Transport = sharedTransport
    proxy.FlushInterval = 100 * time.Millisecond // 支持 SSE / 流式响应边收边发

    // 定制 Director
    proxy.Director = func(req *http.Request) {
        req.URL.Scheme = parsed.Scheme
        req.URL.Host = parsed.Host
        req.URL.Path = parsed.Path
        req.URL.RawQuery = parsed.RawQuery
        req.Host = parsed.Host
        // 复制原始 Header
        for k, v := range r.Header {
            req.Header[k] = v
        }
        // 注入上游认证
        switch route.AuthType {
        case "bearer":
            req.Header.Set("Authorization", "Bearer "+route.AuthValue)
        case "header":
            req.Header.Set(route.AuthKey, route.AuthValue)
        case "query":
            q := req.URL.Query()
            q.Set(route.AuthKey, route.AuthValue)
            req.URL.RawQuery = q.Encode()
        }
    }

    // 超时控制（通过请求 context，避免为每次请求新建 Transport）
    timeout := time.Duration(route.Timeout) * time.Second
    if timeout == 0 {
        timeout = 30 * time.Second
    }
    ctx, cancel := context.WithTimeout(r.Context(), timeout)
    defer cancel()

    // 7. 直接转发（如果需要统一包装响应，可在此扩展）
    proxy.ServeHTTP(w, r.WithContext(ctx))
}
