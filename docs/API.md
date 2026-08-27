# API 参考文档

本文档给出 api-gateway 全部 HTTP 接口的**请求参数**与 **curl 示例**。所有管理 / 设置类接口都需要在
`Authorization` 头中携带 `root` 用户的会话令牌（先通过 `/api/user/login` 获取）。

通用约定：

- 认证头：`Authorization: Bearer <token>`。管理接口也兼容 `X-Admin-Token: <token>`。
- 请求 / 响应体均为 JSON，字符集 `utf-8`。
- 时间戳为 RFC3339（`2026-08-27T14:09:22Z`）。
- 错误时返回形如 `Invalid ...` 的纯文本 + 对应状态码（401 / 403 / 400 / 404 / 500 / 502 等）。

---

## 1. 账户（/api/user/*）

### 1.1 注册 `POST /api/user/register`

仅在全局设置 `open_register = true` 时开放；否则返回 `403`。

请求体：

```json
{ "username": "alice", "password": "secret123" }
```

- `username` 至少 3 位，`password` 至少 6 位，否则 `400`。

响应 `200`：

```json
{ "role": "user", "token": "5f92fc1f...", "username": "alice" }
```

已存在同名用户返回 `400`（`username already exists`）。

### 1.2 登录 `POST /api/user/login`

```bash
curl -X POST http://localhost:8090/api/user/login \
  -H "Content-Type: application/json" \
  -d '{"username":"root","password":"123456"}'
```

响应 `200`：

```json
{ "token": "3df33259...", "username": "root", "role": "root" }
```

凭据错误返回 `401`。

### 1.3 登出 `POST /api/user/logout`

```bash
curl -X POST http://localhost:8090/api/user/logout \
  -H "Authorization: Bearer <token>"
```

返回 `204 No Content`，并使该会话失效。

### 1.4 当前用户 `GET /api/user/self`

```bash
curl http://localhost:8090/api/user/self -H "Authorization: Bearer <token>"
```

响应 `200`：

```json
{ "username": "root", "role": "root" }
```

---

## 2. 全局设置（/api/setting，仅 root）

### 2.1 读取 `GET /api/setting`

```bash
curl http://localhost:8090/api/setting -H "Authorization: Bearer <root-token>"
```

响应 `200`：

```json
{ "mode": "self", "open_register": true, "model_ratio": {} }
```

### 2.2 更新 `PUT /api/setting`

```bash
curl -X PUT http://localhost:8090/api/setting \
  -H "Authorization: Bearer <root-token>" \
  -H "Content-Type: application/json" \
  -d '{
    "mode": "biz",
    "open_register": false,
    "model_ratio": { "gpt-4o": 2, "claude-3-5-sonnet": 3 }
  }'
```

字段说明：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `mode` | string | `self` 自用（不计费）/ `biz` 营业（按倍率计费） |
| `open_register` | bool | 是否允许公开注册 |
| `model_ratio` | object | 模型名 → 倍率。营业模式下，消耗 `原始token数 × 倍率` 计入令牌额度；缺失或 ≤0 按 1 计 |

响应为更新后的完整 Setting。

---

## 3. 渠道（/admin/channels，仅 root）

### 3.1 列表 `GET /admin/channels`

```bash
curl http://localhost:8090/admin/channels -H "Authorization: Bearer <root-token>"
```

响应 `200`：`Channel` 数组，单条示例：

```json
{
  "id": 1,
  "name": "OpenAI",
  "type": "openai",
  "base_url": "https://api.openapi.com",
  "keys": "sk-aaa,sk-bbb",
  "auth_type": "bearer",
  "auth_key": "",
  "models": "gpt-4o,gpt-4",
  "model_mapping": "{\"gpt-4o\":\"gpt-4o-0806\"}",
  "group": "default",
  "priority": 0,
  "weight": 1,
  "rate_limit": 0,
  "status": 1,
  "created_at": "2026-08-27T14:09:22Z",
  "updated_at": "2026-08-27T14:09:22Z"
}
```

### 3.2 新增 `POST /admin/channels`

```bash
curl -X POST http://localhost:8090/admin/channels \
  -H "Authorization: Bearer <root-token>" -H "Content-Type: application/json" \
  -d '{
    "name": "OpenAI",
    "type": "openai",
    "base_url": "https://api.openai.com",
    "keys": "sk-aaa,sk-bbb",
    "auth_type": "bearer",
    "models": "gpt-4o,gpt-4",
    "model_mapping": "{\"gpt-4o\":\"gpt-4o-0806\"}",
    "group": "default",
    "priority": 0,
    "weight": 1,
    "rate_limit": 0,
    "status": 1
  }'
```

返回创建后的完整 `Channel`（含 `id`）。

### 3.3 更新 `PUT /admin/channels/{id}`

```bash
curl -X PUT http://localhost:8090/admin/channels/1 \
  -H "Authorization: Bearer <root-token>" -H "Content-Type: application/json" \
  -d '{"name":"OpenAI-New","status":0}'
```

