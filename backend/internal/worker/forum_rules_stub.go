package worker

import (
	"context"

	"collyrobot/backend/internal/domain"
	"github.com/gocolly/colly/v2"
)

// ForumPageRulesStub 是具体 BBS 规则的占位实现。
// 目标论坛确定后，应实现该接口或新增另一套规则类型，再注入 NewCollyForumFetcher。
type ForumPageRulesStub struct{}

func (ForumPageRulesStub) BuildAuthorPageURL(_ domain.Topic, _ int) (string, error) {
	// TODO(forum-adapter)：
	// 1. 解析 topic.URL 中的主题 ID 或版块参数。
	// 2. 按目标 BBS “只看作者”规则拼接 pageNo、topic.AuthorID 等查询参数。
	// 3. 返回绝对 URL；不要在此处发起网络请求。
	return "", ErrForumRulesNotImplemented
}

func (ForumPageRulesStub) ParseFirstAuthorPage(_ *colly.Response) (FirstAuthorPage, error) {
	// TODO(forum-adapter)：
	// 1. 使用 CSS Selector / XPath 解析总页数；无分页时设为 1。
	// 2. 解析当前第一页作者楼层内容，并按页面原始顺序填入 Contents。
	// 3. 发现登录页、验证码页或反爬页面时返回可识别错误。
	return FirstAuthorPage{}, ErrForumRulesNotImplemented
}

func (ForumPageRulesStub) ParseAuthorPage(_ *colly.Response) ([]domain.PageContent, error) {
	// TODO(forum-adapter)：
	// 1. 定位“只看作者”结果中的每个正文楼层。
	// 2. 清理引用、签名、广告和无关 HTML，保留正文文本与 Floor。
	// 3. 必须按 DOM 中的楼层顺序返回内容。
	return nil, ErrForumRulesNotImplemented
}

func (ForumPageRulesStub) PersistNovel(_ context.Context, _ domain.Topic, _ []domain.PageContent) error {
	// TODO(storage)：
	// 1. 创建小说、章节和正文表或文档模型。
	// 2. 在事务中写入已经按页码排序的完整内容。
	// 3. 所有内容成功落库后才返回 nil，供调度器标记 TopicDone。
	return ErrForumRulesNotImplemented
}

var _ ForumPageRules = ForumPageRulesStub{}
