package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"collyrobot/backend/internal/config"
	"collyrobot/backend/internal/domain"
	applogger "collyrobot/backend/internal/logger"
	"collyrobot/backend/internal/proxyconfig"
	"collyrobot/backend/internal/repository"
	"collyrobot/backend/internal/scheduler"
	"github.com/gin-gonic/gin"
)

// Server 保存 HTTP Handler 所需的应用依赖，不在请求处理中创建数据库或调度器。
type Server struct {
	config         config.Config
	store          repository.DataStore
	scheduler      *scheduler.Scheduler
	logs           *applogger.Module
	frontendLogger *log.Logger
	proxy          *proxyconfig.Manager
}

// New 创建 Gin 路由并注册所有管理接口。
func New(cfg config.Config, store repository.DataStore, dispatcher *scheduler.Scheduler, logs *applogger.Module, proxyManagers ...*proxyconfig.Manager) *gin.Engine {
	proxyManager := firstProxyManager(proxyManagers)
	s := &Server{config: cfg, store: store, scheduler: dispatcher, logs: logs, proxy: proxyManager}
	var accessOutput io.Writer = gin.DefaultWriter
	var recoveryOutput io.Writer = gin.DefaultErrorWriter
	if logs != nil {
		s.frontendLogger = logs.Frontend
		accessOutput = logs.Backend.Writer()
		recoveryOutput = logs.Backend.Writer()
	}

	// 使用 gin.New 显式安装中间件，避免默认配置难以追踪。
	router := gin.New()
	// Gin 访问日志和 panic 恢复日志归入后端日志流，不与浏览器日志混写。
	router.Use(gin.LoggerWithWriter(accessOutput), gin.RecoveryWithWriter(recoveryOutput))

	api := router.Group("/api")
	api.GET("/hello", s.hello)
	api.GET("/health", s.health)
	api.GET("/scheduler", s.schedulerStatus)
	api.POST("/scheduler/start", s.startScheduler)
	api.POST("/scheduler/stop", s.stopScheduler)
	api.POST("/scheduler/index", s.buildIndex)
	api.POST("/scheduler/index/cancel", s.cancelIndex)
	api.POST("/scheduler/queue/waiting", s.queueWaiting)
	api.POST("/scheduler/retry/failed", s.retryFailed)
	api.POST("/scheduler/topics/:id/fetch", s.fetchTopic)
	api.GET("/topics", s.listTopics)
	api.GET("/topics/:id/contents", s.topicContents)
	api.GET("/topics/:id/contents/full", s.fullTopicContent)
	api.PUT("/scheduler/limits", s.updateSchedulerLimits)
	api.GET("/settings/proxy", s.proxyStatus)
	api.PUT("/settings/proxy", s.updateProxy)
	api.POST("/logs/frontend", s.receiveFrontendLog)
	api.GET("/logs/:stream/tail", s.tailLog)

	return router
}

// tailLog 返回指定工作流当日日志的末尾内容，供管理页通过短轮询实时查看状态。
// 只对外提供白名单中的日志流，避免客户端将接口作为文件读取入口。
func (s *Server) tailLog(c *gin.Context) {
	if s.logs == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "log module is not configured"})
		return
	}
	stream := c.Param("stream")
	if stream != "indexer" && stream != "crawler" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "stream must be indexer or crawler"})
		return
	}
	lines, err := strconv.Atoi(c.DefaultQuery("lines", "120"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "lines must be an integer"})
		return
	}
	entries, err := s.logs.Tail(stream, lines)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read log tail: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stream": stream, "lines": entries})
}

func firstProxyManager(managers []*proxyconfig.Manager) *proxyconfig.Manager {
	if len(managers) > 0 && managers[0] != nil {
		return managers[0]
	}
	manager, _ := proxyconfig.New(proxyconfig.Config{Mode: proxyconfig.ModeDirect})
	return manager
}

func (s *Server) proxyStatus(c *gin.Context) {
	c.JSON(http.StatusOK, s.proxy.Status())
}

func (s *Server) updateProxy(c *gin.Context) {
	var next proxyconfig.Config
	if err := c.ShouldBindJSON(&next); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.proxy.UpdatePersistent(c.Request.Context(), next); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, s.proxy.Status())
}

type frontendLogRequest struct {
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Timestamp string         `json:"timestamp"`
	Context   map[string]any `json:"context"`
}

// receiveFrontendLog 接收浏览器日志并写入独立的 frontend-日期.log。
// 服务端重新生成日志行时间，浏览器时间仅作为字段保存，避免客户端时钟影响日志轮转。
func (s *Server) receiveFrontendLog(c *gin.Context) {
	if s.frontendLogger == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "frontend logger is not configured"})
		return
	}
	// 限制单次上报体积，避免日志接口被超大 JSON 占用过多内存或磁盘。
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	var entry frontendLogRequest
	if err := c.ShouldBindJSON(&entry); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	entry.Level = strings.ToUpper(strings.TrimSpace(entry.Level))
	if entry.Level != "DEBUG" && entry.Level != "INFO" && entry.Level != "WARN" && entry.Level != "ERROR" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported log level"})
		return
	}
	entry.Message = strings.TrimSpace(entry.Message)
	if entry.Message == "" || len(entry.Message) > 4096 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message must contain 1 to 4096 bytes"})
		return
	}
	contextJSON, _ := json.Marshal(entry.Context)
	s.frontendLogger.Printf("level=%s client_ip=%s client_time=%q message=%q context=%s",
		entry.Level, c.ClientIP(), entry.Timestamp, entry.Message, contextJSON)
	c.Status(http.StatusNoContent)
}

