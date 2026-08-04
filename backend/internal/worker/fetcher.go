package worker

import (
	"context"
	"errors"

	"collyrobot/backend/internal/domain"
)

// ErrForumRulesNotImplemented 表示目标论坛的 URL、DOM 或内容持久化规则尚未实现。
var ErrForumRulesNotImplemented = errors.New("forum topic rules are not implemented")

// TopicFetcher 封装单个主题的抓取流程。
// syncConcurrency 是该主题内部允许的最大页面请求数，不等于全局 Worker 数量。
type TopicFetcher interface {
	Fetch(context.Context, domain.Topic, int) error
}

// ForumFetcherStub 保留给轻量测试或未接入 Colly 编排器的场景。
// 应用运行时使用 CollyForumFetcher，具体论坛规则由 ForumPageRulesStub 代替。
type ForumFetcherStub struct{}

func (ForumFetcherStub) Fetch(_ context.Context, _ domain.Topic, _ int) error {
	return ErrForumRulesNotImplemented
}

// FirstAuthorPage 是“只看作者”首页的解析结果。
// Contents 必须包含首页正文，避免在异步分页阶段重复请求第 1 页。
type FirstAuthorPage struct {
	TotalPages int
	Contents   []domain.PageContent
}
