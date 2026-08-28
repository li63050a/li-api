package handler

import (
	"api-gateway/model"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// target 表示一次转发目标（渠道 + 具体密钥）
type target struct {
	channel model.Channel
	key     string
}

const (
	failureThreshold    = 5                // 连续失败达到该次数则自动禁用渠道
	autoRecoverCooldown = 60 * time.Second // 自动禁用后多久允许试探性恢复
)

var (
	autoDisabled sync.Map // channelID -> time.Time（被自动禁用的时刻）
	failCount    sync.Map // channelID -> *int64（连续失败计数）
)

// 按用户的分钟级请求限流（滑动窗口）
var (
	userLimitMu sync.Mutex
	userReqWin  = map[string][]time.Time{}
)

func userRateAllowed(username string, limit int) bool {
	if limit <= 0 || username == "" {
		return true
	}
	userLimitMu.Lock()
	defer userLimitMu.Unlock()
	now := time.Now()
	buf := userReqWin[username]
	i := 0
	for ; i < len(buf); i++ {
		if now.Sub(buf[i]) < 60*time.Second {
			break
		}
	}
	buf = buf[i:]
	if len(buf) >= limit {
		userReqWin[username] = buf
		return false
	}
	buf = append(buf, now)
	userReqWin[username] = buf
	return true
}

// channelUsable 判断渠道是否可用（启用且未被自动禁用冷却中）
func channelUsable(c model.Channel) bool {
	if c.Status != 1 {
		return false
	}
	if v, ok := autoDisabled.Load(c.ID); ok {
		if dt, ok2 := v.(time.Time); ok2 && time.Since(dt) < autoRecoverCooldown {
			return false
		}
	}
	return true
}

// markChannelSuccess 重置渠道失败计数并清除自动禁用
func markChannelSuccess(id int) {
	failCount.Store(id, new(int64))
	autoDisabled.LoadAndDelete(id)
}

// markChannelFailure 记录一次失败，达到阈值则自动禁用渠道
func markChannelFailure(id int) {
	c, _ := failCount.LoadOrStore(id, new(int64))
	n := atomic.AddInt64(c.(*int64), 1)
	if n >= failureThreshold {
		autoDisabled.Store(id, time.Now())
		failCount.Store(id, new(int64))
		log.Printf("channel %d auto-disabled after %d consecutive failures", id, failureThreshold)
		notifyChannelDisabled(id)
		_ = NotifyEvent("channel_disabled", fmt.Sprintf("渠道 %d 连续失败已达阈值，已自动禁用", id))
	}
}

// isChannelError 判断上游响应是否代表渠道自身异常（应计为失败并可能自动禁用）
func isChannelError(status int) bool {
	return status == 401 || status == 403 || status == 429 || status >= 500
}

// RelayHandler 仿 new-api 的模型路由转发：按 令牌分组 + 模型名 选择渠道
func RelayHandler(w http.ResponseWriter, r *http.Request) {
	key := extractToken(r)
	tok, err := model.GetToken(key)
	if err != nil {
		http.Error(w, "Invalid API Key", http.StatusUnauthorized)
		return
	}
	if tok.IsExpired() {
		http.Error(w, "Token Expired", http.StatusForbidden)
		return
	}
	if tok.Unlimited == 0 && tok.Quota >= 0 && tok.Used >= tok.Quota {
		http.Error(w, "Quota Exceeded", http.StatusForbidden)
		return
	}
	// 令牌所属用户的额度预检（仅记账，响应完成后统一累加）
	if !model.UserQuotaAllowed(tok.Owner) {
		http.Error(w, "User Quota Exceeded", http.StatusForbidden)
		return
	}
	group := tok.Group
	if group == "" {
		group = "default"
	}

	// 用户级每分钟限流
	if u, ok := model.GetUserByUsername(tok.Owner); ok {
		if !userRateAllowed(tok.Owner, u.RateLimit) {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
	}

	// WebSocket 透传（如 /v1/realtime）：跳过 body 解析，直接转发首个渠道
	if strings.EqualFold(r.Header.Get("Connection"), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		wsModel := ResolveModel(r.URL.Query().Get("model"))
		wsTargets := buildTargets(group, wsModel)
		if len(wsTargets) == 0 {
			http.Error(w, "No available channel for model: "+wsModel, http.StatusBadGateway)
			return
		}
		start := time.Now()
		var wsErr error
		cw := newCountWriter(w)
		t := wsTargets[0]
		proxy := buildForwardProxy(r, t.channel.BaseURL, r.URL.Path, t.channel.AuthType, t.channel.AuthKey, t.key)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			wsErr = err
			http.Error(w, "Bad Gateway: "+err.Error(), http.StatusBadGateway)
		}
		proxy.ServeHTTP(cw, r)
		if wsErr == nil && !isChannelError(cw.status) {
			markChannelSuccess(t.channel.ID)
		} else {
			markChannelFailure(t.channel.ID)
		}
		cost := maxInt64(1, cw.bytes/4)
		cost = model.ModelCost(wsModel, cost, 0)
		_ = model.UseToken(key, cost)
		model.AddUserUsed(tok.Owner, cost)
		recordStats(cost)
		logAccess(map[string]interface{}{
			"time": start.Format(time.RFC3339), "method": r.Method, "path": r.URL.Path,
			"model": wsModel, "group": group, "token": maskToken(key),
			"status": cw.status, "stream": true, "cost": cost,
			"duration": time.Since(start).Milliseconds(),
		})
		return
	}

	// 读取 body 以提取 model / stream（之后会还原用于转发）
	var modelName string
	var stream bool
	var bodyBuf []byte
	if r.Body != nil {
		bodyBuf, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		var p struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if json.Unmarshal(bodyBuf, &p) == nil {
			modelName = p.Model
			stream = p.Stream
		}
		// 还原 body 以便转发
		r.Body = io.NopCloser(bytes.NewReader(bodyBuf))
		r.ContentLength = int64(len(bodyBuf))
	}

	// /v1/models 直接构造返回
	if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/models") {
		serveModels(w, group)
		return
	}

	// 令牌级模型白名单校验（别名先解析为真实模型）
	modelName = ResolveModel(modelName)
	if !model.TokenModelAllowed(tok.Models, modelName) {
		http.Error(w, "Model not allowed for this token", http.StatusForbidden)
		return
	}

	targets := buildTargets(group, modelName)
	if len(targets) == 0 {
		http.Error(w, "No available channel for model: "+modelName, http.StatusBadGateway)
		return
	}

	start := time.Now()
	lastErr := error(nil)

	if stream {
		// 流式：实时转发，不缓冲（不做跨渠道故障转移）
		cw := newCountWriter(w)
		t := targets[0]
		upstream := applyModelMapping(t.channel, modelName)
		nb := rewriteModel(bodyBuf, upstream)
		r.Body = io.NopCloser(bytes.NewReader(nb))
		r.ContentLength = int64(len(nb))
		proxy := buildForwardProxy(r, t.channel.BaseURL, r.URL.Path, t.channel.AuthType, t.channel.AuthKey, t.key)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			lastErr = err
			http.Error(w, "Bad Gateway: "+err.Error(), http.StatusBadGateway)
		}
		proxy.ServeHTTP(cw, r)
		if lastErr == nil && !isChannelError(cw.status) {
			markChannelSuccess(t.channel.ID)
		} else {
			markChannelFailure(t.channel.ID)
		}
		cost := relayEndpointCost(r.URL.Path, bodyBuf, make([]byte, cw.bytes), modelName, "stream")
		cost = model.ModelCost(modelName, cost, 0)
		_ = model.UseToken(key, cost)
		model.AddUserUsed(tok.Owner, cost)
		recordStats(cost)
		logAccess(map[string]interface{}{
			"time": start.Format(time.RFC3339), "method": r.Method, "path": r.URL.Path,
			"model": modelName, "group": group, "token": maskToken(key),
			"status": cw.status, "stream": true, "cost": cost,
			"duration": time.Since(start).Milliseconds(),
		})
		return
	}

	// 非流式：缓冲响应，支持跨渠道 / 多密钥故障转移
	var respBody []byte
	var respStatus int
	var respHeader http.Header
	for _, t := range targets {
		// 按渠道的模型映射重写请求中的 model
		upstream := applyModelMapping(t.channel, modelName)
		nb := rewriteModel(bodyBuf, upstream)
		r.Body = io.NopCloser(bytes.NewReader(nb))
		r.ContentLength = int64(len(nb))
		bw := &bufferWriter{buf: &bytes.Buffer{}}
		proxy := buildForwardProxy(r, t.channel.BaseURL, r.URL.Path, t.channel.AuthType, t.channel.AuthKey, t.key)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			lastErr = err
		}
		proxy.ServeHTTP(bw, r)
		failed := lastErr != nil || isChannelError(bw.status)
		if !failed {
			markChannelSuccess(t.channel.ID)
			respStatus = bw.status
			respHeader = bw.header
			// 把响应里的上游模型名还原为公开名
			respBody = rewriteResponseModel(bw.buf.Bytes(), upstream, modelName)
			break
		}
		markChannelFailure(t.channel.ID)
	}
	if lastErr != nil {
		http.Error(w, "Bad Gateway: "+lastErr.Error(), http.StatusBadGateway)
		return
	}

	// 回写客户端
	for k, vs := range respHeader {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(respStatus)
	_, _ = w.Write(respBody)

	// 计费：按端点类型从 usage / 字节数估算 token 基数（倍率在 ModelCost 中套用）
	cost := relayEndpointCost(r.URL.Path, bodyBuf, respBody, modelName, "normal")
	cost = model.ModelCost(modelName, cost, 0)
	_ = model.UseToken(key, cost)
	model.AddUserUsed(tok.Owner, cost)
	recordStats(cost)

	logAccess(map[string]interface{}{
		"time": start.Format(time.RFC3339), "method": r.Method, "path": r.URL.Path,
		"model": modelName, "group": group, "token": maskToken(key),
		"status": respStatus, "stream": false, "cost": cost,
		"duration": time.Since(start).Milliseconds(),
	})
}

