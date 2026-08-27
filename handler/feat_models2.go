package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// ModelAliasHandler 处理模型别名管理接口：
//
//	GET    /api/model_aliases            读取全部别名
//	POST   /api/model_aliases            新增/更新别名（仅 root）
//	DELETE /api/model_aliases?name=...   删除别名
//	GET    /api/model_aliases/resolve?name=mygpt  查询别名映射
//
// 通过 init() 注册路由，避免修改 main.go。
func init() {
	http.HandleFunc("/api/model_aliases", ModelAliasHandler)
	http.HandleFunc("/api/model_aliases/resolve", ModelAliasHandler)
}

func setModelAliasCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// ModelAliasHandler 统一分发模型别名请求
func ModelAliasHandler(w http.ResponseWriter, r *http.Request) {
	setModelAliasCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	path := r.URL.Path
	switch {
	case path == "/api/model_aliases/resolve" && r.Method == "GET":
		modelAliasResolveHandler(w, r)
	case path == "/api/model_aliases" && r.Method == "GET":
		modelAliasListHandler(w, r)
	case path == "/api/model_aliases" && r.Method == "POST":
		modelAliasCreateHandler(w, r)
	case path == "/api/model_aliases" && r.Method == "DELETE":
		modelAliasDeleteHandler(w, r)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

// modelAliasListHandler GET /api/model_aliases
func modelAliasListHandler(w http.ResponseWriter, r *http.Request) {
	all := model.KVGetAll("alias.")
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	aliases := make(map[string]string, len(keys))
	for _, k := range keys {
		aliases[k] = all[k]
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"aliases": aliases})
}

// modelAliasCreateHandler POST /api/model_aliases {name, model}（仅 root）
func modelAliasCreateHandler(w http.ResponseWriter, r *http.Request) {
	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Model) == "" {
		http.Error(w, "name and model are required", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.Name, " \t\r\n") || strings.ContainsAny(req.Model, " \t\r\n") {
		http.Error(w, "name and model must not contain spaces", http.StatusBadRequest)
		return
	}
	if err := model.KVSet("alias."+req.Name, req.Model); err != nil {
		http.Error(w, "Save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

// modelAliasDeleteHandler DELETE /api/model_aliases?name=...
func modelAliasDeleteHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := model.KVDel("alias." + name); err != nil {
		http.Error(w, "Delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

// modelAliasResolveHandler GET /api/model_aliases/resolve?name=mygpt
func modelAliasResolveHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	modelName, ok := model.KVGet("alias." + name)
	if !ok {
		http.Error(w, "alias not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"alias": name, "model": modelName})
}

// ResolveModel 将别名解析为真实模型名；非别名原样返回。每次 relay 请求调用，保持轻量。
func ResolveModel(name string) string {
	if resolved, ok := model.KVGet("alias." + name); ok {
		return resolved
	}
	return name
}