package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
)

// ModelsHandler 处理模型价格管理相关接口：
//
//	GET  /api/feat/models          读取价格表（任意登录用户）
//	PUT  /api/feat/models          全量替换保存（仅 root）
//	GET  /api/feat/models/export   导出 JSON 文件下载（任意登录用户）
//	POST /api/feat/models/import   合并导入并保存（仅 root）
//
// 通过 init() 注册路由，避免修改 main.go。
func init() {
	http.HandleFunc("/api/feat/models", ModelsHandler)
	http.HandleFunc("/api/feat/models/export", ModelsHandler)
	http.HandleFunc("/api/feat/models/import", ModelsHandler)
}

func setModelCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// ModelsHandler 统一分发模型价格管理请求
func ModelsHandler(w http.ResponseWriter, r *http.Request) {
	setModelCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	path := r.URL.Path
	switch {
	case path == "/api/feat/models/export" && r.Method == "GET":
		modelsExportHandler(w, r)
	case path == "/api/feat/models/import" && r.Method == "POST":
		modelsImportHandler(w, r)
	case path == "/api/feat/models" && r.Method == "GET":
		modelsGetHandler(w, r)
	case path == "/api/feat/models" && r.Method == "PUT":
		modelsPutHandler(w, r)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

// modelsGetHandler GET /api/feat/models
func modelsGetHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSession(r); !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(model.GetModelPrices())
}

// modelsPutHandler PUT /api/feat/models 全量替换（仅 root）
func modelsPutHandler(w http.ResponseWriter, r *http.Request) {
	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	var m model.ModelPrices
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if m == nil {
		m = model.ModelPrices{}
	}
	if err := model.SaveModelPrices(m); err != nil {
		http.Error(w, "Save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

// modelsExportHandler GET /api/feat/models/export 文件下载
func modelsExportHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSession(r); !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	prices := model.GetModelPrices()
	data, err := json.MarshalIndent(prices, "", "  ")
	if err != nil {
		http.Error(w, "Export failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=model_prices.json")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// modelsImportHandler POST /api/feat/models/import 合并导入（仅 root）
func modelsImportHandler(w http.ResponseWriter, r *http.Request) {
	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	var incoming model.ModelPrices
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	current := model.GetModelPrices()
	if current == nil {
		current = model.ModelPrices{}
	}
	for k, v := range incoming {
		current[k] = v
	}
	if err := model.SaveModelPrices(current); err != nil {
		http.Error(w, "Save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}
