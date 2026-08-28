package handler

import (
	"api-gateway/model"
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 公开接口（无需登录）：
//
//	GET /api/rankings  用量排行榜（Top10 用户 / 模型 / 上游渠道）
//	GET /api/pricing   模型价格表（含别名）
//
// 通过 init() 注册路由，避免修改 main.go。
func init() {
	http.HandleFunc("/api/rankings", RankingsHandler)
	http.HandleFunc("/api/pricing", PricingHandler)
}

// maxRankingLines access.log 参与统计的最大行数（只取最近 10 万条）
const maxRankingLines = 100000

func setPublicCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// readAccessLog 读取 access.log（JSONL），只保留最近 maxRankingLines 条有效行
func readAccessLog() ([]string, error) {
	path := filepath.Join(model.DataDir(), "access.log")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	var lines []string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) > maxRankingLines {
			lines = append(lines[:0], lines[len(lines)-maxRankingLines:]...)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

type rankingAgg struct {
	Requests int64
	Cost     float64
}

type rankingUserEntry struct {
	Token    string  `json:"token"`
	Requests int64   `json:"requests"`
	Cost     float64 `json:"cost"`
}

type rankingModelEntry struct {
	Model    string  `json:"model"`
	Requests int64   `json:"requests"`
	Cost     float64 `json:"cost"`
}

type rankingChannelEntry struct {
	Channel  string  `json:"channel"`
	Requests int64   `json:"requests"`
	Cost     float64 `json:"cost"`
}

// RankingsHandler GET /api/rankings 公开排行榜
func RankingsHandler(w http.ResponseWriter, r *http.Request) {
	setPublicCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	lines, err := readAccessLog()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	users := map[string]*rankingAgg{}
	models := map[string]*rankingAgg{}
	channels := map[string]*rankingAgg{}

	for _, line := range lines {
		var e struct {
			Token    string  `json:"token"`
			Model    string  `json:"model"`
			Upstream string  `json:"upstream"`
			Cost     float64 `json:"cost"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if e.Token != "" {
			users[e.Token] = addRank(users[e.Token], e.Cost)
		}
		if e.Model != "" {
			models[e.Model] = addRank(models[e.Model], e.Cost)
		}
		if e.Upstream != "" {
			channels[e.Upstream] = addRank(channels[e.Upstream], e.Cost)
		}
	}

	topUsers := topUserEntries(users)
	topModels := topModelEntries(models)
	topChannels := topChannelEntries(channels)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"top_users":    topUsers,
		"top_models":   topModels,
		"top_channels": topChannels,
	})
}

func addRank(a *rankingAgg, cost float64) *rankingAgg {
	if a == nil {
		a = &rankingAgg{}
	}
	a.Requests++
	a.Cost += cost
	return a
}

// topUserEntries 按消费额降序取 Top10，Token 脱敏
func topUserEntries(m map[string]*rankingAgg) []rankingUserEntry {
	list := make([]rankingUserEntry, 0, len(m))
	for t, a := range m {
		list = append(list, rankingUserEntry{Token: maskToken(t), Requests: a.Requests, Cost: a.Cost})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Cost != list[j].Cost {
			return list[i].Cost > list[j].Cost
		}
		return list[i].Token < list[j].Token
	})
	if len(list) > 10 {
		list = list[:10]
	}
	return list
}

// topModelEntries 按消费额降序取 Top10
func topModelEntries(m map[string]*rankingAgg) []rankingModelEntry {
	list := make([]rankingModelEntry, 0, len(m))
	for name, a := range m {
		list = append(list, rankingModelEntry{Model: name, Requests: a.Requests, Cost: a.Cost})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Cost != list[j].Cost {
			return list[i].Cost > list[j].Cost
		}
		return list[i].Model < list[j].Model
	})
	if len(list) > 10 {
		list = list[:10]
	}
	return list
}

// topChannelEntries 按消费额降序取 Top10
func topChannelEntries(m map[string]*rankingAgg) []rankingChannelEntry {
	list := make([]rankingChannelEntry, 0, len(m))
	for name, a := range m {
		list = append(list, rankingChannelEntry{Channel: name, Requests: a.Requests, Cost: a.Cost})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Cost != list[j].Cost {
			return list[i].Cost > list[j].Cost
		}
		return list[i].Channel < list[j].Channel
	})
	if len(list) > 10 {
		list = list[:10]
	}
	return list
}

type pricingModelEntry struct {
	Model           string  `json:"model"`
	Ratio           float64 `json:"ratio"`
	CompletionRatio float64 `json:"completion_ratio"`
	Note            string  `json:"note,omitempty"`
}

// PricingHandler GET /api/pricing 公开模型价格表
func PricingHandler(w http.ResponseWriter, r *http.Request) {
	setPublicCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	prices := model.GetModelPrices()
	setting := model.GetSetting()

	modelSet := map[string]bool{}
	channels, err := model.GetAllChannels()
	if err == nil {
		for _, c := range channels {
			if c.Group != "default" {
				continue
			}
			for _, m := range strings.Split(c.Models, ",") {
				m = strings.TrimSpace(m)
				if m == "" || m == "*" {
					continue
				}
				modelSet[m] = true
			}
		}
	}

	names := make([]string, 0, len(modelSet))
	for m := range modelSet {
		names = append(names, m)
	}
	sort.Strings(names)

	out := make([]pricingModelEntry, 0, len(names)+4)
	for _, m := range names {
		ratio, comp := pricingRatios(m, prices, setting)
		out = append(out, pricingModelEntry{Model: m, Ratio: ratio, CompletionRatio: comp})
	}

	// 附带模型别名（alias.<名字> -> 目标模型）
	aliases := model.KVGetAll("alias.")
	aliasNames := make([]string, 0, len(aliases))
	for name := range aliases {
		aliasNames = append(aliasNames, name)
	}
	sort.Strings(aliasNames)
	for _, name := range aliasNames {
		target := aliases[name]
		ratio, comp := pricingRatios(target, prices, setting)
		out = append(out, pricingModelEntry{
			Model:           name,
			Ratio:           ratio,
			CompletionRatio: comp,
			Note:            fmt.Sprintf("alias of %s", target),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"models": out})
}

// pricingRatios 取模型价格倍率：优先价格表，其次全局设置，缺省为 1
func pricingRatios(m string, prices model.ModelPrices, setting model.Setting) (float64, float64) {
	if p, ok := prices[m]; ok {
		ratio := p.Ratio
		comp := p.CompletionRatio
		if ratio == 0 {
			ratio = setting.ModelRatio[m]
		}
		if comp == 0 {
			comp = setting.CompletionRatio[m]
		}
		if ratio == 0 {
			ratio = 1
		}
		if comp == 0 {
			comp = ratio
		}
		return ratio, comp
	}
	ratio := setting.ModelRatio[m]
	if ratio == 0 {
		ratio = 1
	}
	comp := setting.CompletionRatio[m]
	if comp == 0 {
		comp = ratio
	}
	return ratio, comp
}
