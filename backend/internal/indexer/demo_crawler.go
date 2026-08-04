package indexer

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"collyrobot/backend/internal/domain"
)

// DemoTopicListCrawler 是不依赖网络的索引模拟器。
// 每次执行会分批产出示例主题，刻意保留少量间隔，以便在管理 UI 中观察到
// “索引中 → 主题入队 → Worker 消费”的完整流水线。
type DemoTopicListCrawler struct {
	runSequence atomic.Uint64
	// sessionID 避免后端重启后 runSequence 从 1 开始，和旧演示数据发生 external_id 冲突。
	sessionID  int64
	Pages      int
	TopicsPage int
	PageDelay  time.Duration
}

// NewDemoTopicListCrawler 创建用于本地调试的默认模拟索引器。
func NewDemoTopicListCrawler() *DemoTopicListCrawler {
	return &DemoTopicListCrawler{
		sessionID:  time.Now().UnixNano(),
		Pages:      3,
		TopicsPage: 3,
		PageDelay:  450 * time.Millisecond,
	}
}

// CrawlTopicIndex 实现 TopicListCrawler，并模拟逐页发现论坛主题。
func (c *DemoTopicListCrawler) CrawlTopicIndex(ctx context.Context, emit TopicBatchEmitter) error {
	pages := c.Pages
	if pages < 1 {
		pages = 1
	}
	topicsPerPage := c.TopicsPage
	if topicsPerPage < 1 {
		topicsPerPage = 1
	}
	runID := fmt.Sprintf("%d-%d", c.sessionID, c.runSequence.Add(1))

	for page := 1; page <= pages; page++ {
		if err := waitDemo(ctx, c.PageDelay); err != nil {
			return err
		}
		batch := make([]domain.Topic, 0, topicsPerPage)
		for position := 1; position <= topicsPerPage; position++ {
			ordinal := (page-1)*topicsPerPage + position
			batch = append(batch, domain.Topic{
				ExternalID: fmt.Sprintf("demo-%s-%03d", runID, ordinal),
				Title:      fmt.Sprintf("演示小说：第 %d 个主题", ordinal),
				AuthorID:   "demo-author",
				URL:        fmt.Sprintf("https://demo.invalid/topic/%s/%d", runID, ordinal),
			})
		}
		if err := emit(batch); err != nil {
			return err
		}
	}
	return nil
}

func waitDemo(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var _ TopicListCrawler = (*DemoTopicListCrawler)(nil)
