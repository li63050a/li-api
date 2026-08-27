package model

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEntry 审计日志条目（JSONL 存储，audit.json 每行一个 JSON）
type AuditEntry struct {
	Time   string `json:"time"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Detail string `json:"detail"`
}

var (
	auditMu   sync.RWMutex
	auditOnce sync.Once
)

func auditPath() string {
	return filepath.Join(DataDir(), "audit.json")
}

// AppendAudit 追加一行审计日志；Time 使用 RFC3339 格式。
func AppendAudit(actor, action, detail string) error {
	auditOnce.Do(func() {})
	auditMu.Lock()
	defer auditMu.Unlock()

	entry := AuditEntry{
		Time:   time.Now().Format(time.RFC3339),
		Actor:  actor,
		Action: action,
		Detail: detail,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(auditPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// LoadAudits 读取全部审计日志，返回最近 500 条（倒序，最新在前）。
// 解析失败的行会被跳过；文件不存在时返回空切片。
func LoadAudits() []AuditEntry {
	auditMu.RLock()
	defer auditMu.RUnlock()

	f, err := os.Open(auditPath())
	if err != nil {
		return []AuditEntry{}
	}
	defer f.Close()

	var all []AuditEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := trimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		all = append(all, e)
	}

	// 倒序：最新在前
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}

	if len(all) > 500 {
		all = all[:500]
	}
	return all
}

// ClearAudits 清空审计日志（写入空文件）。
func ClearAudits() error {
	auditMu.Lock()
	defer auditMu.Unlock()

	f, err := os.OpenFile(auditPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	return f.Close()
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\r' || s[start] == '\n') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r' || s[end-1] == '\n') {
		end--
	}
	return s[start:end]
}
