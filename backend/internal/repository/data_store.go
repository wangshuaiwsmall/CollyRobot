package repository

import (
	"context"
	"time"

	"collyrobot/backend/internal/domain"
)

type TopicCounts struct {
	Waiting int `json:"waiting"`
	Done    int `json:"done"`
	Failed  int `json:"failed"`
}

type TopicPage struct {
	Items      []domain.Topic `json:"items"`
	Counts     TopicCounts    `json:"counts"`
	Page       int            `json:"page"`
	PageSize   int            `json:"page_size"`
	Total      int            `json:"total"`
	TotalPages int            `json:"total_pages"`
}

type ContentPreview struct {
	Contents  []domain.PageContent `json:"contents"`
	Total     int                  `json:"total"`
	Displayed int                  `json:"displayed"`
	Truncated bool                 `json:"truncated"`
}

type FullTopicContent struct {
	Title        string `json:"title"`
	ContentCount int    `json:"content_count"`
	Text         string `json:"text"`
}

// DataStore 汇总应用对持久层的全部需求，使 SQLite 与 MongoDB 可以等价替换。
type DataStore interface {
	TopicRepository
	TopicPageRepository
	ListTopicPage(context.Context, domain.TopicStatus, int, int) (TopicPage, error)
	PreviewContents(context.Context, int64, int) (ContentPreview, error)
	FullContent(context.Context, int64) (FullTopicContent, error)
	LoadSetting(context.Context, string) (string, bool, error)
	SaveSetting(context.Context, string, string, time.Time) error
	Ping(context.Context) error
	Close(context.Context) error
}
