package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config 服务级配置（首次运行自动创建）
type Config struct {
	Listen  string `json:"listen"`
	DataDir string `json:"data_dir"`
}

const defaultConfig = `{
  "listen": ":8080",
  "data_dir": "data"
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
			cfg.Listen = ":8080"
			cfg.DataDir = "data"
			return cfg, nil
		}
		return cfg, err
	}
	if len(data) == 0 {
		_ = os.WriteFile(path, []byte(defaultConfig), 0o644)
		cfg.Listen = ":8080"
		cfg.DataDir = "data"
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
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
	fmt.Print(`API 网关 (api-gateway) — 轻量 LLM / API 中转站（仿 new-api）

用法:
  gateway [选项]

选项:
  -h, --help          显示本帮助
  -c <path>          指定配置文件路径（默认 config.json）
  -gen-config        生成默认配置文件后退出

配置 (config.json):
  listen    监听地址，如 ":8080"
  data_dir  数据存储目录，如 "data"

说明:
  - 首次运行会自动创建 config.json 与 data/ 目录及各数据文件
  - 管理后台默认账号 root / 123456（密码以哈希存储，可在后台修改）
  - 路由配置存于 data/ 下（routes.json / channels.json / tokens.json / user.json / setting.json）
`)
}
