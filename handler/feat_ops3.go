package handler

import (
	"api-gateway/model"
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ModelConflictsHandler GET /admin/modelconflicts（root）
// 检测被 别名/重定向/虚拟模型/固定价格 引用、但没有任何渠道支持的模型名。
func ModelConflictsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
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
	supported := map[string]bool{}
	all, _ := model.GetAllChannelsRaw()
	for _, c := range all {
		if c.Models == "" || c.Models == "*" {
			continue
		}
		for _, m := range strings.Split(c.Models, ",") {
			if m = strings.TrimSpace(m); m != "" {
				supported[m] = true
			}
		}
	}
	referenced := map[string]string{} // name -> source
	for k, v := range model.KVGetAll("alias.") {
		referenced[k] = "alias → " + v
	}
	for k, v := range model.KVGetAll("redirect.") {
		referenced[k] = "redirect → " + v
	}
	for k, v := range model.KVGetAll("redirect_re.") {
		referenced[k] = "redirect_re → " + v
	}
	for _, vm := range model.GetVModels() {
		referenced[vm.Upstream] = "vmodel(" + vm.Display + ") → " + vm.Upstream
	}
	missing := []map[string]string{}
	for name, src := range referenced {
		if !supported[name] {
			missing = append(missing, map[string]string{"model": name, "source": src})
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i]["model"] < missing[j]["model"] })
	json.NewEncoder(w).Encode(map[string]interface{}{
		"missing_models": missing,
		"all_models":     keysOf(supported),
	})
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ReplayHandler POST /api/admin/replay（root）
// 按日志时间找出那条请求，返回其保存的请求体（req_preview），供工具重放。
func ReplayHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
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
	var body struct {
		Time string `json:"time"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Time == "" {
		http.Error(w, "time 必填", http.StatusBadRequest)
		return
	}
	f, err := os.Open(filepath.Join(model.DataDir(), "access.log"))
	if err != nil {
		http.Error(w, "access.log 不存在", http.StatusNotFound)
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e map[string]interface{}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if t, _ := e["time"].(string); t != body.Time {
			continue
		}
		req, _ := e["req_preview"].(string)
		if req == "" {
			json.NewEncoder(w).Encode(map[string]interface{}{"replay": false, "error": "该日志未保存请求体"})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"replay": true, "request": json.RawMessage(req)})
		return
	}
	http.Error(w, "未找到对应日志", http.StatusNotFound)
}

func init() {
	http.HandleFunc("/admin/modelconflicts", ModelConflictsHandler)
	http.HandleFunc("/api/admin/replay", ReplayHandler)
}