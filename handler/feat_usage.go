package handler

import (
	"api-gateway/model"
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// UsageHandler GET /api/feat/usage
// 聚合 access.log（JSONL）中的用量统计：今日总 Token、各用户用量、热门模型 Top10。
// 通过 init() 注册路由，避免修改 main.go。
func init() {
	http.HandleFunc("/api/feat/usage", UsageHandler)
}

var usageMu sync.RWMutex
var usageCache interface{}
var usageCacheAt time.Time

type usageUserEntry struct {
	Username string `json:"username"`
	Used     int64  `json:"used"`
}

type usageModelEntry struct {
	Model  string `json:"model"`
	Tokens int64  `json:"tokens"`
}

type usageResult struct {
	TodayTokens int64             `json:"today_tokens"`
	PerUser     []usageUserEntry  `json:"per_user"`
	TopModels   []usageModelEntry `json:"top_models"`
}

// logTimeLayouts 尝试解析 access.log 中 time 字段的多种格式
var logTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02T15:04:05.999999999Z07:00",
}

func parseLogTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range logTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// computeUsage 读取 access.log 并聚合统计
func computeUsage() (*usageResult, error) {
	path := model.DataDir() + "/access.log"

	tokens, err := model.GetAllTokens()
	if err != nil {
		tokens = nil
	}
	keyToOwner := make(map[string]string)
	for _, t := range tokens {
		keyToOwner[t.Key] = t.Owner
	}

	perUser := make(map[string]int64)
	perModel := make(map[string]int64)
	var todayTokens int64

	now := time.Now()
	todayYear, todayMonth, todayDay := now.Date()

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &usageResult{PerUser: []usageUserEntry{}, TopModels: []usageModelEntry{}}, nil
		}
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e struct {
			Time  string  `json:"time"`
			Model string  `json:"model"`
			Token string  `json:"token"`
			Cost  float64 `json:"cost"`
			Group string  `json:"group"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		cost := int64(e.Cost)

		if t, ok := parseLogTime(e.Time); ok {
			y, m, d := t.Date()
			if y == todayYear && m == todayMonth && d == todayDay {
				todayTokens += cost
			}
		}

		if e.Model != "" {
			perModel[e.Model] += cost
		}

		owner := keyToOwner[e.Token]
		if owner == "" {
			owner = "未知"
		}
		perUser[owner] += cost
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// 各用户用量（按用量降序）
	perUserList := make([]usageUserEntry, 0, len(perUser))
	for u, v := range perUser {
		perUserList = append(perUserList, usageUserEntry{Username: u, Used: v})
	}
	sort.Slice(perUserList, func(i, j int) bool {
		return perUserList[i].Used > perUserList[j].Used
	})

	// 热门模型 Top10（按用量降序取前 10）
	modelList := make([]usageModelEntry, 0, len(perModel))
	for m, v := range perModel {
		modelList = append(modelList, usageModelEntry{Model: m, Tokens: v})
	}
	sort.Slice(modelList, func(i, j int) bool {
		return modelList[i].Tokens > modelList[j].Tokens
	})
	if len(modelList) > 10 {
		modelList = modelList[:10]
	}

	return &usageResult{
		TodayTokens: todayTokens,
		PerUser:     perUserList,
		TopModels:   modelList,
	}, nil
}

// UsageHandler 处理用量统计请求
func UsageHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	_, ok := requireSession(r)
	if !ok {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	usageMu.RLock()
	cached := usageCache
	stale := usageCacheAt.IsZero() || time.Since(usageCacheAt) > 5*time.Second
	usageMu.RUnlock()

	if stale {
		res, err := computeUsage()
		if err != nil {
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		usageMu.Lock()
		usageCache = res
		usageCacheAt = time.Now()
		usageMu.Unlock()
		cached = res
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cached)
}
