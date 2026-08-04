package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SourceLogger 在每条后端日志末尾追加调用方文件名和代码行号。
// 固定输出格式为：时间 + 日志正文 + source=文件名:行号。
type SourceLogger struct {
	base *log.Logger
}

func newSourceLogger(output io.Writer, flags int) *SourceLogger {
	return &SourceLogger{base: log.New(output, "", flags)}
}

// Printf 输出格式化日志，source 指向直接调用 Printf 的业务代码。
func (l *SourceLogger) Printf(format string, args ...any) {
	_, file, line, ok := runtime.Caller(1)
	l.write(fmt.Sprintf(format, args...), file, line, ok)
}

// PrintfDepth 供业务日志辅助函数使用。depth=1 会越过一层辅助函数，
// 将 source 定位到调用该辅助函数的顶层业务代码。
func (l *SourceLogger) PrintfDepth(depth int, format string, args ...any) {
	if depth < 0 {
		depth = 0
	}
	_, file, line, ok := runtime.Caller(1 + depth)
	l.write(fmt.Sprintf(format, args...), file, line, ok)
}

// Fatalf 写入最后一条带代码位置的日志后，以状态码 1 结束进程。
func (l *SourceLogger) Fatalf(format string, args ...any) {
	_, file, line, ok := runtime.Caller(1)
	l.write(fmt.Sprintf(format, args...), file, line, ok)
	os.Exit(1)
}

// Writer 为 Gin 等只接受 io.Writer 的组件提供适配。
func (l *SourceLogger) Writer() io.Writer { return sourceWriter{logger: l} }

func (l *SourceLogger) write(message, file string, line int, ok bool) {
	// Gin 写入的内容通常自带换行，先移除行尾换行，确保 source 位于整条日志结尾。
	message = strings.TrimRight(message, "\r\n")
	if !ok {
		l.base.Printf("%s source=unknown:0", message)
		return
	}
	l.base.Printf("%s source=%s:%d", message, filepath.Base(file), line)
}

type sourceWriter struct{ logger *SourceLogger }

func (w sourceWriter) Write(p []byte) (int, error) {
	// 跳过本日志包和标准日志包，定位 Gin 或其他实际写入方。
	file, line, ok := externalCaller()
	w.logger.write(string(p), file, line, ok)
	return len(p), nil
}

func externalCaller() (string, int, bool) {
	pcs := make([]uintptr, 16)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		file := filepath.ToSlash(frame.File)
		if !strings.Contains(file, "/internal/logger/") && !strings.HasSuffix(file, "/log/log.go") {
			return frame.File, frame.Line, true
		}
		if !more {
			break
		}
	}
	return "", 0, false
}
