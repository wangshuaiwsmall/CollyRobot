package repository

import (
	"context"

	"collyrobot/backend/internal/domain"
)

// TopicPageRepository 保存抓取过程中的分页临时内容。
// 它与 TopicRepository 分离，使主题调度状态和大文本缓存可以独立演进。
type TopicPageRepository interface {
	// LoadPages 返回已经持久化的页面。Fetcher 使用它跳过断点前已完成的请求。
	LoadPages(context.Context, int64) (map[int][]domain.PageContent, error)
	// SavePage 幂等保存单页解析结果；页面抓取成功后立即调用，不等待整个主题完成。
	SavePage(context.Context, int64, int, []domain.PageContent) error
}
