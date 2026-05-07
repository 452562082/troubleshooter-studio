package main

import (
	"flag"
	"fmt"

	"net/http"

	"github.com/xiaolong/troubleshooter-studio/api"
	"github.com/xiaolong/troubleshooter-studio/internal/webui"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8080, "HTTP 监听端口")
	tmplDir := fs.String("t", "", "模板根目录 (默认: 可执行文件旁的 templates/)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tr := *tmplDir
	if tr == "" {
		tr = resolveTemplateDir()
	}
	srv := &api.Server{TemplateRoot: tr}
	router := api.NewRouter(srv, webui.Distribution())
	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("Web UI: http://localhost%s\n", addr)
	fmt.Printf("API:    http://localhost%s/api/\n", addr)
	fmt.Println("前端开发模式: cd web && npm run dev （Vite 会 proxy /api 到此端口）")
	fmt.Println("Ctrl+C 退出")
	return http.ListenAndServe(addr, router)
}

// runDemo 是"零配置试跑":用内置 examples/shop-troubleshooter.yaml + examples/fake-repos 跑完整
// pipeline（validate → analyze → plan → gen），产出一个临时目录，打印产物树 + 关键下一步。
// 目的是让新用户 30 秒内看到 tshoot 能干什么，无需准备任何输入。
