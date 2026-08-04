package crawler

import (
	"fmt"
	"net/url"
	"time"

	"collyrobot/backend/internal/proxyconfig"
	"github.com/gocolly/colly/v2"
)

// Service 统一创建 Colly Collector，并集中保存所有抓取器共享的默认策略。
// 论坛适配器不应在 HTTP Handler 中直接创建 Collector，否则超时、深度、限速等配置会散落各处。
type Service struct {
	// RequestDelay 和 RandomDelay 共同打散请求节奏，避免形成固定频率。
	RequestDelay time.Duration
	RandomDelay  time.Duration
	UserAgent    string
	proxy        *proxyconfig.Manager
}

// NewService 创建无状态的爬虫基础服务。
func NewService(proxyManagers ...*proxyconfig.Manager) *Service {
	var proxyManager *proxyconfig.Manager
	if len(proxyManagers) > 0 {
		proxyManager = proxyManagers[0]
	}
	return &Service{
		RequestDelay: 500 * time.Millisecond,
		RandomDelay:  750 * time.Millisecond,
		UserAgent:    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		proxy:        proxyManager,
	}
}

// NewProbeCollector 创建用于同步探测“只看作者”首页的 Collector。
// 首页负责解析总页数，因此不启用异步模式，便于在返回前获得完整分页信息。
func (s *Service) NewProbeCollector() *colly.Collector {
	collector := colly.NewCollector(
		colly.MaxDepth(1),
		colly.UserAgent(s.UserAgent),
	)
	s.configureBrowserHeaders(collector)
	// DomainGlob 和延迟均来自内部常量/已校验配置，此处不会产生配置错误。
	_ = collector.Limit(s.limitRule("*", 1))
	return collector
}

// NewIndexCollector 创建同步的论坛索引 Collector。
// 不设置 MaxDepth，允许 Request.Visit 持续访问分页；最大页数由 indexer 层显式限制。
// AllowedDomains 防止错误的“下一页”链接跳转到论坛站点之外。
func (s *Service) NewIndexCollector(startURL string) (*colly.Collector, error) {
	parsed, err := url.Parse(startURL)
	if err != nil {
		return nil, fmt.Errorf("parse index start url: %w", err)
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("index start url has no host: %s", startURL)
	}
	collector := colly.NewCollector(
		colly.AllowedDomains(parsed.Hostname()),
		colly.UserAgent(s.UserAgent),
	)
	s.configureBrowserHeaders(collector)
	if err := collector.Limit(s.limitRule(parsed.Hostname(), 1)); err != nil {
		return nil, fmt.Errorf("configure index collector limit: %w", err)
	}
	return collector, nil
}

// NewPageCollector 创建主题内部的异步分页 Collector。
// LimitRule 的 Parallelism 精确对应调度器传入的 syncConcurrency；
// 该 Collector 仅服务一个主题，因此不同主题之间仍由全局 Worker 数量控制。
func (s *Service) NewPageCollector(domain string, parallelism int) (*colly.Collector, error) {
	if domain == "" {
		return nil, fmt.Errorf("crawler domain is empty")
	}
	if parallelism < 1 {
		parallelism = 1
	}
	collector := colly.NewCollector(
		colly.Async(true),
		colly.MaxDepth(1),
		colly.UserAgent(s.UserAgent),
	)
	s.configureBrowserHeaders(collector)
	if err := collector.Limit(s.limitRule(domain, parallelism)); err != nil {
		return nil, fmt.Errorf("configure collector limit: %w", err)
	}
	return collector, nil
}

func (s *Service) limitRule(domain string, parallelism int) *colly.LimitRule {
	return &colly.LimitRule{
		DomainGlob:  domain,
		Parallelism: parallelism,
		Delay:       s.RequestDelay,
		RandomDelay: s.RandomDelay,
	}
}

// configureBrowserHeaders 补齐普通浏览器导航请求常见的协商头。
// 不设置 Sec-CH-UA/Sec-Fetch 等与真实浏览器运行状态强绑定的头，避免产生自相矛盾的指纹。
func (s *Service) configureBrowserHeaders(collector *colly.Collector) {
	if s.proxy != nil {
		collector.SetProxyFunc(s.proxy.ProxyFunc())
	}
	collector.OnRequest(func(request *colly.Request) {
		request.Headers.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
		request.Headers.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
		request.Headers.Set("Cache-Control", "no-cache")
		request.Headers.Set("Upgrade-Insecure-Requests", "1")
		if request.Headers.Get("Referer") == "" && request.URL != nil {
			request.Headers.Set("Referer", request.URL.Scheme+"://"+request.URL.Host+"/")
		}
	})
}
