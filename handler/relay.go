package handler

import (
	"api-gateway/model"
	"bytes"
	"encoding/json"
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
	failureThreshold     = 5                // 连续失败达到该次数则自动禁用渠道
	autoRecoverCooldown  = 60 * time.Second // 自动禁用后多久允许试探性恢复
)

var (
	autoDisabled sync.Map // channelID -> time.Time（被自动禁用的时刻）
	failCount    sync.Map // channelID -> *int64（连续失败计数）
)

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
	group := tok.Group
	if group == "" {
		group = "default"
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

	// 令牌级模型白名单校验
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
		cost := maxInt64(1, cw.bytes/4)
		cost = model.ModelCost(modelName, cost, 0)
		_ = model.UseToken(key, cost)
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

	// 计费：按 usage 中的 prompt/completion 分别乘以倍率（营业模式）；
	// 若上游仅给出 total_tokens，则整体按提示词计费
	prompt, comp := parseUsageParts(respBody)
	if prompt == 0 && comp == 0 {
		if total := parseUsage(respBody); total > 0 {
			prompt = total
		}
	}
	if prompt == 0 && comp == 0 {
		prompt = maxInt64(1, int64(len(respBody))/4)
	}
	cost := model.ModelCost(modelName, prompt, comp)
	_ = model.UseToken(key, cost)

	logAccess(map[string]interface{}{
		"time": start.Format(time.RFC3339), "method": r.Method, "path": r.URL.Path,
		"model": modelName, "group": group, "token": maskToken(key),
		"status": respStatus, "stream": false, "cost": cost,
		"duration": time.Since(start).Milliseconds(),
	})
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
