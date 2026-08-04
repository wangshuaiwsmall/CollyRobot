package config

import (
	"os"
	"strconv"
)

// Config 保存服务启动所需的全局配置。
// WorkerLimit 控制同时处理多少个主题，SyncLimit 控制单个主题内部的最大并发请求数。
type Config struct {
	Port         string
	DatabasePath string
	LogDirectory string
	WorkerLimit  int
	SyncLimit    int
	ProxyMode    string
	ProxyURL     string
	// DemoMode 启用本地模拟索引与正文抓取，不会向任何论坛发起网络请求。
	// 它用于在论坛规则尚未配置完成前调试管理 UI、日志和调度流程。
	DemoMode bool
}

// Load 从环境变量构造配置。使用默认值可让开发者无需准备配置文件即可启动服务。
func Load() Config {
	return Config{
		Port:         envOrDefault("PORT", "8080"),
		DatabasePath: envOrDefault("DATABASE_PATH", "./data/collyrobot.db"),
		LogDirectory: envOrDefault("LOG_DIRECTORY", "./logs"),
		WorkerLimit:  envIntOrDefault("WORKER_LIMIT", 2),
		SyncLimit:    envIntOrDefault("SYNC_CONCURRENCY", 4),
		ProxyMode:    envOrDefault("PROXY_MODE", "direct"),
		ProxyURL:     os.Getenv("PROXY_URL"),
		DemoMode:     envBoolOrDefault("DEMO_MODE", false),
	}
}

// envBoolOrDefault 读取布尔环境变量；缺失或格式非法时使用默认值。
func envBoolOrDefault(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// envIntOrDefault 读取整数环境变量；变量缺失或格式非法时回退到默认值。
func envIntOrDefault(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return value
}

// envOrDefault 读取字符串环境变量，空字符串被视为未配置。
func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