// relayEndpointCost 按端点类型估算本次请求消耗的 token 基数
func relayEndpointCost(path string, reqBody, respBody []byte, modelName string, mode string) int64 {
	switch {
	case strings.Contains(path, "/embeddings"):
		// 嵌入：按 usage.prompt_tokens 计费，缺失时按请求体字节估算
		if total := parseUsage(respBody); total > 0 {
			return total
		}
		return maxInt64(1, int64(len(reqBody))/4)
	case strings.Contains(path, "/moderations"):
		// 审核：按 usage.prompt_tokens 计费，缺失时按响应字节估算
		if total := parseUsage(respBody); total > 0 {
			return total
		}
		return maxInt64(1, int64(len(respBody)))
	case strings.Contains(path, "/images"), strings.Contains(path, "/audio"),
		strings.Contains(path, "/rerank"), strings.Contains(path, "/batch"),
		strings.Contains(path, "/realtime"):
		// 图像/音频/重排/批量/实时：优先 usage，否则按响应字节估算
		if total := parseUsage(respBody); total > 0 {
			return total
		}
		return maxInt64(1, int64(len(respBody))/4)
	default:
		// chat/completions：优先 usage 的 prompt/completion，其次 total，最后按字节估算
		prompt, comp := parseUsageParts(respBody)
		if prompt == 0 && comp == 0 {
			if total := parseUsage(respBody); total > 0 {
				prompt = total
			}
		}
		if prompt == 0 && comp == 0 {
			prompt = maxInt64(1, int64(len(respBody))/4)
		}
		return maxInt64(1, prompt+comp)
	}
}

