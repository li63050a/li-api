package main

import (
	"api-gateway/cache"
	"api-gateway/handler"
	"api-gateway/model"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
)

//go:embed static
var staticFS embed.FS

func main() {
	// 解析命令行参数
	configPath := "config.json"
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			printHelp()
			return
		case "-gen-config":
			if err := genConfig(configPath); err != nil {
				log.Fatal(err)
			}
			log.Println("已生成配置文件:", configPath)
			return
		case "-c":
			if i+1 < len(args) {
				configPath = args[i+1]
				i++
			}
		}
	}

	// 加载配置（不存在则自动创建）
	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatal("Config load:", err)
	}
	// DATA_DIR 环境变量优先于配置文件中的 data_dir；均未设置时使用默认 "data"
	if env := os.Getenv("DATA_DIR"); env != "" {
		cfg.DataDir = env
	}
	_ = os.Setenv("DATA_DIR", cfg.DataDir)

	// 初始化存储
	if err := model.Init(); err != nil {
		log.Fatal("Store init:", err)
	}
	if err := model.InitTokens(); err != nil {
		log.Fatal("Token store init:", err)
	}
	if err := model.InitChannels(); err != nil {
		log.Fatal("Channel store init:", err)
	}
	if err := model.InitSettings(); err != nil {
		log.Fatal("Setting store init:", err)
	}
	if err := model.InitUsers(); err != nil {
		log.Fatal("User store init:", err)
	}

	// 初始加载缓存
	if err := cache.Refresh(); err != nil {
		log.Fatal("Cache init:", err)
	}

	// 静态文件服务（Web 界面）
	staticSub, _ := fs.Sub(staticFS, "static")
	http.Handle("/", http.FileServer(http.FS(staticSub)))

	// 账户 / 会话
	http.HandleFunc("/api/user/login", handler.LoginHandler)
	http.HandleFunc("/api/user/register", handler.RegisterHandler)
	http.HandleFunc("/api/user/logout", handler.LogoutHandler)
	http.HandleFunc("/api/user/self", handler.SelfHandler)
	http.HandleFunc("/api/setting", handler.SettingHandler)

	// 管理 API
	http.HandleFunc("/admin/routes", handler.AdminHandler)
	http.HandleFunc("/admin/routes/", handler.AdminHandler)
	http.HandleFunc("/admin/tokens", handler.TokenHandler)
	http.HandleFunc("/admin/tokens/", handler.TokenHandler)
	http.HandleFunc("/admin/channels", handler.ChannelHandler)
	http.HandleFunc("/admin/channels/", handler.ChannelHandler)

	// 仿 new-api 的模型路由转发（OpenAI 兼容 /v1/*）
	http.HandleFunc("/v1/", handler.RelayHandler)

	// 启动服务
	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = cfg.Listen
	}
	log.Println("🚀 API Gateway started on http://" + listen)
	log.Fatal(http.ListenAndServe(listen, nil))
}
