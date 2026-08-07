package worker

import (
	"context"
	"fmt"
	"sync/atomic"

	"collyrobot/backend/internal/domain"
)

// Worker 是可复用的单主题工作实例。
// 一个实例同一时间只处理一个 Topic；fetcher 可以在主题内部创建受限数量的协程。
type Worker struct {
	// id 在调度器生命周期内单调递增，便于日志追踪具体工作实例。
	id      int64
	fetcher TopicFetcher
	// current 使用原子指针保存当前任务，便于未来安全地暴露运行状态或诊断信息。
	current atomic.Pointer[domain.Topic]
}

// New 创建 Worker 并注入论坛正文抓取器。
func New(id int64, fetcher TopicFetcher) *Worker {
	return &Worker{id: id, fetcher: fetcher}
}

// ID 返回该 Worker 的运行期唯一编号。
func (w *Worker) ID() int64 { return w.id }

// Run 绑定并处理一个主题。无论成功、失败还是取消，返回前都会执行 Reset。
func (w *Worker) Run(ctx context.Context, topic domain.Topic, syncConcurrency int) error {
	w.current.Store(&topic)
	// defer 确保失败路径也不会残留上一个主题的状态，从而可以安全复用实例。
	defer w.Reset()
	// 防御性兜底：即使上层传入非法配置，也至少允许一个同步抓取协程。
	if syncConcurrency < 1 {
		syncConcurrency = 1
	}
	if err := w.fetcher.Fetch(ctx, topic, syncConcurrency, topic.FetchMode); err != nil {
		return fmt.Errorf("worker %d fetch topic %d: %w", w.id, topic.ID, err)
	}
	return nil
}

// Reset 清除本次任务状态。未来新增临时章节、分页队列等字段时，也应在此统一释放。
func (w *Worker) Reset() { w.current.Store(nil) }