// applyModelMapping 根据渠道的模型映射，把公开模型名转换为上游模型名
func applyModelMapping(ch model.Channel, modelName string) string {
	if ch.ModelMapping == "" {
		return modelName
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(ch.ModelMapping), &m); err != nil {
		return modelName
	}
	if v, ok := m[modelName]; ok && v != "" {
		return v
	}
	return modelName
}

// rewriteModel 把请求体中的 model 字段改写为指定名称
func rewriteModel(body []byte, modelName string) []byte {
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	m["model"] = modelName
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// rewriteResponseModel 把响应体中的上游模型名还原为公开模型名
func rewriteResponseModel(body []byte, from, to string) []byte {
	if from == to {
		return body
	}
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	if m["model"] == from {
		m["model"] = to
		out, err := json.Marshal(m)
		if err == nil {
			return out
		}
	}
	return body
}

// buildTargets 选出分组内支持该模型的渠道，按优先级+权重排序，并展开多密钥
func buildTargets(group, modelName string) []target {
	all, _ := model.GetAllChannels()
	var cands []model.Channel
	for _, c := range all {
		if !channelUsable(c) {
			continue
		}
		if c.Group != group {
			continue
		}
		if modelName != "" && !c.SupportsModel(modelName) {
			continue
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return nil
	}
	// 取最高优先级
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Priority > cands[j].Priority })
	maxP := cands[0].Priority
	top := cands[:0]
	for _, c := range cands {
		if c.Priority == maxP {
			top = append(top, c)
		} else {
			break
		}
	}
	// 按权重打乱顺序（带权随机，不放回）
	ordered := weightedShuffle(top)

	var targets []target
	for _, c := range ordered {
		keys := c.KeyList()
		if len(keys) == 0 {
			targets = append(targets, target{channel: c, key: ""})
			continue
		}
		// 轮询起点
		var idx uint64
		if v, ok := keyCounters.Load(c.ID); ok {
			idx = atomic.AddUint64(v.(*uint64), 1) - 1
		} else {
			cc := new(uint64)
			keyCounters.Store(c.ID, cc)
		}
		for i := range keys {
			targets = append(targets, target{channel: c, key: keys[(int(idx)+i)%len(keys)]})
		}
	}
	return targets
}

