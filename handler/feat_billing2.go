package handler

import (
	"api-gateway/model"
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/admin/channels/balance/", ChannelBalanceHandler)
	http.HandleFunc("/api/billing/summary", BillingSummaryHandler)
}

func setBilling2CORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func writeBalanceJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func queryCreditGrants(client *http.Client, base, key string) (float64, float64, bool) {
	req, err := http.NewRequest("GET", base+"/v1/dashboard/billing/credit_grants", nil)
	if err != nil {
		return 0, 0, false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, false
	}
	var r struct {
		TotalGranted *float64 `json:"total_granted"`
		TotalUsed    *float64 `json:"total_used_amount"`
		TotalUsedAlt *float64 `json:"total_used"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, 0, false
	}
	if r.TotalGranted == nil {
		return 0, 0, false
	}
	used := 0.0
	if r.TotalUsed != nil {
		used = *r.TotalUsed
	} else if r.TotalUsedAlt != nil {
		used = *r.TotalUsedAlt
	}
	return *r.TotalGranted, used, true
}

func queryUsage(client *http.Client, base, key string) (float64, bool) {
	now := time.Now()
	start := now.AddDate(0, 0, -100).Unix()
	end := now.Unix()
	url := fmt.Sprintf("%s/v1/usage?start_date=%d&end_date=%d", base, start, end)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var r struct {
		TotalUsage *float64 `json:"total_usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, false
	}
	if r.TotalUsage == nil {
		return 0, false
	}
	return *r.TotalUsage, true
}

func ChannelBalanceHandler(w http.ResponseWriter, r *http.Request) {
	setBilling2CORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session, ok := requireSession(r)
	if !ok || !model.IsRoot(session.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	idx := strings.LastIndex(r.URL.Path, "/balance/")
	id := 0
	if idx >= 0 {
		id, _ = strconv.Atoi(strings.TrimSuffix(r.URL.Path[idx+len("/balance/"):], "/"))
	}
	ch, found := model.GetChannel(id)
	if !found {
		writeBalanceJSON(w, map[string]interface{}{"channel_id": id, "available": false, "error": "not found"})
		return
	}
	keys := ch.KeyList()
	if len(keys) == 0 || ch.BaseURL == "" {
		writeBalanceJSON(w, map[string]interface{}{"channel_id": id, "available": false, "error": "unavailable"})
		return
	}
	base := strings.TrimRight(ch.BaseURL, "/")
	key := keys[0]
	client := &http.Client{Timeout: 8 * time.Second}

	if granted, used, ok := queryCreditGrants(client, base, key); ok {
		writeBalanceJSON(w, map[string]interface{}{
			"channel_id": id, "available": true,
			"granted": granted, "used": used,
			"balance": granted - used, "currency": "usd",
		})
		return
	}
	if used, ok := queryUsage(client, base, key); ok {
		writeBalanceJSON(w, map[string]interface{}{
			"channel_id": id, "available": false,
			"granted": 0, "used": used, "balance": 0, "currency": "usd",
		})
		return
	}
	writeBalanceJSON(w, map[string]interface{}{"channel_id": id, "available": false, "error": "unavailable"})
}

type monthSummary struct {
	Month    string  `json:"month"`
	Requests int64   `json:"requests"`
	Cost     float64 `json:"cost"`
	Tokens   int64   `json:"tokens"`
}

type modelSummary struct {
	Model    string  `json:"model"`
	Requests int64   `json:"requests"`
	Cost     float64 `json:"cost"`
}

func monthOf(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 7 && s[4] == '-' {
		return s[:7]
	}
	if t, ok := parseLogTime(s); ok {
		return t.Format("2006-01")
	}
	return ""
}

func computeBillingSummary() ([]monthSummary, []modelSummary) {
	file, err := os.Open(filepath.Join(model.DataDir(), "access.log"))
	if err != nil {
		return []monthSummary{}, []modelSummary{}
	}
	defer file.Close()

	months := make(map[string]*monthSummary)
	models := make(map[string]*modelSummary)
	var lines int
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() && lines < 100000 {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lines++
		var e struct {
			Time  string  `json:"time"`
			Model string  `json:"model"`
			Cost  float64 `json:"cost"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		month := monthOf(e.Time)
		if month == "" {
			continue
		}
		ms := months[month]
		if ms == nil {
			ms = &monthSummary{Month: month}
			months[month] = ms
		}
		ms.Requests++
		ms.Cost += e.Cost
		ms.Tokens = int64(ms.Cost)
		if e.Model != "" {
			m := models[e.Model]
			if m == nil {
				m = &modelSummary{Model: e.Model}
				models[e.Model] = m
			}
			m.Requests++
			m.Cost += e.Cost
		}
	}

	monthList := make([]monthSummary, 0, len(months))
	for _, ms := range months {
		monthList = append(monthList, *ms)
	}
	sort.Slice(monthList, func(i, j int) bool { return monthList[i].Month < monthList[j].Month })

	modelList := make([]modelSummary, 0, len(models))
	for _, m := range models {
		modelList = append(modelList, *m)
	}
	sort.Slice(modelList, func(i, j int) bool { return modelList[i].Cost > modelList[j].Cost })
	if len(modelList) > 5 {
		modelList = modelList[:5]
	}
	return monthList, modelList
}

func BillingSummaryHandler(w http.ResponseWriter, r *http.Request) {
	setBilling2CORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session, ok := requireSession(r)
	if !ok || !model.IsRoot(session.Username) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	months, topModels := computeBillingSummary()

	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="billing-summary.csv"`)
		cw := csv.NewWriter(w)
		cw.Write([]string{"month", "requests", "cost", "tokens"})
		for _, m := range months {
			cw.Write([]string{
				m.Month,
				strconv.FormatInt(m.Requests, 10),
				strconv.FormatFloat(m.Cost, 'f', 2, 64),
				strconv.FormatInt(m.Tokens, 10),
			})
		}
		cw.Flush()
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"months":     months,
		"top_models": topModels,
	})
}
