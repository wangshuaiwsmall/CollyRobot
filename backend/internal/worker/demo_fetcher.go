package worker

import (
	"context"
	"time"

	"collyrobot/backend/internal/domain"
)

// DemoTopicFetcher 是不访问论坛的正文抓取模拟器。
// 它按主题内并发上限缩短等待时间，用于在 UI 中观察 Worker 并发、队列消耗和完成计数。
type DemoTopicFetcher struct {
	TopicDuration time.Duration
}

// NewDemoTopicFetcher 创建默认演示抓取器。单个主题会保持短暂运行时间，便于观察状态变化。
func NewDemoTopicFetcher() *DemoTopicFetcher {
	return &DemoTopicFetcher{TopicDuration: 900 * time.Millisecond}
}

// Fetch 模拟正文分页抓取；不写入正式小说内容，避免演示数据污染真实内容存储。
func (f *DemoTopicFetcher) Fetch(ctx context.Context, _ domain.Topic, syncConcurrency int) error {
	duration := f.TopicDuration
	if duration <= 0 {
		duration = 900 * time.Millisecond
	}
	if syncConcurrency > 1 {
		duration /= time.Duration(syncConcurrency)
	}
	if duration < 120*time.Millisecond {
		duration = 120 * time.Millisecond
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var _ TopicFetcher = (*DemoTopicFetcher)(nil)
