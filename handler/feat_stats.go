package handler

import (
	"api-gateway/model"
	"bufio"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxStatsLines 聚合统计时最多解析的 access.log 行数，防止超大文件拖垮接口
const maxStatsLines = 100000

// statsCount 统计一个分组（模型 / 渠道 / 用户）的请求数与花费
type statsCount struct {
	Requests int64
	Cost     int64
}

// statsModel 按模型聚合的结果项
type statsModel struct {
	Model    string `json:"model"`
	Requests int64  `json:"requests"`
	Cost     int64  `json:"cost"`
}

// statsChannel 按上游渠道聚合的结果项
type statsChannel struct {
	Channel  string `json:"channel"`
	Requests int64  `json:"requests"`
	Cost     int64  `json:"cost"`
}

// statsUser 按令牌聚合的结果项
type statsUser struct {
	Token    string `json:"token"`
	Requests int64  `json:"requests"`
	Cost     int64  `json:"cost"`
}

// statsResult GET /api/stats 返回的聚合结果
type statsResult struct {
	TotalRequests int64                    `json:"total_requests"`
	TodayRequests int64                    `json:"today_requests"`
	TotalCost     int64                    `json:"total_cost"`
	TodayCost     int64                    `json:"today_cost"`
	TotalTokens   int64                    `json:"total_tokens"`
	ByModel       []statsModel             `json:"by_model"`
	ByChannel     []statsChannel           `json:"by_channel"`
	ByUser        []statsUser              `json:"by_user"`
	StatusCodes   map[string]int64         `json:"status_codes"`
	ByError       []statsError             `json:"by_error"`
	ByDay         []statsDay               `json:"by_day"`
	Trend         []statsDay               `json:"trend"`
	Recent        []map[string]interface{} `json:"recent"`
}

// statsError 按 HTTP 状态码聚合的结果项
type statsError struct {
	Status int   `json:"status"`
	Count  int64 `json:"count"`
}

// statsDay 按日期聚合的结果项
type statsDay struct {
	Day      string `json:"day"`
	Requests int64  `json:"requests"`
	Cost     int64  `json:"cost"`
}

func init() {
	http.HandleFunc("/api/stats", StatsHandler)
	http.HandleFunc("/api/stats/export", StatsExportHandler)
}

// setStatsCORS 设置跨域头并处理 OPTIONS 预检，返回 true 表示已处理完
func setStatsCORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

// loadAccessEntries 读取 data/access.log（JSONL）。
// maxLines <= 0 表示不设上限（CSV 导出用）；文件不存在时返回 nil,nil。
func loadAccessEntries(maxLines int) ([]map[string]interface{}, error) {
	path := filepath.Join(model.DataDir(), "access.log")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []map[string]interface{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e map[string]interface{}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		out = append(out, e)
		if maxLines > 0 && len(out) >= maxLines {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// statsToInt 把 JSON 数值字段安全转成 int64（兼容 float64 / int / string）
func statsToInt(v interface{}) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case float32:
		return int64(t)
	case int:
		return int64(t)
	case int64:
		return t
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n
		}
	case string:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// statsToStr 把 JSON 任意字段转成字符串用于 CSV / 分组
func statsToStr(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return ""
}

// statsTop10 按花费降序排序，返回前 10 项
func statsTop10(costs []int64, names []string, counts []int64) ([]int64, []string, []int64) {
	if len(names) == 0 {
		return nil, nil, nil
	}
	idx := make([]int, len(names))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(i, j int) bool {
		a, b := idx[i], idx[j]
		if costs[a] != costs[b] {
			return costs[a] > costs[b]
		}
		if counts[a] != counts[b] {
			return counts[a] > counts[b]
		}
		return names[a] < names[b]
	})
	if len(idx) > 10 {
		idx = idx[:10]
	}
	outCosts := make([]int64, len(idx))
	outNames := make([]string, len(idx))
	outCounts := make([]int64, len(idx))
	for i, j := range idx {
		outCosts[i] = costs[j]
		outNames[i] = names[j]
		outCounts[i] = counts[j]
	}
	return outCosts, outNames, outCounts
}

// statsDayOf 从日志 time 字段提取日期（YYYY-MM-DD），无法解析返回空串
func statsDayOf(t string) string {
	if d, err := time.Parse("2006-01-02", t); err == nil {
		return d.Format("2006-01-02")
	}
	if len(t) >= 10 {
		if d, err := time.Parse("2006-01-02", t[:10]); err == nil {
			return d.Format("2006-01-02")
		}
	}
	return ""
}

// computeStats 把解析出的日志行聚合成统计结果
func computeStats(entries []map[string]interface{}) *statsResult {
	res := &statsResult{
		StatusCodes: map[string]int64{},
		Recent:      []map[string]interface{}{},
		ByModel:     []statsModel{},
		ByChannel:   []statsChannel{},
		ByUser:      []statsUser{},
		ByError:     []statsError{},
		ByDay:       []statsDay{},
		Trend:       []statsDay{},
	}
	modelIdx := map[string]int{}
	channelIdx := map[string]int{}
	userIdx := map[string]int{}
	errIdx := map[int64]int64{}
	dayIdx := map[string]*statsDay{}
	var mNames []string
	var mCosts []int64
	var mCounts []int64
	var cNames []string
	var cCosts []int64
	var cCounts []int64
	var uNames []string
	var uCosts []int64
	var uCounts []int64

	today := time.Now().Format("2006-01-02")

	for _, e := range entries {
		cost := statsToInt(e["cost"])
		res.TotalRequests++
		res.TotalCost += cost
		res.TotalTokens += cost

		t := statsToStr(e["time"])
		if strings.HasPrefix(t, today) {
			res.TodayRequests++
			res.TodayCost += cost
		}

		if m := statsToStr(e["model"]); m != "" {
			i, ok := modelIdx[m]
			if !ok {
				i = len(mNames)
				modelIdx[m] = i
				mNames = append(mNames, m)
				mCosts = append(mCosts, 0)
				mCounts = append(mCounts, 0)
			}
			mCosts[i] += cost
			mCounts[i]++
		}

		if c := statsToStr(e["upstream"]); c != "" {
			i, ok := channelIdx[c]
			if !ok {
				i = len(cNames)
				channelIdx[c] = i
				cNames = append(cNames, c)
				cCosts = append(cCosts, 0)
				cCounts = append(cCounts, 0)
			}
			cCosts[i] += cost
			cCounts[i]++
		}

		if tok := statsToStr(e["token"]); tok != "" {
			i, ok := userIdx[tok]
			if !ok {
				i = len(uNames)
				userIdx[tok] = i
				uNames = append(uNames, tok)
				uCosts = append(uCosts, 0)
				uCounts = append(uCounts, 0)
			}
			uCosts[i] += cost
			uCounts[i]++
		}

		if st := statsToStr(e["status"]); st != "" {
			res.StatusCodes[st]++
			errIdx[statsToInt(e["status"])]++
		}

		if day := statsDayOf(t); day != "" {
			d := dayIdx[day]
			if d == nil {
				d = &statsDay{Day: day}
				dayIdx[day] = d
			}
			d.Requests++
			d.Cost += cost
		}
	}

	// 最近 20 条，最新在前
	for i := len(entries) - 1; i >= 0 && len(res.Recent) < 20; i-- {
		res.Recent = append(res.Recent, entries[i])
	}

	mCosts, mNames, mCounts = statsTop10(mCosts, mNames, mCounts)
	for i := range mNames {
		res.ByModel = append(res.ByModel, statsModel{Model: mNames[i], Requests: mCounts[i], Cost: mCosts[i]})
	}

	cCosts, cNames, cCounts = statsTop10(cCosts, cNames, cCounts)
	for i := range cNames {
		res.ByChannel = append(res.ByChannel, statsChannel{Channel: cNames[i], Requests: cCounts[i], Cost: cCosts[i]})
	}

	uCosts, uNames, uCounts = statsTop10(uCosts, uNames, uCounts)
	for i := range uNames {
		res.ByUser = append(res.ByUser, statsUser{Token: uNames[i], Requests: uCounts[i], Cost: uCosts[i]})
	}

	// 按状态码升序输出
	var errCodes []int
	for code := range errIdx {
		errCodes = append(errCodes, int(code))
	}
	sort.Ints(errCodes)
	for _, c := range errCodes {
		res.ByError = append(res.ByError, statsError{Status: c, Count: errIdx[int64(c)]})
	}

	// 最近 14 天，不足的日期补零
	now := time.Now()
	for i := 13; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		if d, ok := dayIdx[day]; ok {
			res.ByDay = append(res.ByDay, *d)
		} else {
			res.ByDay = append(res.ByDay, statsDay{Day: day})
		}
	}
	res.Trend = res.ByDay

	return res
}

// StatsHandler GET /api/stats 返回 access.log 的用量统计（仅 root）
func StatsHandler(w http.ResponseWriter, r *http.Request) {
	if setStatsCORS(w, r) {
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, ok := requireSession(r)
	if !ok || !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	entries, err := loadAccessEntries(maxStatsLines)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(computeStats(entries))
}

// StatsExportHandler GET /api/stats/export 导出全部 access.log 为 CSV（仅 root）
func StatsExportHandler(w http.ResponseWriter, r *http.Request) {
	if setStatsCORS(w, r) {
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, ok := requireSession(r)
	if !ok || !model.IsRoot(s.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	entries, err := loadAccessEntries(0)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="access.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"time", "method", "path", "group", "token", "model", "status", "stream", "cost", "duration"})
	for _, e := range entries {
		cw.Write([]string{
			statsToStr(e["time"]),
			statsToStr(e["method"]),
			statsToStr(e["path"]),
			statsToStr(e["group"]),
			statsToStr(e["token"]),
			statsToStr(e["model"]),
			statsToStr(e["status"]),
			statsToStr(e["stream"]),
			strconv.FormatInt(statsToInt(e["cost"]), 10),
			strconv.FormatInt(statsToInt(e["duration"]), 10),
		})
	}
	cw.Flush()
	_ = cw.Error()
}
