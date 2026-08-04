package indexer

import (
	"context"
	"fmt"

	"collyrobot/backend/internal/domain"
	"collyrobot/backend/internal/repository"
)

// TopicBatchEmitter 在论坛列表的一批主题（通常是一页）解析完成后立即接收结果。
// 只有 emitter 返回 nil，抓取器才继续访问下一页，从而形成带背压的流式索引流程。
type TopicBatchEmitter func([]domain.Topic) error

// TopicListCrawler 是“论坛主题列表抓取”的流式适配接口。
// 不同 BBS 的 DOM、分页和鉴权规则由各自实现处理，索引服务只接收标准化后的 Topic 批次。
type TopicListCrawler interface {
	// CrawlTopicIndex 遍历目标版块，并在每页解析完成时调用 emit，而不是等待全部页面结束。
	CrawlTopicIndex(context.Context, TopicBatchEmitter) error
}

// Service 编排一次完整的索引工作流：抓取主题列表，然后批量写入主题仓库。
type Service struct {
	crawler TopicListCrawler
	topics  repository.TopicRepository
}

// New 注入论坛抓取适配器与主题仓库，方便在测试中替换为内存实现。
func New(crawler TopicListCrawler, topics repository.TopicRepository) *Service {
	return &Service{crawler: crawler, topics: topics}
}

// Build 流式执行索引构建，并返回本次新增主题总数。
// 每批主题成功入库后立刻把“本次新增主题”传给 onPersisted；调度器利用该回调唤醒 Worker，
// 因此内容抓取可以与后续论坛列表页的索引并行进行。
func (s *Service) Build(ctx context.Context, onPersisted func([]domain.Topic)) (int, error) {
	total := 0
	err := s.crawler.CrawlTopicIndex(ctx, func(topics []domain.Topic) error {
		if len(topics) == 0 {
			return nil
		}
		// 每一批独立提交。提交成功前不通知 Worker，避免任务尚未可见就发生空唤醒。
		newTopics, err := s.topics.UpsertDiscovered(ctx, topics)
		if err != nil {
			return fmt.Errorf("save topic index batch: %w", err)
		}
		total += len(newTopics)
		if onPersisted != nil && len(newTopics) > 0 {
			onPersisted(newTopics)
		}
		return nil
	})
	if err != nil {
		// 即使后续页面失败，前面已经提交的批次仍然可被 Worker 正常处理。
		return total, fmt.Errorf("crawl topic index: %w", err)
	}
	return total, nil
}