// startScheduler 保留为停止后的恢复入口；正常服务初始化时调度器已处于待命状态。
func (s *Server) startScheduler(c *gin.Context) {
	// HTTP 请求结束会取消 Request.Context，因此不能将它作为 Worker 的根生命周期。
	if err := s.scheduler.Start(context.Background()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, s.scheduler.Status())
}

// queueWaiting 按用户指令将全部 waiting Topic 放入内存队列，不会重复派发正在处理的主题。
func (s *Server) queueWaiting(c *gin.Context) {
	mode, err := domain.ParseFetchMode(c.Query("mode"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	queued, err := s.scheduler.QueueWaiting(c.Request.Context(), mode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"queued": queued, "mode": mode, "status": s.scheduler.Status()})
}

// retryFailed 显式重试失败主题：先恢复为 waiting，再加入内存队列。
func (s *Server) retryFailed(c *gin.Context) {
	mode, err := domain.ParseFetchMode(c.Query("mode"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	queued, err := s.scheduler.RetryFailed(c.Request.Context(), mode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"queued": queued, "mode": mode, "status": s.scheduler.Status()})
}

// fetchTopic 允许对任意状态的单个 Topic 执行增量、校验或重新拉取。
func (s *Server) fetchTopic(c *gin.Context) {
	topicID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || topicID < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "topic id must be a positive integer"})
		return
	}
	mode, err := domain.ParseFetchMode(c.Query("mode"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	queued, err := s.scheduler.QueueTopic(c.Request.Context(), topicID, mode)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if queued == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "topic is already queued or running"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"queued": queued, "topic_id": topicID, "mode": mode, "status": s.scheduler.Status()})
}

// stopScheduler 停止 Worker 并取消通过 API 触发的索引任务。
func (s *Server) stopScheduler(c *gin.Context) {
	s.scheduler.Stop()
	c.JSON(http.StatusOK, s.scheduler.Status())
}

// buildIndex 异步触发一次索引构建，避免论坛分页抓取长期占用 HTTP 请求。
func (s *Server) buildIndex(c *gin.Context) {
	err := s.scheduler.TriggerIndex()
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "index task accepted"})
}

// cancelIndex 请求中断当前索引；只影响索引 Collector，保留已经入库的 Topic。
func (s *Server) cancelIndex(c *gin.Context) {
	if err := s.scheduler.CancelIndex(); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, s.scheduler.Status())
}

// listTopics 只返回指定状态的当前页，并附带各状态总数，避免大量 Topic 一次传给浏览器。
func (s *Server) listTopics(c *gin.Context) {
	status := domain.TopicStatus(strings.ToLower(strings.TrimSpace(c.DefaultQuery("status", string(domain.TopicWaiting)))))
	if status != domain.TopicWaiting && status != domain.TopicDone && status != domain.TopicFailed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be waiting, done or failed"})
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "page must be a positive integer"})
		return
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	if err != nil || pageSize < 1 || pageSize > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "page_size must be between 1 and 100"})
		return
	}
	result, err := s.store.ListTopicPage(c.Request.Context(), status, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

const topicContentPreviewLimit = 20

// topicContents 只返回正文预览，避免打开详情时传输整个 Topic。
func (s *Server) topicContents(c *gin.Context) {
	topicID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || topicID < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "topic id must be a positive integer"})
		return
	}
	preview, err := s.store.PreviewContents(c.Request.Context(), topicID, topicContentPreviewLimit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"topic_id": topicID, "contents": preview.Contents, "total": preview.Total,
		"displayed": preview.Displayed, "truncated": preview.Truncated})
}

// fullTopicContent 按页码、楼层和 UID 排序后拼接全部 Text，供阅读完整内容。
func (s *Server) fullTopicContent(c *gin.Context) {
	topicID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || topicID < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "topic id must be a positive integer"})
		return
	}
	content, err := s.store.FullContent(c.Request.Context(), topicID)
	if err != nil {
		if errors.Is(err, repository.ErrTopicNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "topic not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"topic_id": topicID, "title": content.Title, "content_count": content.ContentCount, "text": content.Text})
}

// schedulerStatus 返回并发上限、Worker 数量及累计处理统计。
func (s *Server) schedulerStatus(c *gin.Context) {
	c.JSON(http.StatusOK, s.scheduler.Status())
}

// updateSchedulerLimits 接收运行时并发配置；调度器会负责校正非法边界值。
func (s *Server) updateSchedulerLimits(c *gin.Context) {
	var limits scheduler.Limits
	if err := c.ShouldBindJSON(&limits); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, s.scheduler.SetLimits(limits))
}

// hello 是前后端联通演示接口。
func (s *Server) hello(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"message":   "Hello from CollyRobot!",
		"framework": "Gin + Colly + SQLite",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// health 通过实际 Ping 数据库判断服务是否具备基本工作条件。
func (s *Server) health(c *gin.Context) {
	if err := s.store.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
