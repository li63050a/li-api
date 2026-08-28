package handler

import (
	"api-gateway/model"
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/smtp"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	schedulerOnce      sync.Once
	schedulerMu        sync.Mutex
	schedulerLastRun   string // YYYY-MM-DD，当日日常任务已运行的日期
	schedulerLastMonth string
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
	tokenExpiryReminder()
	runMonthlyTaskIfDue()
}

func runMonthlyTaskIfDue() {
	now := time.Now()
	month := now.Format("2006-01")
	if schedulerLastMonth == month {
		return
	}
	schedulerLastMonth = month
	firstOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	lastMonth := firstOfMonth.AddDate(0, -1, 0).Format("2006-01")
	requests, cost := buildMonthlySummary(lastMonth)
	if err := sendMonthlySummaryEmail(lastMonth, requests, cost); err != nil {
		log.Printf("scheduler: monthly summary email: %v", err)
		recordTask("monthly_email", "failed: "+err.Error())
		return
	}
	recordTask("monthly_email", fmt.Sprintf("month=%s requests=%d cost=%d", lastMonth, requests, cost))
}

// recordTask 记录一次调度任务运行结果到 KV "task.<seq>"，最多保留 50 条
func recordTask(name, result string) {
	seq := 1
	if raw, ok := model.KVGet("task.seq"); ok {
		if n, err := strconv.Atoi(raw); err == nil {
			seq = n + 1
		}
	}
	rec := map[string]string{
		"time":   time.Now().Format(time.RFC3339),
		"name":   name,
		"result": result,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_ = model.KVSet(fmt.Sprintf("task.%d", seq), string(b))
	_ = model.KVSet("task.seq", strconv.Itoa(seq))

	// 超过 50 条时删除 seq 最小的旧记录
	if len(model.KVGetAll("task.")) > 50 {
		smallest := 0
		for suffix := range model.KVGetAll("task.") {
			if suffix == "seq" {
				continue
			}
			if n, err := strconv.Atoi(suffix); err == nil && (smallest == 0 || n < smallest) {
				smallest = n
			}
		}
		if smallest > 0 {
			_ = model.KVDel(fmt.Sprintf("task.%d", smallest))
		}
	}
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
	granted := 0
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
		granted++
		_ = model.AppendBilling(model.BillingEntry{
			User:    username,
			Type:    "subscribe",
			Amount:  plan.Quota,
			Balance: 0,
			Remark:  plan.Name,
		})
	}
	recordTask("subscription_grant", fmt.Sprintf("granted %d user(s)", granted))
}

// channelHealthPing 渠道健康探测：GET BaseURL+/v1/models，失败仅记录日志，不自动禁用
func channelHealthPing() {
	channels, err := model.GetAllChannels()
	if err != nil {
		log.Printf("scheduler: load channels: %v", err)
		recordTask("channel_health_ping", "failed: "+err.Error())
		return
	}
	for _, c := range channels {
		probeChannelHealth(c)
	}
	recordTask("channel_health_ping", fmt.Sprintf("probed %d channel(s)", len(channels)))
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
		recordTask("stats_snapshot", "failed: no access.log")
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
	recordTask("stats_snapshot", fmt.Sprintf("requests=%d cost=%.4f", requests, cost))
}

// tokenExpiryReminder 令牌到期提醒：对 3 天内过期且尚未提醒过的令牌，向归属用户发邮件
func tokenExpiryReminder() {
	tokens, err := model.GetAllTokens()
	if err != nil {
		log.Printf("scheduler: load tokens: %v", err)
		return
	}
	cfg := model.GetSetting()
	if cfg.SMTPHost == "" {
		return
	}
	now := time.Now()
	deadline := now.Add(3 * 24 * time.Hour)
	for _, t := range tokens {
		if t.ExpiredAt.IsZero() || !now.Before(t.ExpiredAt) || !t.ExpiredAt.Before(deadline) {
			continue
		}
		if _, ok := model.KVGet("reminded." + t.Key); ok {
			continue
		}
		u, found := model.GetUserByUsername(t.Owner)
		if !found || u.Email == "" {
			continue
		}
		body := "Your token " + t.Name + " (" + t.Key + ") expires at " + t.ExpiredAt.Format(time.RFC3339) + ".\r\n\r\nPlease renew it before expiration."
		if err := schedulerSendMail(cfg, u.Email, "Token "+t.Name+" expires soon", body); err != nil {
			log.Printf("scheduler: token expiry reminder for %q failed: %v", t.Key, err)
			continue
		}
		_ = model.KVSet("reminded."+t.Key, "1")
	}
}

// schedulerSendMail 通过全局 SMTP 配置发送邮件（SMTPUser 为空时不使用认证）
func schedulerSendMail(cfg model.Setting, to, subject, body string) error {
	if cfg.SMTPHost == "" {
		return fmt.Errorf("smtp not configured")
	}
	port := cfg.SMTPPort
	if port == 0 {
		port = 25
	}
	from := cfg.SMTPFrom
	if from == "" {
		from = cfg.SMTPUser
	}
	if from == "" {
		from = to
	}
	var auth smtp.Auth
	if cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPHost)
	}
	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, port)
	subj := mime.QEncoding.Encode("UTF-8", subject)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", from, to, subj, body)
	return smtp.SendMail(addr, auth, from, []string{to}, []byte(msg))
}
