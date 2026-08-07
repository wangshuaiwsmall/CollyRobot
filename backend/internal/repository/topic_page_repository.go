package repository

import (
	"context"

	"collyrobot/backend/internal/domain"
)

// TopicPageRepository 按 Topic + UID 保存稳定的正文片段。
type TopicPageRepository interface {
	// PrepareFetch 在重新拉取模式下删除 Topic 的全部旧正文，其他模式不修改数据。
	PrepareFetch(context.Context, int64, domain.FetchMode) error
	// SaveContents 按模式保存当前抓到的正文；UID 在同一 Topic 内唯一。
	SaveContents(context.Context, int64, domain.FetchMode, []domain.PageContent) error
}
