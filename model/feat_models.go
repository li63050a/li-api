package model

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"api-gateway/db"
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

// loadModelPrices 从 SQLite 加载价格表；无记录时回退到迁移旧 JSON 或写入空表。
func loadModelPrices() (ModelPrices, error) {
	modelPricesMu.Lock()
	defer modelPricesMu.Unlock()
	if modelPricesOk {
		return modelPrices, nil
	}

	var dataText string
	err := db.DB.QueryRow("SELECT data FROM model_prices WHERE id = 1").Scan(&dataText)
	if err == nil {
		m := ModelPrices{}
		if len(dataText) > 0 {
			if err := json.Unmarshal([]byte(dataText), &m); err != nil {
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
	if err != sql.ErrNoRows {
		return nil, err
	}

	path := modelPricesPath()
	if data, ferr := os.ReadFile(path); ferr == nil {
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
		if serr := saveModelPricesLocked(); serr != nil {
			return nil, serr
		}
		db.RenameJSONToBak(DataDir(), "model_prices.json")
		return modelPrices, nil
	}

	modelPrices = ModelPrices{}
	modelPricesOk = true
	_ = saveModelPricesLocked()
	return modelPrices, nil
}

// saveModelPricesLocked 必须在持有 modelPricesMu 时调用
func saveModelPricesLocked() error {
	data, err := json.Marshal(modelPrices)
	if err != nil {
		return err
	}
	if _, err := db.DB.Exec("DELETE FROM model_prices WHERE id = 1"); err != nil {
		return err
	}
	_, err = db.DB.Exec("INSERT INTO model_prices (id, data) VALUES (1, ?)", string(data))
	return err
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
