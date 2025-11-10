package internal

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Logger 简单日志接口
type Logger interface {
	Debug(format string, args ...interface{})
	Info(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
	Progress(current, total int, stage string)
}

// SimpleLogger 简单日志实现
type SimpleLogger struct {
	mu      sync.Mutex
	enabled bool
}

// NewSimpleLogger 创建简单日志器
func NewSimpleLogger(enabled bool) *SimpleLogger {
	return &SimpleLogger{enabled: enabled}
}

func (l *SimpleLogger) log(level, format string, args ...interface{}) {
	if !l.enabled {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("15:04:05")
	fmt.Fprintf(os.Stderr, "[%s] [%s] %s\n", timestamp, level, msg)
}

func (l *SimpleLogger) Debug(format string, args ...interface{}) {
	l.log("DEBUG", format, args...)
}

func (l *SimpleLogger) Info(format string, args ...interface{}) {
	l.log("INFO", format, args...)
}

func (l *SimpleLogger) Warn(format string, args ...interface{}) {
	l.log("WARN", format, args...)
}

func (l *SimpleLogger) Error(format string, args ...interface{}) {
	l.log("ERROR", format, args...)
}

func (l *SimpleLogger) Progress(current, total int, stage string) {
	if !l.enabled {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	percent := 0.0
	if total > 0 {
		percent = float64(current) / float64(total) * 100
	}
	fmt.Fprintf(os.Stderr, "\r[进度] %s: %d/%d (%.1f%%)", stage, current, total, percent)
	if current >= total {
		fmt.Fprintf(os.Stderr, "\n")
	}
}

// 全局日志实例
var defaultLogger Logger = NewSimpleLogger(true)

// SetLogger 设置全局日志器
func SetLogger(logger Logger) {
	defaultLogger = logger
}

// GetLogger 获取全局日志器
func GetLogger() Logger {
	return defaultLogger
}

