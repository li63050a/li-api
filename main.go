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

    // 初始加载缓存
    if err := cache.Refresh(); err != nil {
        log.Fatal("Cache init:", err)
    }

    // 路由设置
    // 静态文件服务（Web 界面）
    staticSub, _ := fs.Sub(staticFS, "static")
    http.Handle("/", http.FileServer(http.FS(staticSub)))

    // 管理 API
    http.HandleFunc("/admin/routes", handler.AdminHandler)
    http.HandleFunc("/admin/routes/", handler.AdminHandler) // 处理带 ID 的路径
    http.HandleFunc("/admin/tokens", handler.TokenHandler)
    http.HandleFunc("/admin/tokens/", handler.TokenHandler) // 处理带 key 的路径
    http.HandleFunc("/admin/channels", handler.ChannelHandler)
    http.HandleFunc("/admin/channels/", handler.ChannelHandler) // 处理带 ID 的路径

    // 仿 new-api 的模型路由转发（OpenAI 兼容 /v1/*）
    http.HandleFunc("/v1/", handler.RelayHandler)

    // 核心转发（所有 /proxy/ 请求）
    http.HandleFunc("/proxy/", handler.ProxyHandler)

    // 启动服务
    listen := os.Getenv("LISTEN")
    if listen == "" {
        listen = ":8080"
    }
    log.Println("🚀 API Gateway started on http://" + listen)
    log.Fatal(http.ListenAndServe(listen, nil))
}