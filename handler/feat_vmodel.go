package handler

import (
	"api-gateway/model"
	"encoding/json"
	"net/http"
	"strings"
)

// VModelHandler 管理虚拟模型（展示名伪装）：
// 展示名可随意命名、可自定义价格、可批量挂名，背后全部路由到同一个上游模型。
// GET /admin/vmodels 列出全部；POST 批量新增（display 用逗号分隔）；DELETE ?name= 删除。
func VModelHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
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
		json.NewEncoder(w).Encode(map[string]interface{}{"vmodels": model.GetVModels()})
	case "POST":
		var body struct {
			Display         string  `json:"display"`
			Upstream        string  `json:"upstream"`
			Ratio           float64 `json:"ratio"`
			CompletionRatio float64 `json:"completion_ratio"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.Upstream) == "" || strings.TrimSpace(body.Display) == "" {
			http.Error(w, "display/upstream 必填", http.StatusBadRequest)
			return
		}
		if body.Ratio <= 0 {
			body.Ratio = 1
		}
		if body.CompletionRatio <= 0 {
			body.CompletionRatio = body.Ratio
		}
		created := 0
		for _, name := range strings.Split(body.Display, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if err := model.SaveVModel(model.VModel{
				Display:         name,
				Upstream:        strings.TrimSpace(body.Upstream),
				Ratio:           body.Ratio,
				CompletionRatio: body.CompletionRatio,
			}); err == nil {
				created++
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "created": created})
	case "DELETE":
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name 必填", http.StatusBadRequest)
			return
		}
		_ = model.DelVModel(name)
		json.NewEncoder(w).Encode(map[string]string{"success": "true"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func init() {
	http.HandleFunc("/admin/vmodels", VModelHandler)
}