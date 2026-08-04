package worker

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"collyrobot/backend/internal/crawler"
	"collyrobot/backend/internal/domain"
	"collyrobot/backend/internal/repository"
	"github.com/gocolly/colly/v2"
)

const (
	pageNumberContextKey = "page_no"
	attemptContextKey    = "attempt"
)

// ForumPageRules 隔离与具体 BBS 有关的逻辑。
// CollyForumFetcher 只负责编排并发、重试、顺序合并和取消，不关心 HTML 选择器或 URL 参数名。
type ForumPageRules interface {
	BuildAuthorPageURL(topic domain.Topic, pageNo int) (string, error)
	ParseFirstAuthorPage(response *colly.Response) (FirstAuthorPage, error)
	ParseAuthorPage(response *colly.Response) ([]domain.PageContent, error)
	PersistNovel(context.Context, domain.Topic, []domain.PageContent) error
}

// CollyForumFetcher 是一个主题一个 Collector 的抓取编排器。
// 同一主题最多发起 syncConcurrency 个分页请求；不同主题的并行度由 Scheduler Worker 数控制。
type CollyForumFetcher struct {
	crawler    *crawler.Service
	rules      ForumPageRules
	pages      repository.TopicPageRepository
	maxRetries int
	retryDelay time.Duration
}

func NewCollyForumFetcher(crawlerService *crawler.Service, rules ForumPageRules, pages repository.TopicPageRepository) *CollyForumFetcher {
	return &CollyForumFetcher{
		crawler: crawlerService, rules: rules, pages: pages,
		maxRetries: 2, retryDelay: 500 * time.Millisecond,
	}
}

// Fetch 按“同步首页探测 -> 异步分页抓取 -> 页码排序 -> 最终持久化”执行一个主题。
func (f *CollyForumFetcher) Fetch(ctx context.Context, topic domain.Topic, syncConcurrency int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	firstURL, err := f.rules.BuildAuthorPageURL(topic, 1)
	if err != nil {
		return fmt.Errorf("build first author page url: %w", err)
	}
	firstPage, err := f.probeFirstPage(ctx, firstURL)
	if err != nil {
		return err
	}
	if firstPage.TotalPages < 1 {
		return fmt.Errorf("invalid total pages %d", firstPage.TotalPages)
	}

	// 先读取已有临时页。已缓存页面无需重复请求，服务重启后可从中断位置继续。
	pages, err := f.pages.LoadPages(ctx, topic.ID)
	if err != nil {
		return fmt.Errorf("load cached pages: %w", err)
	}
	if _, exists := pages[1]; !exists {
		if err := f.savePage(ctx, topic.ID, 1, firstPage.Contents); err != nil {
			return err
		}
		pages[1] = normalizePageContents(1, firstPage.Contents)
	}
	if err := f.fetchRemainingPages(ctx, topic, firstURL, firstPage.TotalPages, syncConcurrency, pages); err != nil {
		return err
	}
	pages, err = f.pages.LoadPages(ctx, topic.ID)
	if err != nil {
		return fmt.Errorf("reload cached pages: %w", err)
	}
	if len(pages) != firstPage.TotalPages {
		return fmt.Errorf("incomplete cached pages: expected %d, got %d", firstPage.TotalPages, len(pages))
	}
	return f.rules.PersistNovel(ctx, topic, mergePagesInOrder(pages))
}

func (f *CollyForumFetcher) probeFirstPage(ctx context.Context, firstURL string) (FirstAuthorPage, error) {
	collector := f.crawler.NewProbeCollector()
	var firstPage FirstAuthorPage
	var parseErr error
	collector.OnRequest(func(request *colly.Request) {
		if ctx.Err() != nil {
			request.Abort()
		}
	})
	collector.OnResponse(func(response *colly.Response) {
		firstPage, parseErr = f.rules.ParseFirstAuthorPage(response)
	})
	collector.OnError(func(_ *colly.Response, err error) {
		parseErr = err
	})
	if err := collector.Visit(firstURL); err != nil {
		return FirstAuthorPage{}, fmt.Errorf("request first author page: %w", err)
	}
	if parseErr != nil {
		return FirstAuthorPage{}, fmt.Errorf("parse first author page: %w", parseErr)
	}
	if err := ctx.Err(); err != nil {
		return FirstAuthorPage{}, err
	}
	return firstPage, nil
}

