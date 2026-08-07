package domain

import (
	"fmt"
	"strings"
	"time"
)

// TopicStatus 描述一个主题在索引和抓取流水线中的状态。
type TopicStatus string

const (
	// TopicWaiting 表示主题已经被索引，等待用户下达抓取指令或等待后续断点续爬。
	// queued、running 等实时调度状态仅保存在调度器内存，不写入数据库。
	TopicWaiting TopicStatus = "waiting"
	// TopicDone 表示主题正文已经完整抓取并持久化。
	TopicDone TopicStatus = "done"
	// TopicFailed 表示最近一次抓取发生业务或页面错误，具体原因记录在 LastError。
	TopicFailed TopicStatus = "failed"
)

// ParseFetchMode 解析外部抓取模式；空值使用默认增量模式。
func ParseFetchMode(value string) (FetchMode, error) {
	mode := FetchMode(strings.ToLower(strings.TrimSpace(value)))
	if mode == "" {
		return FetchIncremental, nil
	}
	switch mode {
	case FetchIncremental, FetchValidate, FetchReload:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported fetch mode %q", value)
	}
}

// Topic 是索引模块与工作模块之间传递的标准主题记录。
// ExternalID 使用论坛自身的主题编号，是跨多次索引任务去重的稳定键；ID 是本地数据库主键。
type Topic struct {
	ID         int64       `json:"id"`
	ExternalID string      `json:"external_id"`
	Title      string      `json:"title"`
	AuthorID   string      `json:"author_id"`
	URL        string      `json:"url"`
	Status     TopicStatus `json:"status"`
	LastError  string      `json:"last_error,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
	FetchMode  FetchMode   `json:"-"`
}

// FetchMode 决定同一 UID 的正文如何落库。
type FetchMode string

const (
	FetchIncremental FetchMode = "incremental"
	FetchValidate    FetchMode = "validate"
	FetchReload      FetchMode = "reload"
)

// PageContent 是小说正文在单个论坛页面中解析出的一个楼层片段。
// UID 由数据源提供；为空时抓取器使用 Floor + PageNo 生成同 Topic 内的身份。
type PageContent struct {
	PageNo int    `json:"page_no"`
	UID    string `json:"uid"`
	Floor  int    `json:"floor"`
	Text   string `json:"text"`
}
