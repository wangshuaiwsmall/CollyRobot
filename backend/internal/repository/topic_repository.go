package repository

import (
	"context"
	"errors"

	"collyrobot/backend/internal/domain"
)

// ErrNoWaitingTopic 表示没有可由用户指令加入内存队列的等待主题。
var ErrNoWaitingTopic = errors.New("no waiting topic")

// TopicRepository 定义调度器和索引器所需的最小持久化能力。
// 业务层只依赖该接口，因此以后从 SQLite 切换到 MongoDB 时无需修改调度和抓取逻辑。
type TopicRepository interface {
	// UpsertDiscovered 批量写入索引结果，并返回本次真正新增、处于 waiting 状态的主题。
	// 重复主题只更新元数据，不重置其抓取状态。
	UpsertDiscovered(context.Context, []domain.Topic) ([]domain.Topic, error)
	// LoadWaiting 返回全部可抓取的等待主题；调用方决定是否将它们放入内存队列。
	LoadWaiting(context.Context) ([]domain.Topic, error)
	// RetryFailed 将失败主题恢复为 waiting，并返回恢复后的主题供调用方显式入队。
	RetryFailed(context.Context) ([]domain.Topic, error)
	// ListTopics 返回全部已建立索引的主题，供管理界面按持久化业务状态展示。
	ListTopics(context.Context) ([]domain.Topic, error)
	// MarkDone 将主题标记为已完成。
	MarkDone(context.Context, int64) error
	// MarkFailed 保存失败状态和可诊断的错误信息。
	MarkFailed(context.Context, int64, string) error
}
