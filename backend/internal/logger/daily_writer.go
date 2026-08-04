package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DailyWriter 是一个线程安全的按日期轮转写入器。
// 文件名格式为“前缀-YYYY-MM-DD.log”，日期变化后的第一条日志会触发文件切换。
type DailyWriter struct {
	mu     sync.Mutex
	dir    string
	prefix string
	date   string
	file   *os.File
	now    func() time.Time
}

// NewDailyWriter 创建写入器并立即打开当天的日志文件，以便启动阶段尽早发现目录权限问题。
func NewDailyWriter(dir, prefix string) (*DailyWriter, error) {
	writer := &DailyWriter{dir: dir, prefix: prefix, now: time.Now}
	if err := writer.rotateLocked(writer.currentDate()); err != nil {
		return nil, err
	}
	return writer, nil
}

// Write 实现 io.Writer。单把互斥锁同时保护日期检查、文件切换和实际写入，
// 防止多个 HTTP/Worker 协程在午夜并发写入时重复关闭或打开文件。
func (w *DailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	date := w.currentDate()
	if w.file == nil || date != w.date {
		if err := w.rotateLocked(date); err != nil {
			return 0, err
		}
	}
	return w.file.Write(p)
}

// Close 释放当前文件句柄，可安全重复调用。
func (w *DailyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *DailyWriter) currentDate() string {
	return w.now().Format("2006-01-02")
}

// rotateLocked 切换到指定日期的文件。调用者必须持有 w.mu；初始化阶段尚未并发，也可直接调用。
func (w *DailyWriter) rotateLocked(date string) error {
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return fmt.Errorf("创建日志目录: %w", err)
	}
	path := filepath.Join(w.dir, fmt.Sprintf("%s-%s.log", w.prefix, date))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开日志文件 %s: %w", path, err)
	}
	oldFile := w.file
	w.file = file
	w.date = date
	if oldFile != nil {
		_ = oldFile.Close()
	}
	return nil
}

var _ io.WriteCloser = (*DailyWriter)(nil)
