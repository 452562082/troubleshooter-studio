package api

import (
	"io"
	"io/fs"
	"net/http"
	"strings"
)

// NewRouter 创建 HTTP 路由：/api/* → Go handler，其他 → 前端静态文件
// webFS 的根下应直接包含 index.html + assets/*（已经 fs.Sub 过的 dist）；
// 传 nil 时只提供 API（开发模式由 Vite dev server 提供前端并 proxy /api）
func NewRouter(srv *Server, webFS fs.FS) http.Handler {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("POST /api/validate", srv.HandleValidate)
	mux.HandleFunc("POST /api/plan", srv.HandlePlan)
	mux.HandleFunc("POST /api/gen", srv.HandleGen)
	mux.HandleFunc("POST /api/doctor", srv.HandleDoctor)
	mux.HandleFunc("GET /api/schema", srv.HandleSchema)

	// 前端静态文件（生产模式：embed；开发模式：Vite dev server proxy）
	if webFS != nil {
		mux.Handle("/", spaHandler(webFS))
	}

	return corsMiddleware(mux)
}

// spaHandler 处理 SPA 路由：命中静态文件直接返回，未命中的非 /api 路径回退到 index.html
// 让 Vue Router 在前端接管路径。/api/* 不会到达这里（ServeMux 前半段已经匹配）。
func spaHandler(distFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(distFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			serveIndex(w, distFS)
			return
		}
		// 尝试打开请求的文件；若不存在（SPA 客户端路由），回退 index.html
		if f, err := distFS.Open(p); err != nil {
			serveIndex(w, distFS)
			return
		} else {
			_ = f.Close()
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, distFS fs.FS) {
	f, err := distFS.Open("index.html")
	if err != nil {
		http.Error(w, "index.html not found in embedded dist", http.StatusNotImplemented)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.Copy(w, f)
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