// weightedShuffle 按权重做一次不放回随机排序
func weightedShuffle(channels []model.Channel) []model.Channel {
	pool := make([]model.Channel, len(channels))
	copy(pool, channels)
	out := make([]model.Channel, 0, len(channels))
	for len(pool) > 0 {
		total := 0
		for _, c := range pool {
			w := c.Weight
			if w <= 0 {
				w = 1
			}
			total += w
		}
		n := randInt(total)
		acc := 0
		for i, c := range pool {
			w := c.Weight
			if w <= 0 {
				w = 1
			}
			acc += w
			if n < acc {
				out = append(out, c)
				pool = append(pool[:i], pool[i+1:]...)
				break
			}
		}
	}
	return out
}

// serveModels 返回该分组下所有渠道支持的模型列表
func serveModels(w http.ResponseWriter, group string) {
	all, _ := model.GetAllChannels()
	set := map[string]bool{}
	for _, c := range all {
		if c.Group != group || c.Status != 1 {
			continue
		}
		if c.Models == "" || c.Models == "*" {
			// 通配渠道无法枚举，跳过
			continue
		}
		for _, m := range strings.Split(c.Models, ",") {
			if m = strings.TrimSpace(m); m != "" {
				set[m] = true
			}
		}
	}
	// 追加模型别名（alias.* 的键名，供用户直接调用）
	for name := range model.KVGetAll("alias.") {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = true
		}
	}
	data := make([]map[string]string, 0, len(set))
	for m := range set {
		data = append(data, map[string]string{"id": m, "object": "model", "owned_by": "gateway"})
	}
	sort.Slice(data, func(i, j int) bool { return data[i]["id"] < data[j]["id"] })
	json.NewEncoder(w).Encode(map[string]interface{}{
		"object": "list",
		"data":   data,
	})
}
