package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"

	"api-gateway/model"
)

// ServerVersion 由 main 注入当前版本号，供 /api/ops/perf 与 /api/public 返回
var ServerVersion = "dev"

const (
	perfKeyEnabled  = "perf.enabled"
	perfKeyMaxLoad  = "perf.max_load"
	perfKeyMaxMem   = "perf.max_mem_pct"
	perfDefaultLoad = 8.0
	perfDefaultMem  = 90.0
)

func init() {
	http.HandleFunc("/api/ops/perf", PerfHandler)
	http.HandleFunc("/api/setting/perf", PerfSettingsHandler)
}

func perfCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func perfThresholds() (float64, float64) {
	maxLoad := perfDefaultLoad
	maxMem := perfDefaultMem
	if v, ok := model.KVGet(perfKeyMaxLoad); ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f > 0 {
			maxLoad = f
		}
	}
	if v, ok := model.KVGet(perfKeyMaxMem); ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f > 0 {
			maxMem = f
		}
	}
	return maxLoad, maxMem
}

// PerfHandler GET /api/ops/perf 返回系统性能指标（仅 root）
func PerfHandler(w http.ResponseWriter, r *http.Request) {
	perfCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, ok := requireSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "auth_error")
		return
	}
	if !model.IsRoot(s.Username) {
		writeError(w, http.StatusForbidden, "forbidden", "permission_error")
		return
	}

	cpuLoad := readLoadAvg()
	memTotal, memAvail := readMeminfo()
	memTotalMB := memTotal / 1024
	memUsedMB := (memTotal - memAvail) / 1024
	memUsedPct := 0.0
	if memTotal > 0 {
		memUsedPct = float64(memTotal-memAvail) / float64(memTotal) * 100
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"cpu_load":     cpuLoad,
		"mem_used_pct": memUsedPct,
		"mem_total_mb": memTotalMB,
		"mem_used_mb":  memUsedMB,
		"goroutines":   runtime.NumGoroutine(),
		"version":      ServerVersion,
	})
}

// PerfGuard 请求守卫：perf.enabled=1 时，负载或内存超阈值则返回 503
func PerfGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if v, ok := model.KVGet(perfKeyEnabled); !ok || strings.TrimSpace(v) != "1" {
			next(w, r)
			return
		}
		maxLoad, maxMem := perfThresholds()
		if readLoadAvg() > maxLoad || readMemPct() > maxMem {
			writeError(w, http.StatusServiceUnavailable, "system busy", "server_error")
			return
		}
		next(w, r)
	}
}

// PerfSettingsHandler GET/POST /api/setting/perf 读写性能守卫配置（仅 root）
func PerfSettingsHandler(w http.ResponseWriter, r *http.Request) {
	perfCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	s, ok := requireSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "auth_error")
		return
	}
	switch r.Method {
	case "GET":
		if !model.IsRoot(s.Username) {
			writeError(w, http.StatusForbidden, "forbidden", "permission_error")
			return
		}
		enabled := false
		if v, ok := model.KVGet(perfKeyEnabled); ok && strings.TrimSpace(v) == "1" {
			enabled = true
		}
		maxLoad, maxMem := perfThresholds()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled":     enabled,
			"max_load":    maxLoad,
			"max_mem_pct": maxMem,
		})
	case "POST":
		if !model.IsRoot(s.Username) {
			writeError(w, http.StatusForbidden, "forbidden", "permission_error")
			return
		}
		var body struct {
			Enabled   bool    `json:"enabled"`
			MaxLoad   float64 `json:"max_load"`
			MaxMemPct float64 `json:"max_mem_pct"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json", "invalid_request_error")
			return
		}
		ev := "0"
		if body.Enabled {
			ev = "1"
		}
		for k, v := range map[string]string{
			perfKeyEnabled: ev,
			perfKeyMaxLoad: fmt.Sprintf("%v", body.MaxLoad),
			perfKeyMaxMem:  fmt.Sprintf("%v", body.MaxMemPct),
		} {
			if err := model.KVSet(k, v); err != nil {
				writeError(w, http.StatusInternalServerError, "save failed: "+err.Error(), "server_error")
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// readLoadAvg 读取 /proc/loadavg 第一个 token（CPU 负载）；不可用时返回 0
func readLoadAvg() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return f
}

// readMeminfo 读取 MemTotal 与 MemAvailable（kB）；不可用时返回 0,0
func readMeminfo() (total, avail uint64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		if key != "MemTotal" && key != "MemAvailable" {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		if key == "MemTotal" {
			total = v
		} else {
			avail = v
		}
	}
	return total, avail
}

// readMemPct 返回内存使用百分比；不可用时返回 0
func readMemPct() float64 {
	total, avail := readMeminfo()
	if total == 0 {
		return 0
	}
	return float64(total-avail) / float64(total) * 100
}
