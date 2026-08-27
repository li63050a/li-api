package model

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"api-gateway/db"
)

// AuditEntry 审计日志条目（SQLite 存储）
type AuditEntry struct {
	Time   string `json:"time"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Detail string `json:"detail"`
}

var (
	auditMu           sync.RWMutex
	auditMigrateOnce sync.Once
)

// AppendAudit 追加一行审计日志；Time 使用 RFC3339 格式。
func AppendAudit(actor, action, detail string) error {
	auditMu.Lock()
	defer auditMu.Unlock()
	auditMigrateOnce.Do(func() { migrateAuditJSON() })
	_, err := db.DB.Exec(`INSERT INTO audits(time,actor,action,detail) VALUES(?,?,?,?)`,
		time.Now().Format(time.RFC3339), actor, action, detail)
	return err
}

// LoadAudits 读取全部审计日志，返回最近 500 条（倒序，最新在前）。
func LoadAudits() []AuditEntry {
	auditMu.RLock()
	defer auditMu.RUnlock()
	auditMigrateOnce.Do(func() { migrateAuditJSON() })

	rows, err := db.DB.Query(`SELECT id,time,actor,action,detail FROM audits ORDER BY id DESC`)
	if err != nil {
		return []AuditEntry{}
	}
	defer rows.Close()
	var all []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var id int
		if err := rows.Scan(&id, &e.Time, &e.Actor, &e.Action, &e.Detail); err != nil {
			continue
		}
		all = append(all, e)
	}
	if len(all) > 500 {
		all = all[:500]
	}
	return all
}

// ClearAudits 清空审计日志
func ClearAudits() error {
	auditMu.Lock()
	defer auditMu.Unlock()
	_, err := db.DB.Exec("DELETE FROM audits")
	return err
}

// migrateAuditJSON 首次启动时把旧的 audit.json(JSONL) 迁移进 SQLite
func migrateAuditJSON() {
	empty, err := db.IsEmpty("audits")
	if err != nil || !empty {
		return
	}
	path := filepath.Join(DataDir(), "audit.json")
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e AuditEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		_, _ = db.DB.Exec(`INSERT INTO audits(time,actor,action,detail) VALUES(?,?,?,?)`, e.Time, e.Actor, e.Action, e.Detail)
	}
	db.RenameJSONToBak(DataDir(), "audit.json")
}