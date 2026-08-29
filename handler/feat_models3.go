package handler

import (
	"api-gateway/model"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/api/models/fetch", ModelsFetchHandler)
	http.HandleFunc("/api/models/add", ModelsAddHandler)
	http.HandleFunc("/api/models/rename", ModelsRenameHandler)
	http.HandleFunc("/api/models/prompt", ModelsPromptHandler)
	http.HandleFunc("/api/models/detail", ModelsDetailHandler)
}

func setModels3CORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func writeModels3Error(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// requireModelsRoot 校验管理会话且必须是 root；OPTIONS 直接放行返回 false
func requireModelsRoot(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return false
	}
	s, ok := requireSession(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	if !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// fetchUpstreamModels 请求上游 /v1/models 拉取模型 id 列表（8s 超时）
func fetchUpstreamModels(baseURL, apiKey string) ([]string, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	var ids []string
	for _, d := range out.Data {
		if id := strings.TrimSpace(d.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// ModelsFetchHandler POST /api/models/fetch {base_url, api_key} → {"models":[...]}（仅 root）
func ModelsFetchHandler(w http.ResponseWriter, r *http.Request) {
	setModels3CORS(w)
	if !requireModelsRoot(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeModels3Error(w, "Invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.BaseURL) == "" {
		writeModels3Error(w, "base_url is required")
		return
	}
	ids, err := fetchUpstreamModels(req.BaseURL, req.APIKey)
	if err != nil {
		writeModels3Error(w, "fetch failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"models": ids})
}

// ModelsAddHandler POST /api/models/add：渠道 + 虚拟模型一步创建（仅 root）
func ModelsAddHandler(w http.ResponseWriter, r *http.Request) {
	setModels3CORS(w)
	if !requireModelsRoot(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		DisplayName     string  `json:"display_name"`
		Vendor          string  `json:"vendor"`
		BaseURL         string  `json:"base_url"`
		APIKey          string  `json:"api_key"`
		Models          string  `json:"models"`
		SystemPrompt    string  `json:"system_prompt"`
		Ratio           float64 `json:"ratio"`
		AzureAPIVersion string  `json:"azure_api_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeModels3Error(w, "Invalid JSON: "+err.Error())
		return
	}
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.BaseURL = strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	req.Vendor = strings.ToLower(strings.TrimSpace(req.Vendor))
	if req.DisplayName == "" {
		writeModels3Error(w, "display_name is required")
		return
	}
	if req.BaseURL == "" {
		writeModels3Error(w, "base_url is required")
		return
	}
	switch req.Vendor {
	case "openai", "azure", "compatible":
	default:
		writeModels3Error(w, "vendor must be openai|azure|compatible")
		return
	}

	var upstream []string
	if strings.EqualFold(strings.TrimSpace(req.Models), "fetch") {
		ids, err := fetchUpstreamModels(req.BaseURL, req.APIKey)
		if err != nil {
			writeModels3Error(w, "fetch failed: "+err.Error())
			return
		}
		if len(ids) == 0 {
			writeModels3Error(w, "no models returned from upstream")
			return
		}
		upstream = ids
	} else {
		for _, m := range strings.Split(req.Models, ",") {
			if m = strings.TrimSpace(m); m != "" {
				upstream = append(upstream, m)
			}
		}
	}
	if len(upstream) == 0 {
		writeModels3Error(w, "models is required")
		return
	}
	ratio := req.Ratio
	if ratio <= 0 {
		ratio = 1
	}

	// 复用同 BaseURL 的既有渠道，否则新建
	all, _ := model.GetAllChannelsRaw()
	var ch model.Channel
	existing := false
	for _, c := range all {
		if strings.TrimRight(strings.TrimSpace(c.BaseURL), "/") == req.BaseURL {
			ch = c
			existing = true
			break
		}
	}
	if existing {
		have := map[string]bool{}
		allModels := []string{}
		for _, m := range strings.Split(ch.Models, ",") {
			if m = strings.TrimSpace(m); m != "" && !have[m] {
				have[m] = true
				allModels = append(allModels, m)
			}
		}
		changed := false
		for _, m := range upstream {
			if !have[m] {
				have[m] = true
				allModels = append(allModels, m)
				changed = true
			}
		}
		if changed {
			ch.Models = strings.Join(allModels, ",")
			if err := model.UpdateChannel(ch.ID, &ch); err != nil {
				writeModels3Error(w, "update channel failed: "+err.Error())
				return
			}
		}
	} else {
		ch = model.Channel{
			Name:            "c-" + req.DisplayName,
			Type:            req.Vendor,
			BaseURL:         req.BaseURL,
			Keys:            req.APIKey,
			AuthType:        "bearer",
			Models:          strings.Join(upstream, ","),
			Group:           "default",
			Status:          1,
			Weight:          1,
			AzureAPIVersion: req.AzureAPIVersion,
		}
		if _, err := model.InsertChannel(&ch); err != nil {
			writeModels3Error(w, "create channel failed: "+err.Error())
			return
		}
	}

	// 每个上游模型建一个虚拟模型；单个模型用 display_name，多个用上游名
	var created []map[string]string
	for _, m := range upstream {
		display := m
		if len(upstream) == 1 {
			display = req.DisplayName
		}
		if err := model.SaveVModel(model.VModel{Display: display, Upstream: m, Ratio: ratio}); err != nil {
			writeModels3Error(w, "save vmodel failed: "+err.Error())
			return
		}
		if req.SystemPrompt != "" {
			if err := model.SetVModelPrompt(display, req.SystemPrompt); err != nil {
				writeModels3Error(w, "save prompt failed: "+err.Error())
				return
			}
		}
		created = append(created, map[string]string{"display": display, "upstream": m})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "models": created})
}

// ModelsRenameHandler POST /api/models/rename {name, new_name}（仅 root）
func ModelsRenameHandler(w http.ResponseWriter, r *http.Request) {
	setModels3CORS(w)
	if !requireModelsRoot(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name    string `json:"name"`
		NewName string `json:"new_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeModels3Error(w, "Invalid JSON: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.NewName = strings.TrimSpace(req.NewName)
	if req.Name == "" || req.NewName == "" {
		writeModels3Error(w, "name and new_name are required")
		return
	}
	vm, ok := model.GetVModel(req.Name)
	if !ok {
		writeModels3Error(w, "model not found")
		return
	}
	if err := model.DelVModel(req.Name); err != nil {
		writeModels3Error(w, "delete failed: "+err.Error())
		return
	}
	vm.Display = req.NewName
	if err := model.SaveVModel(vm); err != nil {
		writeModels3Error(w, "save failed: "+err.Error())
		return
	}
	if prompt, ok := model.GetVModelPrompt(req.Name); ok {
		_ = model.SetVModelPrompt(req.NewName, prompt)
		_ = model.KVDel("vprompt." + req.Name)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

// ModelsPromptHandler POST /api/models/prompt {name, system_prompt}（仅 root）
func ModelsPromptHandler(w http.ResponseWriter, r *http.Request) {
	setModels3CORS(w)
	if !requireModelsRoot(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name         string `json:"name"`
		SystemPrompt string `json:"system_prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeModels3Error(w, "Invalid JSON: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeModels3Error(w, "name is required")
		return
	}
	if _, ok := model.GetVModel(req.Name); !ok {
		writeModels3Error(w, "model not found")
		return
	}
	if err := model.SetVModelPrompt(req.Name, req.SystemPrompt); err != nil {
		writeModels3Error(w, "save failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

// ModelsDetailHandler GET /api/models/detail?name=...（仅 root）
func ModelsDetailHandler(w http.ResponseWriter, r *http.Request) {
	setModels3CORS(w)
	if !requireModelsRoot(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeModels3Error(w, "name is required")
		return
	}
	vm, ok := model.GetVModel(name)
	if !ok {
		writeModels3Error(w, "model not found")
		return
	}
	prompt, _ := model.GetVModelPrompt(name)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"display":          vm.Display,
		"upstream":         vm.Upstream,
		"ratio":            vm.Ratio,
		"completion_ratio": vm.CompletionRatio,
		"system_prompt":    prompt,
	})
}
