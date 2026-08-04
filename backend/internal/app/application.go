// Package app 负责组装应用依赖，但不决定调度器是否启动。
// 生命周期控制由 Web API 或命令行入口显式发起，便于管理界面和无前端调试共用。
package app

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"sync"

	"collyrobot/backend/internal/config"
	"collyrobot/backend/internal/crawler"
	"collyrobot/backend/internal/httpserver"
	"collyrobot/backend/internal/indexer"
	applogger "collyrobot/backend/internal/logger"
	"collyrobot/backend/internal/proxyconfig"
	"collyrobot/backend/internal/scheduler"
	"collyrobot/backend/internal/store"
	"collyrobot/backend/internal/worker"
)

// Application 持有服务运行所需的长生命周期依赖。
// New 只完成初始化，不会启动调度器或开始任何抓取任务。
type Application struct {
	Handler   http.Handler
	Scheduler *scheduler.Scheduler
	Logs      *applogger.Module
	db        *sql.DB
	closeOnce sync.Once
	closeErr  error
}

// New 创建数据库、日志、业务模块和 HTTP 路由。
func New(cfg config.Config, console io.Writer) (*Application, error) {
	logs, err := applogger.New(cfg.LogDirectory, console)
	if err != nil {
		return nil, fmt.Errorf("initialize logger: %w", err)
	}
	db, err := store.OpenSQLite(cfg.DatabasePath)
	if err != nil {
		_ = logs.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}

	topics := store.NewTopicStore(db)
	proxyManager, err := proxyconfig.NewPersistent(db, proxyconfig.Config{Mode: cfg.ProxyMode, URL: cfg.ProxyURL})
	if err != nil {
		_ = db.Close()
		_ = logs.Close()
		return nil, fmt.Errorf("initialize proxy configuration: %w", err)
	}
	crawlerService := crawler.NewService(proxyManager)
	var indexCrawler indexer.TopicListCrawler
	var forumFetcher worker.TopicFetcher
	if cfg.DemoMode {
		// 演示模式使用无网络实现，让 UI 可在论坛规则尚未接入时调试完整工作流。
		indexCrawler = indexer.NewDemoTopicListCrawler()
		forumFetcher = worker.NewDemoTopicFetcher()
		logs.Backend.Printf("level=INFO event=demo_mode_enabled")
	} else {
		// 索引器使用同步 Colly 递归翻页；具体论坛列表规则暂由 Stub 占位。
		indexCrawler = indexer.NewCollyForumIndexCrawler(crawlerService, indexer.ForumIndexRulesStub{})
		// 抓取编排器已接入 Colly；具体 BBS 的 URL/页面规则暂由 Stub 占位。
		forumFetcher = worker.NewCollyForumFetcher(crawlerService, worker.ForumPageRulesStub{}, topics)
	}
	indexBuilder := indexer.New(indexCrawler, topics)
	dispatcher := scheduler.New(topics, forumFetcher, indexBuilder, logs.Backend, scheduler.Limits{
		Workers: cfg.WorkerLimit, SyncConcurrency: cfg.SyncLimit,
	})
	dispatcher.SetWorkflowLoggers(logs.Indexer, logs.Crawler)
	// 服务启动时让调度器默认待命。此操作只创建空闲 Worker，不会加载 waiting Topic，
	// 因而不会因服务重启产生意外抓取；实际入队仍由管理 API 显式控制。
	if err := dispatcher.Start(context.Background()); err != nil {
		_ = db.Close()
		_ = logs.Close()
		return nil, fmt.Errorf("start scheduler: %w", err)
	}

	return &Application{
		Handler:   httpserver.New(cfg, db, dispatcher, logs, proxyManager),
		Scheduler: dispatcher,
		Logs:      logs,
		db:        db,
	}, nil
}

// Close 按依赖反向顺序释放资源。先停止调度器，确保不会在数据库和日志关闭后继续写入。
func (a *Application) Close() error {
	a.closeOnce.Do(func() {
		a.Scheduler.Stop()
		dbErr := a.db.Close()
		logErr := a.Logs.Close()
		if dbErr != nil {
			a.closeErr = dbErr
			return
		}
		a.closeErr = logErr
	})
	return a.closeErr
}
