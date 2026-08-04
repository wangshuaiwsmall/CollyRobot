package indexer

import (
	"errors"

	"collyrobot/backend/internal/domain"
	"github.com/gocolly/colly/v2"
)

// ErrForumRulesNotImplemented 明确表示当前失败源于论坛适配规则尚未实现。
var ErrForumRulesNotImplemented = errors.New("forum index rules are not implemented")

// ForumIndexRulesStub 是具体 BBS 列表页规则的占位实现。
// 确认目标论坛后，应实现 ForumIndexRules 并注入 CollyForumIndexCrawler。
type ForumIndexRulesStub struct{}

func (ForumIndexRulesStub) StartURL() string {
	// TODO(forum-adapter)：返回目标小说版块的第一页绝对 URL。
	return ""
}

func (ForumIndexRulesStub) PageSelector() string {
	// TODO(forum-adapter)：返回每个列表页唯一存在一次的页面容器选择器，例如 "body"。
	return "body"
}

func (ForumIndexRulesStub) ParseTopicList(_ *colly.HTMLElement) ([]domain.Topic, error) {
	// TODO(forum-adapter)：
	// 1. 在当前页容器中查找所有主题行。
	// 2. 解析论坛主题 ID、标题、作者 ID、主题标准 URL。
	// 3. 过滤置顶、公告、非小说分类或无作者信息的主题。
	// 4. 返回当前页的 []domain.Topic；无需手动跨页去重，存储层会按 ExternalID 去重。
	return nil, ErrForumRulesNotImplemented
}

func (ForumIndexRulesStub) NextPageURL(_ *colly.HTMLElement) (string, bool) {
	// TODO(forum-adapter)：
	// 1. 从当前页“下一页”链接读取 href。
	// 2. 最后一页返回 ("", false)。
	// 3. 仅返回相对或绝对 URL，不在规则层调用 Request.Visit。
	return "", false
}

var _ ForumIndexRules = ForumIndexRulesStub{}
