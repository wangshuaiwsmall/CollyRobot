package indexer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"collyrobot/backend/internal/crawler"
	"collyrobot/backend/internal/domain"
	"github.com/gocolly/colly/v2"
)

// ForumIndexRules 隔离目标论坛的列表 DOM 和链接规则。
// CollyForumIndexCrawler 只负责单协程访问、流式提交和安全翻页。
type ForumIndexRules interface {
	StartURL() string
	PageSelector() string
	ParseTopicList(*colly.HTMLElement) ([]domain.Topic, error)
	NextPageURL(*colly.HTMLElement) (string, bool)
}

// CollyForumIndexCrawler 使用一个同步 Colly Collector 递归访问论坛索引页。
// 每处理完一页即调用 emit；调用 Request.Visit 后才继续下一页，因而天然对 emit 形成背压。
type CollyForumIndexCrawler struct {
	crawler  *crawler.Service
	rules    ForumIndexRules
	maxPages int
}

func NewCollyForumIndexCrawler(crawlerService *crawler.Service, rules ForumIndexRules) *CollyForumIndexCrawler {
	return &CollyForumIndexCrawler{crawler: crawlerService, rules: rules, maxPages: 10_000}
}

func (c *CollyForumIndexCrawler) CrawlTopicIndex(ctx context.Context, emit TopicBatchEmitter) error {
	startURL := strings.TrimSpace(c.rules.StartURL())
	if startURL == "" {
		return ErrForumRulesNotImplemented
	}
	selector := strings.TrimSpace(c.rules.PageSelector())
	if selector == "" {
		return fmt.Errorf("forum index page selector is empty")
	}
	collector, err := c.crawler.NewIndexCollector(startURL)
	if err != nil {
		return err
	}

	var callbackErr error
	pageCount := 0
	collector.OnRequest(func(request *colly.Request) {
		if ctx.Err() != nil {
			request.Abort()
		}
	})
	collector.OnError(func(_ *colly.Response, requestErr error) {
		if callbackErr == nil {
			callbackErr = requestErr
		}
	})
	collector.OnHTML(selector, func(element *colly.HTMLElement) {
		if callbackErr != nil || ctx.Err() != nil {
			return
		}
		pageCount++
		if pageCount > c.maxPages {
			callbackErr = fmt.Errorf("forum index exceeded maximum pages: %d", c.maxPages)
			return
		}

		topics, err := c.rules.ParseTopicList(element)
		if err != nil {
			callbackErr = fmt.Errorf("parse index page %s: %w", element.Request.URL, err)
			return
		}
		if len(topics) > 0 {
			// emit 成功意味着当前页主题已经完成持久化并进入内存队列。
			if err := emit(topics); err != nil {
				callbackErr = fmt.Errorf("emit index page %s: %w", element.Request.URL, err)
				return
			}
		}

		nextURL, found := c.rules.NextPageURL(element)
		if !found || strings.TrimSpace(nextURL) == "" {
			return
		}
		absoluteURL := element.Request.AbsoluteURL(nextURL)
		visited, err := element.Request.HasVisited(absoluteURL)
		if err != nil {
			callbackErr = fmt.Errorf("check next index page %s: %w", absoluteURL, err)
			return
		}
		if visited {
			// Colly 的已访问记录终止重复分页链接，防止论坛页面形成环路。
			return
		}
		if err := element.Request.Visit(absoluteURL); err != nil && !isAlreadyVisited(err) {
			callbackErr = fmt.Errorf("visit next index page %s: %w", absoluteURL, err)
		}
	})

	if err := collector.Visit(startURL); err != nil && !isAlreadyVisited(err) {
		return err
	}
	if callbackErr != nil {
		return callbackErr
	}
	return ctx.Err()
}

func isAlreadyVisited(err error) bool {
	var visitedErr *colly.AlreadyVisitedError
	return errors.As(err, &visitedErr)
}

var _ TopicListCrawler = (*CollyForumIndexCrawler)(nil)
