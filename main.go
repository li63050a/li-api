package main

import (
	"api-gateway/cache"
	"api-gateway/handler"
	"api-gateway/model"
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed static
var staticFS embed.FS

// Version 当前版本号（按 0.0.x 依次递增）
const Version = "0.0.1.1"

func main() {
	log.Println("api-gateway", Version, "starting ...")
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
	if err := model.InitRedemptions(); err != nil {
		log.Fatal("Redemption store init:", err)
	}

	// 初始加载缓存
	if err := cache.Refresh(); err != nil {
		log.Fatal("Cache init:", err)
	}

	// 后台定时任务（套餐每日赠送 / 渠道健康巡检 / 统计快照）
	handler.StartScheduler()

	// 静态文件服务（Web 界面）
	staticSub, _ := fs.Sub(staticFS, "static")
	http.Handle("/", http.FileServer(http.FS(staticSub)))

	// 账户 / 会话（挂安全响应头 + CSRF 校验）
	http.HandleFunc("/api/user/login", handler.SecurityHeaders(handler.CSRFCheck(handler.LoginHandler)))
	http.HandleFunc("/api/user/register", handler.SecurityHeaders(handler.CSRFCheck(handler.RegisterHandler)))
	http.HandleFunc("/api/user/logout", handler.SecurityHeaders(handler.CSRFCheck(handler.LogoutHandler)))
	http.HandleFunc("/api/user/self", handler.SecurityHeaders(handler.CSRFCheck(handler.SelfHandler)))
	http.HandleFunc("/api/setting", handler.SecurityHeaders(handler.CSRFCheck(handler.SettingHandler)))
	http.HandleFunc("/api/dashboard", handler.SecurityHeaders(handler.CSRFCheck(handler.DashboardHandler)))
	http.HandleFunc("/api/model_presets", handler.SecurityHeaders(handler.CSRFCheck(handler.ModelPresetsHandler)))
	http.HandleFunc("/api/redemption/redeem", handler.SecurityHeaders(handler.CSRFCheck(handler.RedeemHandler)))
	http.HandleFunc("/api/logs", handler.SecurityHeaders(handler.CSRFCheck(handler.LogsHandler)))
	http.HandleFunc("/api/setting/test_email", handler.SecurityHeaders(handler.CSRFCheck(handler.TestEmailHandler)))

	// 管理 API（挂安全响应头 + CSRF 校验 + 维护模式 + 安全守卫）
	http.HandleFunc("/admin/routes", handler.SecurityHeaders(handler.CSRFCheck(handler.MaintenanceMiddleware(handler.GuardMiddleware(handler.AdminHandler)))))
	http.HandleFunc("/admin/routes/", handler.SecurityHeaders(handler.CSRFCheck(handler.MaintenanceMiddleware(handler.GuardMiddleware(handler.AdminHandler)))))
	http.HandleFunc("/admin/tokens", handler.SecurityHeaders(handler.CSRFCheck(handler.MaintenanceMiddleware(handler.GuardMiddleware(handler.TokenHandler)))))
	http.HandleFunc("/admin/tokens/", handler.SecurityHeaders(handler.CSRFCheck(handler.MaintenanceMiddleware(handler.GuardMiddleware(handler.TokenHandler)))))
	http.HandleFunc("/admin/channels", handler.SecurityHeaders(handler.CSRFCheck(handler.MaintenanceMiddleware(handler.GuardMiddleware(handler.ChannelHandler)))))
	http.HandleFunc("/admin/channels/", handler.SecurityHeaders(handler.CSRFCheck(handler.MaintenanceMiddleware(handler.GuardMiddleware(handler.ChannelHandler)))))
	http.HandleFunc("/admin/channels/test/", handler.SecurityHeaders(handler.CSRFCheck(handler.MaintenanceMiddleware(handler.GuardMiddleware(handler.ChannelTestHandler)))))
	http.HandleFunc("/admin/users", handler.SecurityHeaders(handler.CSRFCheck(handler.MaintenanceMiddleware(handler.GuardMiddleware(handler.UsersHandler)))))
	http.HandleFunc("/admin/users/", handler.SecurityHeaders(handler.CSRFCheck(handler.MaintenanceMiddleware(handler.GuardMiddleware(handler.UsersHandler)))))
	http.HandleFunc("/admin/redemptions", handler.SecurityHeaders(handler.CSRFCheck(handler.MaintenanceMiddleware(handler.GuardMiddleware(handler.RedemptionHandler)))))
	http.HandleFunc("/admin/redemptions/", handler.SecurityHeaders(handler.CSRFCheck(handler.MaintenanceMiddleware(handler.GuardMiddleware(handler.RedemptionHandler)))))
	http.HandleFunc("/admin/model_redirects", handler.SecurityHeaders(handler.CSRFCheck(handler.MaintenanceMiddleware(handler.GuardMiddleware(handler.RedirectHandler)))))

	// 模型路由转发（OpenAI 兼容 /v1/*，挂安全响应头 + 请求计数 + 维护模式 + 敏感词审查 + 安全守卫）
	http.HandleFunc("/v1/", handler.SecurityHeaders(handler.CountMiddleware(handler.MaintenanceMiddleware(handler.SensitiveMiddleware(handler.GuardMiddleware(handler.RelayHandler))))))

	// 启动服务（支持 HTTPS / 优雅停机 / SIGHUP 热重载缓存）
	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = cfg.Listen
	}
	srv := &http.Server{Addr: listen, Handler: nil}
	go func() {
		if cfg.SSLEnabled {
			if cfg.SSLCert == "" || cfg.SSLKey == "" {
				log.Fatal("ssl_enabled=true 但 ssl_cert / ssl_key 未配置")
			}
			log.Println("🚀 API Gateway started on https://" + listen)
			if err := srv.ListenAndServeTLS(cfg.SSLCert, cfg.SSLKey); err != nil && err != http.ErrServerClosed {
				log.Fatal(err)
			}
			return
		}
		log.Println("🚀 API Gateway started on http://" + listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	for s := range sig {
		if s == syscall.SIGHUP {
			log.Println("SIGHUP: 热重载缓存 ...")
			if err := cache.Refresh(); err != nil {
				log.Println("reload error:", err)
			} else {
				log.Println("reload done")
			}
			continue
		}
		break
	}
	log.Println("shutting down ...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Println("shutdown error:", err)
	}
	log.Println("bye")
}
