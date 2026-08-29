package handler

import (
	"api-gateway/model"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 数学人机验证：内存存储，单次有效，5 分钟过期。
// 注册强制校验；登录开关 security.captcha_login = "1" 时校验。
const (
	captchaTTL      = 5 * time.Minute
	maxCaptchas     = 200
	captchaRandMax  = 20
	captchaRandMin  = 1
)

type captchaEntry struct {
	answer string
	exp    time.Time
}

var (
	captchaMu sync.Mutex
	captchas  = map[string]captchaEntry{}
)

// purgeCaptchasLocked 惰性清理已过期的验证码（调用方需持锁）
func purgeCaptchasLocked() {
	now := time.Now()
	for k, v := range captchas {
		if now.After(v.exp) {
			delete(captchas, k)
		}
	}
}

// dropOldestCaptchaLocked 超出上限时删除最早的一条（调用方需持锁）
func dropOldestCaptchaLocked() {
	if len(captchas) <= maxCaptchas {
		return
	}
	var oldest string
	var oldestExp time.Time
	first := true
	for k, v := range captchas {
		if first || v.exp.Before(oldestExp) {
			oldest = k
			oldestExp = v.exp
			first = false
		}
	}
	delete(captchas, oldest)
}

// newCaptchaID 生成随机 16 位十六进制 id
func newCaptchaID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// randCaptchaInt 生成 [1,20] 的随机整数
func randCaptchaInt() int {
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return captchaRandMin
	}
	return captchaRandMin + int(b[0]%captchaRandMax)
}

func setCaptchaCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// CaptchaHandler GET /api/captcha 生成一道加法人机验证题
func CaptchaHandler(w http.ResponseWriter, r *http.Request) {
	setCaptchaCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a := randCaptchaInt()
	b := randCaptchaInt()
	id := newCaptchaID()
	captchaMu.Lock()
	purgeCaptchasLocked()
	captchas[id] = captchaEntry{answer: strconv.Itoa(a + b), exp: time.Now().Add(captchaTTL)}
	dropOldestCaptchaLocked()
	captchaMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":       id,
		"question": fmt.Sprintf("%d + %d = ?", a, b),
		"hint":     "输入计算结果",
	})
}

// VerifyCaptcha 校验验证码：一次性使用，过期/不存在/答案错误均返回 false
func VerifyCaptcha(id, answer string) bool {
	if id == "" {
		return false
	}
	captchaMu.Lock()
	defer captchaMu.Unlock()
	purgeCaptchasLocked()
	e, ok := captchas[id]
	if !ok {
		return false
	}
	delete(captchas, id)
	if time.Now().After(e.exp) {
		return false
	}
	return strings.TrimSpace(answer) == e.answer
}

// ModelsPublicHandler GET /api/models_public 公开模型列表（无需登录，不含密钥）
func ModelsPublicHandler(w http.ResponseWriter, r *http.Request) {
	setCaptchaCORS(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	set := map[string]struct{}{}
	if chans, err := model.GetAllChannelsRaw(); err == nil {
		for _, c := range chans {
			for _, m := range strings.Split(c.Models, ",") {
				m = strings.TrimSpace(m)
				if m == "" || m == "*" {
					continue
				}
				set[m] = struct{}{}
			}
		}
	}
	for name := range model.KVGetAll("alias.") {
		set[name] = struct{}{}
	}
	for _, vm := range model.GetVModels() {
		set[vm.Display] = struct{}{}
	}
	names := make([]string, 0, len(set))
	for m := range set {
		names = append(names, m)
	}
	sort.Strings(names)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"models": names})
}

// 通过 init() 注册路由，避免修改 main.go。
func init() {
	http.HandleFunc("/api/captcha", CaptchaHandler)
	http.HandleFunc("/api/models_public", ModelsPublicHandler)
}