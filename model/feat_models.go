package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// PriceEntry 单个模型的价格倍率
type PriceEntry struct {
	Ratio           float64 `json:"ratio"`
	CompletionRatio float64 `json:"completion_ratio"`
}

// ModelPrices 价格表：模型名 -> 价格倍率
type ModelPrices map[string]PriceEntry

var (
	modelPricesMu sync.RWMutex
	modelPrices   ModelPrices
	modelPricesOk bool
)

// modelPricesPath 价格表文件路径
func modelPricesPath() string {
	return filepath.Join(DataDir(), "model_prices.json")
}

// loadModelPrices 从磁盘加载价格表；文件不存在则用空表并写入默认值。
func loadModelPrices() (ModelPrices, error) {
	modelPricesMu.Lock()
	defer modelPricesMu.Unlock()
	if modelPricesOk {
		return modelPrices, nil
	}
	path := modelPricesPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			modelPrices = ModelPrices{}
			modelPricesOk = true
			_ = saveModelPricesLocked()
			return modelPrices, nil
		}
		return nil, err
	}
	m := ModelPrices{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, err
		}
	}
	if m == nil {
		m = ModelPrices{}
	}
	modelPrices = m
	modelPricesOk = true
	return modelPrices, nil
}

// saveModelPricesLocked 必须在持有 modelPricesMu 时调用
func saveModelPricesLocked() error {
	path := modelPricesPath()
	data, err := json.MarshalIndent(modelPrices, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// GetModelPrices 返回价格表副本（懒初始化）
func GetModelPrices() ModelPrices {
	m, err := loadModelPrices()
	if err != nil {
		return ModelPrices{}
	}
	modelPricesMu.RLock()
	defer modelPricesMu.RUnlock()
	out := make(ModelPrices, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// SaveModelPrices 全量替换并保存价格表
func SaveModelPrices(m ModelPrices) error {
	modelPricesMu.Lock()
	defer modelPricesMu.Unlock()
	if m == nil {
		m = ModelPrices{}
	}
	modelPrices = m
	modelPricesOk = true
	return saveModelPricesLocked()
}
