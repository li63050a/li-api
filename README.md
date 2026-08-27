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

## 功能特性

| 功能 | 说明 |
| --- | --- |
| 前缀路由 | 按请求路径前缀匹配到不同上游（如 `/v1` → OpenAI，`/anthropic` → Claude） |
| 上游认证注入 | 支持 `none` / `bearer` / `header` / `query` 四种方式自动注入密钥 |
| 入站校验 | 可选要求请求携带 `X-API-Key` 才允许转发 |
| 限流 | 按路由维度限制每分钟请求数 |
| 路径白名单 | 可选限制该路由只转发指定路径 |
| 流式转发 | 支持 SSE / 流式响应（LLM 对话必备），边收边发 |
| 管理后台 | 内置静态页面，可视化增删改路由 |
| 配置持久化 | 路由存于 `data/routes.json`，热更新内存缓存 |

---

## 快速开始

```bash
# 编译（推荐加裁剪参数，进一步缩小体积）
go build -ldflags="-s -w" -o gateway .

# 可选：设置管理后台口令（不设置则任何人可管理）
export ADMIN_TOKEN=your-secret

# 启动，默认监听 :8080
./gateway
```

启动后：

- 管理后台：http://localhost:8080/
- 管理 API：`/admin/routes`
- 转发入口：`/proxy/<你的前缀>/...`

### 添加一条路由（示例：转发到 OpenAI）

通过页面或直接调用 API：

```bash
curl -X POST http://localhost:8080/admin/routes \
  -H "Content-Type: application/json" \
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

之后访问 `http://localhost:8080/proxy/v1/chat/completions` 即等价于访问上游对应接口，
并由网关自动带上 `Authorization: Bearer sk-xxxx`。

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
| `main.go` | 初始化存储、加载缓存、注册路由、启动 HTTP 服务 |
| `model` | `Route` 结构体 + 内存存储 + JSON 持久化 |
| `cache` | 路由前缀匹配的内存缓存（最长前缀优先） |
| `handler/proxy.go` | 核心转发逻辑（认证注入、限流、流式） |
| `handler/admin.go` | 路由 CRUD 管理 API |
| `static` | 内置管理后台页面（embed 嵌入二进制） |

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
| `auth_value` | 认证值 / token |
| `timeout` | 上游超时（秒），默认 30 |
| `need_api_key` | 是否要求入站请求带 `X-API-Key` |
| `allowed_paths` | 逗号分隔的白名单路径，为空表示全部放行 |
| `rate_limit` | 每分钟最大请求数，0 表示不限 |
| `enable` | 是否启用 |

---

## 环境变量

| 变量 | 说明 |
| --- | --- |
| `ADMIN_TOKEN` | 管理后台 / API 访问口令；设置后必须携带（Header `X-Admin-Token` 或参数 `?token=`） |
| `LISTEN` | 监听地址，默认 `:8080` |
| `DATA_DIR` | 数据目录（路由 JSON 存放处），默认 `data` |

---

## 构建优化

```bash
# 裁剪符号表与调试信息，并启用 UPX（若已安装）可进一步压缩到 ~1MB
go build -ldflags="-s -w" -o gateway .
upx --best gateway
```

---

## 路线图（后续轻量增强）

- [ ] 多上游 Key 轮询 / 故障转移
- [ ] 请求 / Token 用量统计与配额
- [ ] 模型名映射（如把 `gpt-4o` 映射到上游私有模型）
- [ ] 简单令牌账号体系（对标 new-api 的 key / 余额）
- [ ] 访问日志 / 可观测性

---

## License

Apache License 2.0（沿用上游 new-api 的许可证）