func (f *CollyForumFetcher) fetchRemainingPages(ctx context.Context, topic domain.Topic, firstURL string, totalPages, syncConcurrency int, cachedPages map[int][]domain.PageContent) error {
	parsedURL, err := url.Parse(firstURL)
	if err != nil {
		return fmt.Errorf("parse first author page url: %w", err)
	}
	if parsedURL.Hostname() == "" {
		return fmt.Errorf("first author page url has no host: %s", firstURL)
	}
	collector, err := f.crawler.NewPageCollector(parsedURL.Hostname(), syncConcurrency)
	if err != nil {
		return err
	}

	jobs := make([]pageJob, 0, totalPages-1)
	for pageNo := 2; pageNo <= totalPages; pageNo++ {
		if _, exists := cachedPages[pageNo]; exists {
			continue
		}
		pageURL, err := f.rules.BuildAuthorPageURL(topic, pageNo)
		if err != nil {
			return fmt.Errorf("build author page %d url: %w", pageNo, err)
		}
		jobs = append(jobs, pageJob{pageNo: pageNo, url: pageURL})
	}
	if len(jobs) == 0 {
		return nil
	}

	results := make(chan pageResult, len(jobs))
	collector.OnRequest(func(request *colly.Request) {
		if ctx.Err() != nil {
			request.Abort()
		}
	})
	collector.OnResponse(func(response *colly.Response) {
		pageNo := pageNumberFromContext(response.Ctx)
		contents, err := f.rules.ParseAuthorPage(response)
		results <- pageResult{pageNo: pageNo, contents: normalizePageContents(pageNo, contents), err: err}
	})
	collector.OnError(func(response *colly.Response, requestErr error) {
		if response != nil && response.Request != nil && f.retry(ctx, response) {
			return
		}
		pageNo := 0
		if response != nil {
			pageNo = pageNumberFromContext(response.Ctx)
		}
		results <- pageResult{pageNo: pageNo, err: requestErr}
	})

	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		requestCtx := colly.NewContext()
		requestCtx.Put(pageNumberContextKey, strconv.Itoa(job.pageNo))
		requestCtx.Put(attemptContextKey, "0")
		if err := collector.Request("GET", job.url, nil, requestCtx, nil); err != nil {
			return fmt.Errorf("schedule author page %d: %w", job.pageNo, err)
		}
	}
	go func() {
		collector.Wait()
		close(results)
	}()
	var firstErr error
	for result := range results {
		if result.err != nil {
			// 不立即返回：其他并发请求可能已经成功，仍应写入临时页表供下次续爬复用。
			if firstErr == nil {
				firstErr = fmt.Errorf("fetch author page %d: %w", result.pageNo, result.err)
			}
			continue
		}
		if result.pageNo < 2 || result.pageNo > totalPages {
			if firstErr == nil {
				firstErr = fmt.Errorf("received invalid page result %d", result.pageNo)
			}
			continue
		}
		// 保存动作在结果到达时执行，而不是 Wait 之后统一写入；失败后已有页面可供续爬复用。
		if err := f.savePage(ctx, topic.ID, result.pageNo, result.contents); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return nil
}

func (f *CollyForumFetcher) retry(ctx context.Context, response *colly.Response) bool {
	request := response.Request
	attempt, _ := strconv.Atoi(request.Ctx.Get(attemptContextKey))
	if attempt >= f.maxRetries {
		return false
	}
	delay := f.retryDelay * time.Duration(1<<attempt)
	if serverDelay, ok := retryAfter(response.Headers.Get("Retry-After"), time.Now()); ok && serverDelay > delay {
		delay = serverDelay
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
	}
	request.Ctx.Put(attemptContextKey, strconv.Itoa(attempt+1))
	return request.Retry() == nil
}

func retryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	if delay := when.Sub(now); delay > 0 {
		return delay, true
	}
	return 0, true
}

type pageResult struct {
	pageNo   int
	contents []domain.PageContent
	err      error
}

type pageJob struct {
	pageNo int
	url    string
}

func pageNumberFromContext(ctx *colly.Context) int {
	pageNo, _ := strconv.Atoi(ctx.Get(pageNumberContextKey))
	return pageNo
}

func (f *CollyForumFetcher) savePage(ctx context.Context, topicID int64, pageNo int, contents []domain.PageContent) error {
	normalized := normalizePageContents(pageNo, contents)
	// 页面已经收到完整 HTTP 响应后，即使上层正在停止，也尽量保留该页作为下次续爬断点。
	if err := f.pages.SavePage(context.WithoutCancel(ctx), topicID, pageNo, normalized); err != nil {
		return fmt.Errorf("save page %d: %w", pageNo, err)
	}
	return nil
}

func normalizePageContents(pageNo int, contents []domain.PageContent) []domain.PageContent {
	for index := range contents {
		contents[index].PageNo = pageNo
	}
	return contents
}

func mergePagesInOrder(pages map[int][]domain.PageContent) []domain.PageContent {
	pageNumbers := make([]int, 0, len(pages))
	for pageNo := range pages {
		pageNumbers = append(pageNumbers, pageNo)
	}
	sort.Ints(pageNumbers)
	merged := make([]domain.PageContent, 0)
	for _, pageNo := range pageNumbers {
		contents := pages[pageNo]
		// 同一页的规则实现必须保留 DOM 楼层顺序；Floor 仅用于后续额外校验和展示。
		merged = append(merged, contents...)
	}
	return merged
}

var _ TopicFetcher = (*CollyForumFetcher)(nil)
