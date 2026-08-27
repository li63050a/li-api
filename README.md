# API 中转站 (api-gateway)

一个仿照 [new-api](https://github.com/Calcium-Ion/new-api) 思路、但**极简、轻量**的 API / LLM 中转网关。

- **纯 Go 实现**，无 CGO，交叉编译零依赖
- **极小体积**：编译产物仅约 3MB（new-api 动辄数十 MB）
- **极低内存**：常驻内存约 10MB 级别，无外部数据库进程
- **单文件运行**：一个二进制 + 一份 `data/routes.json` 即可工作
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
| 全局设置 | `/api/setting` 管理运营模式 / 开放注册 / 模型倍率，持久化到 `config.json` |
| 管理后台 | 内置静态页面，可视化增删改 渠道 / 令牌 / 路由 / 设置 |
| 配置持久化 | `config.json` / `channels.json` / `tokens.json` / `routes.json`，热更新内存缓存 |

---

## 快速开始

```bash
# 编译（推荐加裁剪参数，进一步缩小体积）
go build -ldflags="-s -w" -o gateway .

# 首次运行会自动在 ./data 下生成 config.json 与各类数据文件；
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

## License

Apache License 2.0（沿用上游 new-api 的许可证）
