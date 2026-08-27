package handler

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"api-gateway/model"
)

const (
	guardKeyWhitelist = "guard.ip_whitelist"
	guardKeyBlacklist = "guard.ip_blacklist"
	guardKeyRateLimit = "guard.ip_rate_limit"
)

var (
	guardMu    sync.Mutex
	guardDirty = true
	guardWhite []*net.IPNet
	guardBlack []*net.IPNet
	guardLimit int
)

var (
	rateMu   sync.Mutex
	rateHits = make(map[string][]time.Time)
	rateOnce sync.Once
)

func init() {
	http.HandleFunc("/api/setting/guard", GuardSettingsHandler)
}

func guardWriteErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{"error": msg})
}

func refreshGuard() {
	guardMu.Lock()
	defer guardMu.Unlock()
	if !guardDirty {
		return
	}
	if wl, ok := model.KVGet(guardKeyWhitelist); ok {
		guardWhite = parseIPList(wl)
	} else {
		guardWhite = nil
	}
	if bl, ok := model.KVGet(guardKeyBlacklist); ok {
		guardBlack = parseIPList(bl)
	} else {
		guardBlack = nil
	}
	if rl, ok := model.KVGet(guardKeyRateLimit); ok {
		guardLimit, _ = strconv.Atoi(strings.TrimSpace(rl))
	} else {
		guardLimit = 0
	}
	guardDirty = false
}

func parseIPList(s string) []*net.IPNet {
	var out []*net.IPNet
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if n := parseCIDREntry(part); n != nil {
			out = append(out, n)
		}
	}
	return out
}

func parseCIDREntry(s string) *net.IPNet {
	if strings.Contains(s, "/") {
		_, n, err := net.ParseCIDR(s)
		if err == nil {
			return n
		}
		return nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil
	}
	if ip4 := ip.To4(); ip4 != nil {
		return &net.IPNet{IP: ip4, Mask: net.CIDRMask(32, 32)}
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
}

func ipInList(ipStr string, list []*net.IPNet) bool {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return false
	}
	for _, n := range list {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func clientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return xff
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func guardRateAllow(ip string, limit int) bool {
	rateOnce.Do(startRateJanitor)
	rateMu.Lock()
	defer rateMu.Unlock()
	now := time.Now()
	window := now.Add(-time.Minute)
	kept := rateHits[ip][:0]
	for _, t := range rateHits[ip] {
		if t.After(window) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		rateHits[ip] = kept
		return false
	}
	rateHits[ip] = append(kept, now)
	return true
}

func startRateJanitor() {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			window := now.Add(-time.Minute)
			rateMu.Lock()
			for k, ts := range rateHits {
				kept := ts[:0]
				for _, t := range ts {
					if t.After(window) {
						kept = append(kept, t)
					}
				}
				if len(kept) == 0 {
					delete(rateHits, k)
				} else {
					rateHits[k] = kept
				}
			}
			rateMu.Unlock()
		}
	}()
}

func GuardSettingsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	switch r.Method {
	case "GET":
		wl, _ := model.KVGet(guardKeyWhitelist)
		bl, _ := model.KVGet(guardKeyBlacklist)
		rl, _ := model.KVGet(guardKeyRateLimit)
		limit, _ := strconv.Atoi(strings.TrimSpace(rl))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ip_whitelist":  wl,
			"ip_blacklist":  bl,
			"ip_rate_limit": limit,
		})
	case "POST":
		s, ok := requireSession(r)
		if !ok || !model.IsRoot(s.Username) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		var req struct {
			IPWhitelist string `json:"ip_whitelist"`
			IPBlacklist string `json:"ip_blacklist"`
			IPRateLimit int    `json:"ip_rate_limit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if err := model.KVSet(guardKeyWhitelist, req.IPWhitelist); err != nil {
			http.Error(w, "Save failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := model.KVSet(guardKeyBlacklist, req.IPBlacklist); err != nil {
			http.Error(w, "Save failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := model.KVSet(guardKeyRateLimit, strconv.Itoa(req.IPRateLimit)); err != nil {
			http.Error(w, "Save failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		guardMu.Lock()
		guardDirty = true
		guardMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func GuardMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		refreshGuard()
		guardMu.Lock()
		white := guardWhite
		black := guardBlack
		limit := guardLimit
		guardMu.Unlock()

		ip := clientIP(r)

		if len(white) > 0 && !ipInList(ip, white) {
			log.Printf("guard: ip %s not allowed", ip)
			guardWriteErr(w, http.StatusForbidden, "ip not allowed")
			return
		}
		if len(black) > 0 && ipInList(ip, black) {
			log.Printf("guard: ip %s blocked", ip)
			guardWriteErr(w, http.StatusForbidden, "ip blocked")
			return
		}
		if limit > 0 && !guardRateAllow(ip, limit) {
			log.Printf("guard: ip %s rate limited", ip)
			guardWriteErr(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		next(w, r)
	}
}
