package handler

import (
	"api-gateway/model"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// target 表示一次转发目标（渠道 + 具体密钥）
type target struct {
	channel model.Channel
	key     string
}

// RelayHandler 仿 new-api 的模型路由转发：按 令牌分组 + 模型名 选择渠道
func RelayHandler(w http.ResponseWriter, r *http.Request) {
	key := extractToken(r)
	tok, err := model.GetToken(key)
	if err != nil {
		http.Error(w, "Invalid API Key", http.StatusUnauthorized)
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
		cost := maxInt64(1, cw.bytes/4)
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
		if lastErr == nil {
			respStatus = bw.status
			respHeader = bw.header
			// 把响应里的上游模型名还原为公开名
			respBody = rewriteResponseModel(bw.buf.Bytes(), upstream, modelName)
			break
		}
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

	// 计费：优先用响应中的 usage.total_tokens，否则按字节估算
	cost := parseUsage(respBody)
	if cost <= 0 {
		cost = maxInt64(1, int64(len(respBody))/4)
	}
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
		if c.Group != group || c.Status != 1 {
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
