package handler

import (
    "api-gateway/cache"
    "api-gateway/model"
    "encoding/json"
    "net/http"
    "os"
    "strconv"
    "strings"
)

// adminToken 返回管理口令（未设置则返回空）
func adminToken() string {
    return os.Getenv("ADMIN_TOKEN")
}

// AdminHandler 处理 /admin/routes 的 CRUD
func AdminHandler(w http.ResponseWriter, r *http.Request) {
    // 设置 CORS（方便前端调试）
    w.Header().Set("Access-Control-Allow-Origin", "*")
    w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
    w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

    if r.Method == "OPTIONS" {
        w.WriteHeader(http.StatusOK)
        return
    }

    // 管理口令校验（设置了 ADMIN_TOKEN 才生效）
    if token := adminToken(); token != "" {
        if r.URL.Query().Get("token") != token && r.Header.Get("X-Admin-Token") != token {
            http.Error(w, "Forbidden", http.StatusForbidden)
            return
        }
    }

    // 解析路径，支持 /admin/routes 和 /admin/routes/{id}
    path := strings.TrimPrefix(r.URL.Path, "/admin/routes")
    parts := strings.Split(path, "/")
    // parts[0] 可能是空字符串或数字

    switch r.Method {
    case "GET":
        if len(parts) == 1 || parts[1] == "" {
            // 列表（含禁用，方便管理）
            routes, err := model.GetAll()
            if err != nil {
                http.Error(w, err.Error(), http.StatusInternalServerError)
                return
            }
            json.NewEncoder(w).Encode(routes)
            return
        }
        // 单个获取（暂不实现）
        http.NotFound(w, r)

    case "POST":
        // 新增
        var route model.Route
        if err := json.NewDecoder(r.Body).Decode(&route); err != nil {
            http.Error(w, "Invalid JSON", http.StatusBadRequest)
            return
        }
        if _, err := model.Insert(&route); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        // 刷新缓存
        cache.Refresh()
        w.WriteHeader(http.StatusCreated)
        json.NewEncoder(w).Encode(route)

    case "PUT":
        // 更新，需要 id
        if len(parts) < 2 || parts[1] == "" {
            http.Error(w, "Missing ID", http.StatusBadRequest)
            return
        }
        id, err := strconv.Atoi(parts[1])
        if err != nil {
            http.Error(w, "Invalid ID", http.StatusBadRequest)
            return
        }
        var route model.Route
        if err := json.NewDecoder(r.Body).Decode(&route); err != nil {
            http.Error(w, "Invalid JSON", http.StatusBadRequest)
            return
        }
        if err := model.Update(id, &route); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        cache.Refresh()
        json.NewEncoder(w).Encode(route)

    case "DELETE":
        if len(parts) < 2 || parts[1] == "" {
            http.Error(w, "Missing ID", http.StatusBadRequest)
            return
        }
        id, err := strconv.Atoi(parts[1])
        if err != nil {
            http.Error(w, "Invalid ID", http.StatusBadRequest)
            return
        }
        if err := model.Delete(id); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        // 清理限流器（可选）
        limiterMu.Lock()
        delete(limiters, id)
        limiterMu.Unlock()
        cache.Refresh()
        w.WriteHeader(http.StatusNoContent)

    default:
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
    }
}