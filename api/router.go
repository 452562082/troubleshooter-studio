package api

import (
	"embed"
	"io/fs"
	"net/http"
)

// NewRouter 创建 HTTP 路由：/api/* → Go handler，其他 → 前端静态文件
// webFS 是 embed 的前端 dist 目录；传 nil 时只提供 API（开发模式由 Vite proxy 处理前端）
func NewRouter(srv *Server, webFS *embed.FS) http.Handler {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("POST /api/validate", srv.HandleValidate)
	mux.HandleFunc("POST /api/plan", srv.HandlePlan)
	mux.HandleFunc("POST /api/gen", srv.HandleGen)
	mux.HandleFunc("POST /api/doctor", srv.HandleDoctor)
	mux.HandleFunc("GET /api/schema", srv.HandleSchema)

	// 前端静态文件（生产模式：embed；开发模式：Vite dev server proxy）
	if webFS != nil {
		distFS, err := fs.Sub(webFS, "web/dist")
		if err == nil {
			fileServer := http.FileServer(http.FS(distFS))
			mux.Handle("/", spaHandler(fileServer))
		}
	}

	return corsMiddleware(mux)
}

// spaHandler 让所有非 /api 且非静态文件的请求回退到 index.html（SPA 路由）
func spaHandler(fileServer http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 先尝试静态文件
		fileServer.ServeHTTP(w, r)
	})
}

// corsMiddleware 开发模式允许 Vite dev server 跨域
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
