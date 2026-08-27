package model

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Redemption 充值码（仿 new-api：管理员生成，用户兑换后获得额度）
type Redemption struct {
	Code       string    `json:"code"`
	Quota      int64     `json:"quota"`       // 兑换后增加的额度
	Status     int       `json:"status"`      // 1 未使用 0 已使用
	CreatedBy  string    `json:"created_by"`  // 生成者
	CreatedAt  time.Time `json:"created_at"`
	RedeemedBy string    `json:"redeemed_by"` // 兑换者
	RedeemedAt time.Time `json:"redeemed_at"`
}

var (
	redeemMu       sync.RWMutex
	redemptions    []Redemption
)

// InitRedemptions 加载充值码数据
func InitRedemptions() error {
	path := filepath.Join(dataDir, "redemptions.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			redemptions = []Redemption{}
			return nil
		}
		return err
	}
	if len(data) == 0 {
		redemptions = []Redemption{}
		return nil
	}
	if err := json.Unmarshal(data, &redemptions); err != nil {
		return err
	}
	return nil
}

func saveRedemptions() error {
	path := filepath.Join(dataDir, "redemptions.json")
	data, err := json.MarshalIndent(redemptions, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// CreateRedemptions 批量生成充值码
func CreateRedemptions(quota int64, count int, creator string) ([]string, error) {
	if count <= 0 || count > 100 {
		return nil, errors.New("count 超出范围 (1-100)")
	}
	if quota <= 0 {
		return nil, errors.New("quota 必须为正数")
	}
	redeemMu.Lock()
	defer redeemMu.Unlock()
	codes := make([]string, 0, count)
	now := time.Now()
	for i := 0; i < count; i++ {
		code := randToken(16)
		redemptions = append(redemptions, Redemption{
			Code:      code,
			Quota:     quota,
			Status:    1,
			CreatedBy: creator,
			CreatedAt: now,
		})
		codes = append(codes, code)
	}
	if err := saveRedemptions(); err != nil {
		return nil, err
	}
	return codes, nil
}

// GetAllRedemptions 返回全部充值码（管理后台展示）
func GetAllRedemptions() []Redemption {
	redeemMu.RLock()
	defer redeemMu.RUnlock()
	out := make([]Redemption, len(redemptions))
	copy(out, redemptions)
	return out
}

// Redeem 兑换充值码，将额度加到指定用户上
func Redeem(code, username string) error {
	redeemMu.Lock()
	defer redeemMu.Unlock()
	for i := range redemptions {
		if redemptions[i].Code == code {
			if redemptions[i].Status != 1 {
				return errors.New("充值码已使用或无效")
			}
			redemptions[i].Status = 0
			redemptions[i].RedeemedBy = username
			redemptions[i].RedeemedAt = time.Now()
			if err := saveRedemptions(); err != nil {
				return err
			}
			// 给用户加额度
			AddUserQuota(username, redemptions[i].Quota)
			return nil
		}
	}
	return errors.New("充值码不存在")
}
