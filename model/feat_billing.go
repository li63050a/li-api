package model

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
	billingMu      sync.RWMutex
	billingLoaded  bool
)

func billingPath() string {
	return filepath.Join(DataDir(), "billing.json")
}

// AppendBilling 追加一条账单记录（JSONL，懒初始化）
func AppendBilling(e BillingEntry) error {
	billingMu.Lock()
	defer billingMu.Unlock()

	if e.Time == "" {
		e.Time = time.Now().Format(time.RFC3339)
	}

	data, err := json.Marshal(e)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(billingPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// LoadBillings 读取全部账单，返回最近 500 条且倒序
func LoadBillings() []BillingEntry {
	billingMu.RLock()
	defer billingMu.RUnlock()

	path := billingPath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []BillingEntry{}
		}
		return []BillingEntry{}
	}
	defer f.Close()

	var entries []BillingEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e BillingEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}

	// 倒序
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	if len(entries) > 500 {
		entries = entries[:500]
	}
	return entries
}
