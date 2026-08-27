# API 中转站 (api-gateway)

一个仿照 [new-api](https://github.com/Calcium-Ion/new-api) 思路、但**极简、轻量**的 API / LLM 中转网关。

- **纯 Go 实现**，无 CGO，交叉编译零依赖
- **极小体积**：编译产物仅约 3MB（new-api 动辄数十 MB）
- **极低内存**：常驻内存约 10MB 级别，无外部数据库进程
- **单文件运行**：一个二进制 + 一份 SQLite 数据文件（`data/gateway.db`）即可工作
- 支持反向代理、上游认证注入、限流、路径白名单、SSE 流式转发

> 定位：把多个上游（OpenAI / Anthropic / 自建服务等）聚合为一个统一入口，
> 通过前缀路由转发，并为每个渠道注入各自的鉴权信息。适合个人 / 小团队自建中转。

---

## 核心概念（仿 new-api）

本网关按 new-api 的思路组织，而非简单前缀转发：

- **渠道 Channel**：一个上游服务实例（如某个 OpenAI / Anthropic 账号）。含 `base_url`、多个上游密钥（轮询+故障转移）、支持的 `models`、所属 `group`、优先级/权重、限流。
- **令牌 Token**：面向用户的访问凭证。绑定到某个 `group`，按 `quota`（token 额度）计费；请求需携带 `Authorization: Bearer <token>`。
- **分组 Group**：令牌与渠道通过分组关联——某分组的令牌只会路由到同分组的渠道。
- **模型路由**：请求到达后，按 `令牌.group` + 请求体中的 `model` 选出支持的渠道，按优先级/权重挑选，再注入该渠道的密钥转发。

此外保留旧的「前缀路由 `/proxy/*`」模式，适合不走模型名的简单代理场景。

## 功能特性

| 功能 | 说明 |
| --- | --- |
| 账户体系 | 用户注册 / 登录（会话令牌）；`open_register` 控制是否开放注册；`root` 为超级管理员，普通用户只能查看 / 管理自己的令牌 |
| 权限分级 | 管理 API 与设置接口需 `root` 会话；普通用户仅能操作用自己创建的令牌 |
| 运营模式 | **自用 (self)**：请求直接转发、不计费；**营业 (biz)**：按 `model_ratio` 倍率计费，适合对外售卖额度 |
| 模型路由 | 按 `分组 + model` 选择渠道，支持优先级 / 权重 |
| 模型名映射 | 渠道可配 `model_mapping`，把公开模型名改写为上游模型名（响应自动还原） |
| 渠道多密钥 | 一个渠道可配多个上游密钥，请求间轮询，连接失败自动故障转移 |
| 令牌与额度 | 用户令牌按 `group` 关联渠道，按消耗 token 数计费（`usage` 或估算），可设额度 / 不限 |
| 上游认证注入 | `bearer` / `header` / `query` 三种方式自动注入密钥，并清除用户自身凭据 |
| 旧版前缀代理 | `/proxy/*` 仍可用，按路径前缀转发（兼容老用法） |
| 流式转发 | 支持 SSE / 流式响应（LLM 对话必备），边收边发 |
| 访问日志 | 每次转发写入 `access.log`（JSONL：状态码/耗时/消耗/尝试次数） |
| 全局设置 | `/api/setting` 管理运营模式 / 开放注册 / 模型倍率，持久化到 SQLite |
| 管理后台 | 内置静态页面，可视化增删改 渠道 / 令牌 / 路由 / 设置 |
| 配置持久化 | 关系型存储：`data/gateway.db`（SQLite，纯 Go 无 CGO），启动时载入内存，CRUD 落盘 |
| 双因素认证 2FA | TOTP（Google Authenticator），登录先验密码再验动态码 |
| 邮箱 / 找回密码 | SMTP 验证码绑定邮箱，忘记密码邮件重置 |
| 第三方 OAuth | GitHub / Google 一键登录（自动建号） |
| 通知 | 渠道自动禁用等事件推送到 Webhook / Telegram / 钉钉 / 飞书 / 邮件 |
| 安全守卫 | IP 白名单 / 黑名单（CIDR）+ 每 IP 每分钟限流 |
| 用量统计 | `/api/stats` 聚合看板（请求数 / 消耗 / 按模型 / 按用户 / 状态码）+ CSV 导出 |
| 模型别名 | `alias.*` 把公开名映射到真实模型，`/v1/models` 自动列出，转发时自动解析 |
| 容器部署 | 附带 Dockerfile / docker-compose.yml |

---

## 快速开始

```bash
# 编译（推荐加裁剪参数，进一步缩小体积）
go build -ldflags="-s -w" -o gateway .

# 首次运行会自动在 ./data 下生成 config.json 与 SQLite 数据库（gateway.db）；
# 旧版本遗留的各类 *.json 数据文件会在首次启动时自动迁移进 SQLite，并重命名为 *.json.bak；
# 可用 -gen-config 强制生成一份带注释的示例配置：
./gateway -gen-config

# 启动。默认读取当前目录 ./config.json，监听其中配置的地址（默认 :8090）
./gateway
# 也可通过环境变量 / 参数覆盖：
LISTEN=:8080 DATA_DIR=/data/gateway ./gateway
```

启动后：

- 管理后台：http://localhost:8090/（先登录 `root` / `123456`，首次使用请改密）
- 账户接口：`/api/user/login`、`/api/user/register`
- 管理 API：`/admin/channels`、`/admin/tokens`
- 转发入口：`/v1/*`（仿 new-api 的 OpenAI 兼容转发）、`/proxy/*`（旧版前缀代理）

> 默认内置管理员账号 `root` / `123456`，数据文件位于 `data/` 目录。
> 多管理员可在 `config.json` 的 `root_users` 中追加用户名（含默认 `root`）。

### 命令行参数

| 参数 | 说明 |
| --- | --- |
| `-h` / `--help` | 打印帮助信息后退出 |
| `-c <path>` | 指定配置文件路径（默认 `config.json`） |
| `-gen-config` | 生成示例 `config.json` 后退出 |

### 添加一条渠道（示例：转发到 OpenAI）

通过页面或直接调用 API（需先登录拿到 root 会话令牌）：

```bash
TOKEN=$(curl -s -X POST http://localhost:8090/api/user/login \
  -H "Content-Type: application/json" \
  -d '{"username":"root","password":"123456"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")

curl -X POST http://localhost:8090/admin/channels \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{
    "name": "OpenAI",
    "type": "openai",
    "base_url": "https://api.openai.com",
    "keys": "sk-xxxx",
    "models": "gpt-4o,gpt-4",
    "group": "default",
    "auth_type": "bearer",
    "status": 1
  }'
```

> 旧版 `/proxy/*` 前缀路由仍可用，添加方式见文末「配置说明」。

---

## 账户与权限

- **注册 / 登录**：`POST /api/user/register`、`POST /api/user/login` 返回会话令牌（Bearer）。
  是否允许公开注册由全局设置 `open_register` 控制；关闭后普通用户无法自助注册，需管理员创建。
- **会话**：后续请求在 `Authorization: Bearer <token>` 中携带会话令牌。
- **分级**：
  - `root`（超级管理员，见 `config.json` 的 `root_users`）：可管理全部渠道 / 令牌 / 全局设置。
  - 普通用户：仅能查看与删除**自己创建**的令牌，其余管理接口返回 403。

## 运营模式（self / biz）

通过 `PUT /api/setting` 切换：

- **自用 self**：请求直接转发，**不扣减额度、不计费**。适合个人 / 内网自用。
- **营业 biz**：按 `model_ratio`（模型倍率表）对每次消耗的 token 数计费；
  例如 `{"gpt-4o": 2}` 表示 `gpt-4o` 的消耗按 `原始token数 × 2` 计入令牌额度。
  倍率为空或 ≤0 时按 1 计。

计费来源：优先取上游响应 `usage.total_tokens`，缺失时按响应字节数估算；再叠加模式 / 倍率换算。

## 管理 API

> 完整的请求参数、响应字段与 curl 示例见下文「API 参考文档」章节。

除页面外，也可直接调用 REST API（管理 / 设置类接口需带 `Authorization: Bearer <root会话>`）：

- `POST /api/user/login` · `POST /api/user/register` · `POST /api/user/logout` · `GET /api/user/self`
- `GET/PUT /api/setting` —— 读取 / 更新全局设置（仅 root）
- `GET/POST /admin/channels` —— 渠道列表 / 新增
- `PUT/DELETE /admin/channels/{id}` —— 更新 / 删除渠道
- `GET/POST /admin/tokens` —— 令牌列表（root 看全部，用户只看自己的）/ 生成（POST 不传 `key` 则自动生成，记录 `owner`）
- `PUT/DELETE /admin/tokens/{key}` —— 更新 / 删除令牌
- `GET/POST /admin/routes` —— 旧版前缀路由列表 / 新增
- `PUT/DELETE /admin/routes/{id}` —— 更新 / 删除路由

### 快速搭建（仿 new-api）

1. 在「渠道」页添加一个渠道：`base_url` 填上游（如 `https://api.openai.com`），`models` 填支持的模型（如 `gpt-4o`，多个逗号分隔，`*` 表示全部），`keys` 填上游密钥（多个逗号分隔轮流），`group` 填 `default`。
2. 在「令牌」页生成令牌，分组同样填 `default`，可设额度（token 数，`-1` 不限）。
3. 用户即可用 OpenAI 兼容方式调用：

```bash
curl https://你的网关/v1/chat/completions \
  -H "Authorization: Bearer <令牌>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}'
```

网关会按 `分组 + model` 选渠道、注入上游密钥、扣减令牌额度，并把响应（含 `usage`）原样返回。

---

## 架构

```
            ┌────────────┐
  client ──▶│  /proxy/*  │──▶ 前缀匹配（内存缓存）
            └────────────┘        │
                                  ▼
                          ┌───────────────┐
                          │  反向代理转发   │  注入上游认证 / 限流 / 超时
                          └───────────────┘
                                  │
                                  ▼
                            upstream API

  /admin/routes ──▶ 管理 API ──▶ 读写 data/routes.json ──▶ 刷新内存缓存
```

| 包 | 职责 |
| --- | --- |
| `main.go` | 解析参数/配置、初始化存储、加载缓存、注册路由、启动 HTTP 服务 |
| `model` | `Route` / `Channel` / `Token` / `User` / `Setting` 结构体 + 内存存储 + JSON 持久化 |
| `cache` | 路由前缀匹配的内存缓存（最长前缀优先）；渠道 / 令牌运行时缓存 |
| `handler/proxy.go` | 旧版 `/proxy/*` 转发逻辑（认证注入、限流、流式） |
| `handler/relay.go` | 仿 new-api 的 `/v1/*` 模型路由转发（分组+模型选渠道、计费） |
| `handler/admin.go` | 渠道 / 令牌 / 设置 管理 API |
| `handler/auth.go` | 用户注册 / 登录 / 会话 |
| `static` | 内置管理后台页面（embed 嵌入二进制，含登录 / 渠道 / 令牌 / 设置） |

### 为什么这么轻？

- **无数据库**：用 `data/routes.json` 文件存储配置，启动时载入内存，CRUD 时落盘。
  去掉了 `modernc.org/sqlite` 这类庞大的依赖，二进制从 ~15MB 降到 ~3MB。
- **无框架**：直接使用 Go 标准库 `net/http` + `httputil.ReverseProxy`。
- **无 CGO**：纯 Go 编译，可直接 `GOOS=linux GOARCH=arm64` 交叉编译到树莓派 / 服务器。
- **连接复用**：共享 `http.Transport`，长连接复用，内存稳定。

---

## 配置说明（Route 字段）

| 字段 | 含义 |
| --- | --- |
| `name` | 路由名称（展示用） |
| `prefix` | 匹配前缀，如 `/v1` |
| `upstream_url` | 上游基地址，如 `https://api.openai.com` |
| `auth_type` | `none` / `bearer` / `header` / `query` |
| `auth_key` | header 或 query 的键名 |
| `auth_value` | 认证值 / token（单密钥） |
| `auth_values` | 多个上游密钥，逗号分隔，请求间轮询（故障转移） |
| `timeout` | 上游超时（秒），默认 30 |
| `need_api_key` | 是否要求入站请求带 `X-API-Key` |
| `allowed_paths` | 逗号分隔的白名单路径，为空表示全部放行 |
| `rate_limit` | 每分钟最大请求数，0 表示不限 |
| `enable` | 是否启用 |

---

## 配置文件 `config.json`

首次启动若文件不存在会自动创建（默认 `data` 目录，可用 `DATA_DIR` 或 `-c` 指定）。示例：

```json
{
  "listen": ":8090",
  "data_dir": "data",
  "root_users": ["root"],
  "setting": {
    "mode": "self",
    "open_register": true,
    "model_ratio": {}
  }
}
```

| 字段 | 含义 |
| --- | --- |
| `listen` | 监听地址，默认 `:8090` |
| `data_dir` | 数据目录（各类 JSON 存放处），默认 `data`；可通过环境变量 `DATA_DIR` 覆盖 |
| `root_users` | 超级管理员用户名列表（默认含 `root`） |
| `setting.mode` | `self` 自用 / `biz` 营业 |
| `setting.open_register` | 是否允许公开注册 |
| `setting.model_ratio` | 模型倍率表（营业计费用） |

数据文件均落在 `data_dir` 下：`config.json`、`channels.json`、`tokens.json`、`routes.json`、`users.json`、`setting.json`、`access.log`。

## 环境变量

| 变量 | 说明 |
| --- | --- |
| `LISTEN` | 监听地址，覆盖 `config.json` 中的 `listen`（默认 `:8090`） |
| `DATA_DIR` | 数据目录，覆盖 `config.json` 中的 `data_dir`（默认 `data`） |

> 鉴权采用账户会话体系：用 `root` 登录后拿到的会话令牌作为管理凭证，已取代旧版的 `ADMIN_TOKEN`。

---

## 构建优化

```bash
# 裁剪符号表与调试信息，并启用 UPX（若已安装）可进一步压缩到 ~1MB
go build -ldflags="-s -w" -o gateway .
upx --best gateway
```

---

## 路线图（后续轻量增强）

- [x] 多上游 Key 轮询 / 故障转移
- [x] 请求 / Token 用量统计与配额
- [x] 模型名映射（如把 `gpt-4o` 映射到上游私有模型）
- [x] 账户体系（注册 / 登录 / 会话、root 与普通用户分级）
- [x] 运营模式（self 自用 / biz 营业 + 模型倍率计费）
- [x] 访问日志 / 可观测性
- [ ] 令牌额度告警 / 用量报表
- [ ] 渠道自动禁用后的恢复探针可视化

---

## API 参考文档

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


## License

Apache License 2.0（沿用上游 new-api 的许可证）
