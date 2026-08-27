package handler

import (
    "api-gateway/cache"
    "api-gateway/model"
    "encoding/json"
    "net/http"
    "strconv"
    "strings"
)

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

    // 管理会话校验
    if _, ok := requireSession(r); !ok {
        http.Error(w, "Forbidden", http.StatusForbidden)
        return
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
// SettingHandler 处理 /api/setting 的读取与更新（仅 root）
func SettingHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	s, ok := requireSession(r)
	if !ok || !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	switch r.Method {
	case "GET":
		json.NewEncoder(w).Encode(model.GetSetting())
	case "PUT":
		var patch model.Setting
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(model.UpdateSetting(patch))
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// ChannelHandler 处理 /admin/channels 的 CRUD（仿 new-api 的渠道管理）
func ChannelHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, ok := requireSession(r); !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/admin/channels")
	parts := strings.Split(path, "/")
	idStr := ""
	if len(parts) >= 2 {
		idStr = strings.Trim(parts[1], "/")
	}

	switch r.Method {
	case "GET":
		if idStr == "" {
			chans, err := model.GetAllChannelsRaw()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(chans)
			return
		}
		http.NotFound(w, r)

	case "POST":
		var c model.Channel
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if _, err := model.InsertChannel(&c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(c)

	case "PUT":
		if idStr == "" {
			http.Error(w, "Missing ID", http.StatusBadRequest)
			return
		}
		id, err := strconv.Atoi(idStr)
		if err != nil {
			http.Error(w, "Invalid ID", http.StatusBadRequest)
			return
		}
		var c model.Channel
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := model.UpdateChannel(id, &c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(c)

	case "DELETE":
		if idStr == "" {
			http.Error(w, "Missing ID", http.StatusBadRequest)
			return
		}
		id, err := strconv.Atoi(idStr)
		if err != nil {
			http.Error(w, "Invalid ID", http.StatusBadRequest)
			return
		}
		if err := model.DeleteChannel(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
