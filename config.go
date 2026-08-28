package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config 服务级配置（首次运行自动创建；仅网络层配置，业务设置在运行时管理）
type Config struct {
	Listen     string `json:"listen"`
	DataDir    string `json:"data_dir"`
	SSLEnabled bool   `json:"ssl_enabled"` // 是否开启 HTTPS
	SSLCert    string `json:"ssl_cert"`    // TLS 证书路径（ssl_enabled 时必填）
	SSLKey     string `json:"ssl_key"`     // TLS 私钥路径（ssl_enabled 时必填）
}

const defaultConfig = `{
  "listen": "0.0.0.0:8080",
  "data_dir": "data",
  "ssl_enabled": false,
  "ssl_cert": "",
  "ssl_key": ""
}
`

// loadConfig 读取配置；不存在则自动创建默认配置
func loadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if werr := os.WriteFile(path, []byte(defaultConfig), 0o644); werr != nil {
				return cfg, werr
			}
			cfg.Listen = "0.0.0.0:8080"
			cfg.DataDir = "data"
			return cfg, nil
		}
		return cfg, err
	}
	if len(data) == 0 {
		_ = os.WriteFile(path, []byte(defaultConfig), 0o644)
		cfg.Listen = "0.0.0.0:8080"
		cfg.DataDir = "data"
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Listen == "" {
		cfg.Listen = "0.0.0.0:8080"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	return cfg, nil
}

// genConfig 生成默认配置文件
func genConfig(path string) error {
	return os.WriteFile(path, []byte(defaultConfig), 0o644)
}

func printHelp() {
	fmt.Print(`API 网关 (api-gateway) — 轻量 LLM / API 中转站

用法:
  gateway [选项]

选项:
  -h, --help          显示本帮助
  -c <path>          指定配置文件路径（默认 config.json）
  -gen-config        生成默认配置文件后退出

配置 (config.json) — 仅网络层配置:
  listen       监听地址，如 "0.0.0.0:8080"（默认）
  data_dir     数据存储目录，如 "data"
  ssl_enabled  是否开启 HTTPS（true/false）
  ssl_cert     TLS 证书路径（ssl_enabled 时必填）
  ssl_key      TLS 私钥路径（ssl_enabled 时必填）

说明:
  - 首次运行会自动创建 config.json、data/ 目录与 SQLite 数据库 gateway.db
  - 不预置默认管理员：系统无用户时，首个注册用户自动成为 root 超级管理员
  - 也可用环境变量 INIT_ROOT_USER / INIT_ROOT_PASSWORD 按需引导初始 root
  - 营业模式 / 开放注册 / 模型倍率等业务设置在后台管理（存 SQLite），不在配置文件中
  - 数据存于 data/gateway.db（users/tokens/channels/settings 等表）
`)
}
