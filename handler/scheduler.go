package handler

import (
	"api-gateway/model"
	"bufio"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	schedulerOnce    sync.Once
	schedulerMu      sync.Mutex
	schedulerLastRun string // YYYY-MM-DD，当日日常任务已运行的日期
)

// init 启动后台调度器（无需修改 main.go）
func init() {
	StartScheduler()
}

// StartScheduler 启动后台调度器：每 30 分钟一个 tick，仅当当日任务尚未运行时执行一次
func StartScheduler() {
	schedulerOnce.Do(func() {
		go schedulerLoop()
	})
}

func schedulerLoop() {
	// 等待 main 完成存储初始化后再跑首个任务
	time.Sleep(10 * time.Second)
	runDailyTasksIfDue()
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		runDailyTasksIfDue()
	}
}

func runDailyTasksIfDue() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("scheduler: task panic recovered: %v", r)
		}
	}()
	schedulerMu.Lock()
	defer schedulerMu.Unlock()
	today := time.Now().Format("2006-01-02")
	if schedulerLastRun == today {
		return
	}
	schedulerLastRun = today
	subDailyGrant()
	channelHealthPing()
	statsSnapshot()
}

// findPlan 按名称查找套餐
func findPlan(name string) (model.Plan, bool) {
	for _, p := range model.GetPlans() {
		if p.Name == name {
			return p, true
		}
	}
	return model.Plan{}, false
}

// subDailyGrant 订阅每日额度发放：对每个未过期订阅，按套餐额度再次发放
func subDailyGrant() {
	subs := model.KVGetAll("sub.")
	for username, v := range subs {
		var s model.Sub
		if json.Unmarshal([]byte(v), &s) != nil || s.Plan == "" {
			continue
		}
		expire, err := time.Parse(time.RFC3339, s.Expire)
		if err != nil || time.Now().After(expire) {
			continue
		}
		plan, ok := findPlan(s.Plan)
		if !ok {
			continue
		}
		model.AddUserQuota(username, plan.Quota)
		_ = model.AppendBilling(model.BillingEntry{
			User:    username,
			Type:    "subscribe",
			Amount:  plan.Quota,
			Balance: 0,
			Remark:  plan.Name,
		})
	}
}

// channelHealthPing 渠道健康探测：GET BaseURL+/v1/models，失败仅记录日志，不自动禁用
func channelHealthPing() {
	channels, err := model.GetAllChannels()
	if err != nil {
		log.Printf("scheduler: load channels: %v", err)
		return
	}
	for _, c := range channels {
		probeChannelHealth(c)
	}
}

// probeChannelHealth 对单条渠道执行轻量健康探测（5s 超时，取首个上游密钥）
func probeChannelHealth(c model.Channel) {
	base := strings.TrimRight(c.BaseURL, "/")
	target := base + "/v1/models"
	key := ""
	if keys := strings.Split(c.Keys, ","); len(keys) > 0 {
		key = strings.TrimSpace(keys[0])
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		log.Printf("scheduler: channel %q health probe failed: %v", c.Name, err)
		return
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("scheduler: channel %q health probe failed: %v", c.Name, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("scheduler: channel %q health probe failed: status %d", c.Name, resp.StatusCode)
	}
}

// statsSnapshot 今日统计快照：聚合 access.log 中当天的请求数与费用，写入 KV snapshot.daily
func statsSnapshot() {
	path := filepath.Join(model.DataDir(), "access.log")
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("scheduler: open access.log: %v", err)
		}
		return
	}
	defer f.Close()

	now := time.Now()
	today := now.Format("2006-01-02")
	cy, cm, cd := now.Date()

	var requests int64
	var cost float64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e struct {
			Time string  `json:"time"`
			Cost float64 `json:"cost"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		t, ok := parseLogTime(e.Time)
		if !ok {
			continue
		}
		ty, tm, td := t.Date()
		if ty == cy && tm == cm && td == cd {
			requests++
			cost += e.Cost
		}
	}

	snap := map[string]interface{}{
		"date":       today,
		"requests":   requests,
		"cost":       cost,
		"updated_at": now.Format(time.RFC3339),
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return
	}
	_ = model.KVSet("snapshot.daily", string(b))
}
