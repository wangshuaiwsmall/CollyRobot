package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"collyrobot/backend/internal/app"
	"collyrobot/backend/internal/config"
)

func main() {
	if err := run(); err != nil {
		log.Printf("backend stopped with error: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, startScheduler, buildIndex := commandConfig()
	application, err := app.New(cfg, os.Stdout)
	if err != nil {
		return err
	}
	defer application.Close()

	application.Logs.Backend.Printf("level=INFO event=service_start port=%s demo_mode=%t", cfg.Port, cfg.DemoMode)
	if buildIndex {
		if err := application.Scheduler.TriggerIndex(); err != nil {
			return err
		}
	}

	address := ":" + cfg.Port
	server := &http.Server{
		Addr:              address,
		Handler:           application.Handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	application.Logs.Backend.Printf("level=INFO event=http_listen address=http://localhost:%s scheduler_started=true start_flag=%t", cfg.Port, startScheduler)
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()

	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	select {
	case err := <-serverErrors:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	case <-signalContext.Done():
		application.Logs.Backend.Printf("level=INFO event=shutdown_started signal=%q", signalContext.Err())
	}

	// 先停止 HTTP 接入并给在途请求收尾时间，再关闭 Worker、数据库和日志。
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		_ = server.Close()
		return err
	}
	application.Logs.Backend.Printf("level=INFO event=http_server_shutdown_complete")
	return application.Close()
}

// commandConfig 以环境变量配置为默认值，命令行参数可在无前端调试时覆盖这些值。
func commandConfig() (config.Config, bool, bool) {
	cfg := config.Load()
	startScheduler := flag.Bool("start", false, "兼容参数：调度器现已默认启动并待命")
	buildIndex := flag.Bool("index", false, "异步触发一次索引构建")
	flag.StringVar(&cfg.Port, "port", cfg.Port, "HTTP 端口")
	flag.StringVar(&cfg.DatabasePath, "database", cfg.DatabasePath, "SQLite 数据库文件路径")
	flag.StringVar(&cfg.LogDirectory, "log-dir", cfg.LogDirectory, "日志目录")
	flag.IntVar(&cfg.WorkerLimit, "workers", cfg.WorkerLimit, "全局 Worker 上限")
	flag.IntVar(&cfg.SyncLimit, "sync-concurrency", cfg.SyncLimit, "单主题内部并发上限")
	flag.BoolVar(&cfg.DemoMode, "demo", cfg.DemoMode, "启用本地模拟索引和内容抓取，不访问真实论坛")
	flag.Parse()
	return cfg, *startScheduler, *buildIndex
}
