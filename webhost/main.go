// Command webhost 托管 Vue 构建产物，并将 API 请求反向代理到 CollyRobot 后端。
// 它只依赖 Go 标准库，运行目标无需安装 Node.js 或 pnpm。
package main

import (
	"bytes"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

// dist 由 scripts/build-webhost.cmd 在构建前从 frontend/dist 复制而来。
// all: 前缀可包含 Vite 生成的点文件或其他静态资源。
//
//go:embed all:dist
var embeddedDist embed.FS

func main() {
	addr := flag.String("addr", envOrDefault("WEB_HOST_ADDR", ":8081"), "静态站点监听地址")
	backendURL := flag.String("backend", envOrDefault("BACKEND_URL", "http://127.0.0.1:8080"), "后端 API 地址")
	flag.Parse()

	staticFiles, err := fs.Sub(embeddedDist, "dist")
	if err != nil {
		log.Fatalf("load embedded frontend assets: %v", err)
	}
	target, err := url.Parse(*backendURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		log.Fatalf("invalid backend URL %q", *backendURL)
	}

	apiProxy := httputil.NewSingleHostReverseProxy(target)
	apiProxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, proxyErr error) {
		log.Printf("api proxy failed method=%s path=%s error=%v", request.Method, request.URL.Path, proxyErr)
		http.Error(writer, "backend API is unavailable", http.StatusBadGateway)
	}

	server := &http.Server{
		Addr:              *addr,
		Handler:           newHandler(staticFiles, apiProxy),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("web host listening on http://localhost%s; API proxy=%s", *addr, target)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func newHandler(staticFiles fs.FS, apiProxy http.Handler) http.Handler {
	fileServer := http.FileServer(http.FS(staticFiles))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api" || strings.HasPrefix(request.URL.Path, "/api/") {
			apiProxy.ServeHTTP(writer, request)
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+request.URL.Path), "/")
		if name != "" {
			if info, err := fs.Stat(staticFiles, name); err == nil && !info.IsDir() {
				setCacheHeader(writer, name)
				fileServer.ServeHTTP(writer, request)
				return
			}
			// 带扩展名的路径一般是静态资源；缺失时返回 404 而不是 Vue 入口文件。
			if strings.Contains(path.Base(name), ".") {
				http.NotFound(writer, request)
				return
			}
		}

		// Vue History 路由回退：业务路径始终交给 index.html 再由前端路由解析。
		setCacheHeader(writer, "index.html")
		index, err := fs.ReadFile(staticFiles, "index.html")
		if err != nil {
			http.Error(writer, "frontend entry is unavailable", http.StatusInternalServerError)
			return
		}
		http.ServeContent(writer, request, "index.html", time.Time{}, bytes.NewReader(index))
	})
}

func setCacheHeader(writer http.ResponseWriter, name string) {
	if name == "index.html" {
		writer.Header().Set("Cache-Control", "no-cache")
		return
	}
	if strings.HasPrefix(name, "assets/") {
		// Vite 资源名包含内容哈希，可长期缓存；新版 index.html 会指向新文件名。
		writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
