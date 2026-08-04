package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Module 持有前后端两个完全独立的日志流。
// Backend 用于 Gin、调度器及后端业务；Frontend 只接收浏览器上报日志。
type Module struct {
	Backend        *SourceLogger
	Frontend       *log.Logger
	Indexer        *SourceLogger
	Crawler        *SourceLogger
	backendWriter  *DailyWriter
	frontendWriter *DailyWriter
	indexerWriter  *DailyWriter
	crawlerWriter  *DailyWriter
	dir            string
}

// New 创建日志模块。日志同时写入文件；后端日志额外输出到 console，方便本地开发观察。
func New(dir string, console io.Writer) (*Module, error) {
	backendWriter, err := NewDailyWriter(dir, "backend")
	if err != nil {
		return nil, fmt.Errorf("初始化后端日志: %w", err)
	}
	frontendWriter, err := NewDailyWriter(dir, "frontend")
	if err != nil {
		_ = backendWriter.Close()
		return nil, fmt.Errorf("初始化前端日志: %w", err)
	}
	indexerWriter, err := NewDailyWriter(dir, "indexer")
	if err != nil {
		_ = backendWriter.Close()
		_ = frontendWriter.Close()
		return nil, fmt.Errorf("初始化索引日志: %w", err)
	}
	crawlerWriter, err := NewDailyWriter(dir, "crawler")
	if err != nil {
		_ = backendWriter.Close()
		_ = frontendWriter.Close()
		_ = indexerWriter.Close()
		return nil, fmt.Errorf("初始化抓取日志: %w", err)
	}

	backendOutput := io.Writer(backendWriter)
	if console != nil {
		backendOutput = io.MultiWriter(console, backendWriter)
	}
	// 使用服务器本地日期，与日志文件名采用的本地日期保持一致。
	flags := log.Ldate | log.Ltime | log.Lmicroseconds
	return &Module{
		Backend:        newSourceLogger(backendOutput, flags),
		Frontend:       log.New(frontendWriter, "", flags),
		Indexer:        newSourceLogger(indexerWriter, flags),
		Crawler:        newSourceLogger(crawlerWriter, flags),
		backendWriter:  backendWriter,
		frontendWriter: frontendWriter,
		indexerWriter:  indexerWriter,
		crawlerWriter:  crawlerWriter,
		dir:            dir,
	}, nil
}

// Close 关闭两个日志文件，并返回遇到的第一个错误。
func (m *Module) Close() error {
	backendErr := m.backendWriter.Close()
	frontendErr := m.frontendWriter.Close()
	indexerErr := m.indexerWriter.Close()
	crawlerErr := m.crawlerWriter.Close()
	if backendErr != nil {
		return backendErr
	}
	if frontendErr != nil {
		return frontendErr
	}
	if indexerErr != nil {
		return indexerErr
	}
	return crawlerErr
}

// Tail 返回指定日志流当天文件的最后若干行，供管理 UI 轮询展示。
// stream 只接受已定义前缀，避免 API 通过文件名访问任意路径。
func (m *Module) Tail(stream string, lines int) ([]string, error) {
	prefixes := map[string]string{
		"backend":  "backend",
		"frontend": "frontend",
		"indexer":  "indexer",
		"crawler":  "crawler",
	}
	prefix, exists := prefixes[stream]
	if !exists {
		return nil, fmt.Errorf("unknown log stream: %s", stream)
	}
	if lines < 1 {
		lines = 100
	}
	if lines > 500 {
		lines = 500
	}
	path := filepath.Join(m.dir, fmt.Sprintf("%s-%s.log", prefix, time.Now().Format("2006-01-02")))
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	// 管理页只显示尾部，避免日志文件长期运行后造成一次响应过大。
	const maxReadBytes = 512 * 1024
	if len(content) > maxReadBytes {
		content = content[len(content)-maxReadBytes:]
	}
	entries := strings.Split(strings.TrimRight(string(content), "\r\n"), "\n")
	if len(entries) == 1 && entries[0] == "" {
		return []string{}, nil
	}
	if len(entries) > lines {
		entries = entries[len(entries)-lines:]
	}
	return entries, nil
}
