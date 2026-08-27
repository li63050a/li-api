package cache

import (
    "api-gateway/model"
    "sync"
)

var (
    routeCache sync.Map
    cacheMu    sync.RWMutex
)

// Refresh 刷新缓存
func Refresh() error {
    routes, err := model.GetAllEnabled()
    if err != nil {
        return err
    }
    newMap := &sync.Map{}
    for i := range routes {
        r := routes[i]
        newMap.Store(r.Prefix, &r)
    }
    cacheMu.Lock()
    routeCache = *newMap
    cacheMu.Unlock()
    return nil
}

// GetRoute 根据路径前缀匹配路由（返回 *model.Route 和是否匹配）
func GetRoute(path string) (*model.Route, bool) {
    cacheMu.RLock()
    defer cacheMu.RUnlock()
    var matched *model.Route
    // 因为 routeCache 是 sync.Map，需要遍历或使用 Range
    // 为了性能，我们遍历所有前缀，取最长匹配（或第一个匹配）
    // 简单起见，我们直接用 Range 找到第一个匹配的前缀
    // 但如果前缀有包含关系（如 /v1 和 /v1/chat），我们希望更长前缀优先
    // 可以改存储方式，这里简单遍历，记录最长匹配
    var longest string
    routeCache.Range(func(key, value interface{}) bool {
        prefix := key.(string)
        if len(prefix) > len(longest) && len(path) >= len(prefix) && path[:len(prefix)] == prefix {
            longest = prefix
            matched = value.(*model.Route)
        }
        return true
    })
    if matched == nil {
        return nil, false
    }
    return matched, true
}