### 3.4 删除 `DELETE /admin/channels/{id}`

```bash
curl -X DELETE http://localhost:8090/admin/channels/1 -H "Authorization: Bearer <root-token>"
```

---

## 4. 令牌（/admin/tokens）

- **root**：可查看 / 管理全部令牌。
- **普通用户**：仅能查看 / 删除**自己创建**的令牌（`owner` 为自己），可为自己生成新令牌。

### 4.1 列表 `GET /admin/tokens`

```bash
curl http://localhost:8090/admin/tokens -H "Authorization: Bearer <token>"
```

响应 `200`：`Token` 数组，单条示例：

```json
{
  "key": "5128718c...",
  "name": "alice-tok",
  "owner": "alice",
  "group": "default",
  "quota": 1000,
  "used": 36,
  "unlimited": 0,
  "status": 1,
  "created_at": "2026-08-27T14:10:00Z",
  "updated_at": "2026-08-27T14:12:00Z"
}
```

- `quota`：额度（token 数），`-1` 表示不限（此时 `unlimited` 应为 `1`）。
- `used`：已消耗。

### 4.2 生成 `POST /admin/tokens`

```bash
curl -X POST http://localhost:8090/admin/tokens \
  -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
  -d '{"name":"alice-tok","group":"default","quota":1000,"unlimited":0}'
```

- 不传 `key` 时自动生成 32 位随机密钥。
- 普通用户生成时，`owner` 自动设为自己的用户名。
- `unlimited=1` 时 `quota` 被忽略（视为不限）。

返回完整 `Token`（含 `key`）。**请务必保存返回的 `key`，它只在此处出现一次。**

### 4.3 更新 `PUT /admin/tokens/{key}`

```bash
curl -X PUT http://localhost:8090/admin/tokens/<key> \
  -H "Authorization: Bearer <root-token>" -H "Content-Type: application/json" \
  -d '{"status":0,"quota":5000}'
```

### 4.4 删除 `DELETE /admin/tokens/{key}`

```bash
curl -X DELETE http://localhost:8090/admin/tokens/<key> -H "Authorization: Bearer <token>"
```

---

## 5. 旧版前缀路由（/admin/routes，仅 root）

用于不走模型名的简单代理（兼容老用法）。

### 5.1 列表 / 新增 `GET|POST /admin/routes`

```bash
curl -X POST http://localhost:8090/admin/routes \
  -H "Authorization: Bearer <root-token>" -H "Content-Type: application/json" \
  -d '{
    "name": "OpenAI",
    "prefix": "/v1",
    "upstream_url": "https://api.openai.com",
    "auth_type": "bearer",
    "auth_value": "sk-xxxx",
    "timeout": 60,
    "enable": true
  }'
```

### 5.2 更新 / 删除 `PUT|DELETE /admin/routes/{id}`

请求体字段见 `Route` 结构（名称、前缀、上游地址、认证方式、限流、是否需令牌、路径白名单等）。

---

## 6. 模型路由转发（/v1/*，仿 new-api）

面向用户的 OpenAI 兼容入口。需携带用户令牌：

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Authorization: Bearer <用户令牌>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}'
```

行为：

1. 校验令牌（无效 / 禁用 / 额度不足分别返回 `401` / `403` / `403`）。
2. 按 `令牌.group` + 请求体 `model` 在**启用**渠道中筛选，按优先级 + 权重挑选，并展开多密钥轮询。
3. 注入该渠道的密钥（`bearer` / `header` / `query`），清除用户自身凭据，转发到上游。
4. 上游不可用时自动故障转移到下一个密钥 / 渠道；全部失败返回 `502`。
5. 计费：优先取上游 `usage.total_tokens`，缺失时按响应字节估算；再按 `mode` / `model_ratio` 换算后扣减令牌额度。
6. 流式（`stream:true`）边收边发（SSE）。

其它端点：

- `GET /v1/models`：返回该分组下所有渠道支持的模型列表。
- 无匹配渠道 / 模型时返回 `502`（`No available channel for model: ...`）。

---

## 7. 旧版前缀代理（/proxy/*）

```bash
curl http://localhost:8090/proxy/v1/chat/completions \
  -H "Authorization: Bearer <用户令牌>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[]}'
```

按 `/proxy` 之后的路径前缀匹配 `Route`（最长前缀优先），注入 `Route` 配置的上游认证后转发；
若 `Route.need_api_key=true` 则额外校验入站用户令牌并扣减额度。

---

## 8. 错误码速查

| 状态码 | 含义 |
| --- | --- |
| `400` | 请求体非法 / 参数不满足约束（如用户名或密码过短） |
| `401` | 未登录 / 令牌无效 / 已禁用 |
| `403` | 无权限（非 root 访问管理接口、注册已关闭、额度不足） |
| `404` | 资源不存在（渠道 / 令牌 / 路由 ID 错） |
| `502` | 上游无可用渠道，或上游连接 / 响应错误（故障转移后仍失败） |
| `500` | 服务端内部错误（如配置落盘失败） |
