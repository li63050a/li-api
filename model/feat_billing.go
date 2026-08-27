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

// BillingEntry 一条额度/账单变动记录
type BillingEntry struct {
	Time    string `json:"time"`    // RFC3339 时间
	User    string `json:"user"`    // 关联用户
	Type    string `json:"type"`    // 变动类型：adjust / redeem 等
	Amount  int64  `json:"amount"`  // 变动额度（可正可负）
	Balance int64  `json:"balance"` // 变动后余额
	Remark  string `json:"remark"`  // 备注
}

var (
	billingMu          sync.RWMutex
	billingMigrateOnce sync.Once
)

// AppendBilling 追加一条账单记录（SQLite）
func AppendBilling(e BillingEntry) error {
	billingMu.Lock()
	defer billingMu.Unlock()
	billingMigrateOnce.Do(func() { migrateBillingJSON() })
	if e.Time == "" {
		e.Time = time.Now().Format(time.RFC3339)
	}
	_, err := db.DB.Exec(`INSERT INTO billing(time,usr,type,amount,balance,remark) VALUES(?,?,?,?,?,?)`,
		e.Time, e.User, e.Type, e.Amount, e.Balance, e.Remark)
	return err
}

// LoadBillings 读取全部账单，返回最近 500 条且倒序（最新在前）
func LoadBillings() []BillingEntry {
	billingMu.RLock()
	defer billingMu.RUnlock()
	billingMigrateOnce.Do(func() { migrateBillingJSON() })

	rows, err := db.DB.Query(`SELECT id,time,usr,type,amount,balance,remark FROM billing ORDER BY id DESC`)
	if err != nil {
		return []BillingEntry{}
	}
	defer rows.Close()
	var entries []BillingEntry
	for rows.Next() {
		var e BillingEntry
		var id int
		if err := rows.Scan(&id, &e.Time, &e.User, &e.Type, &e.Amount, &e.Balance, &e.Remark); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if len(entries) > 500 {
		entries = entries[:500]
	}
	return entries
}

// migrateBillingJSON 首次启动时把旧的 billing.json(JSONL) 迁移进 SQLite
func migrateBillingJSON() {
	empty, err := db.IsEmpty("billing")
	if err != nil || !empty {
		return
	}
	path := filepath.Join(DataDir(), "billing.json")
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e BillingEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		_, _ = db.DB.Exec(`INSERT INTO billing(time,usr,type,amount,balance,remark) VALUES(?,?,?,?,?,?)`,
			e.Time, e.User, e.Type, e.Amount, e.Balance, e.Remark)
	}
	db.RenameJSONToBak(DataDir(), "billing.json")
}