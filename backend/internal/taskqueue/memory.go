// Package taskqueue 提供单进程内的主题任务队列。
// 队列只承担高频调度；任务的最终状态仍由 repository 持久化。
package taskqueue

import (
	"context"
	"errors"
	"sync"

	"collyrobot/backend/internal/domain"
)

// ErrStopped 表示调用方要求当前 Worker 停止等待新任务。
var ErrStopped = errors.New("task queue wait stopped")

// MemoryQueue 是支持去重、取消等待和重置恢复的内存 FIFO 队列。
// queued 仅记录“尚未被 Worker 取走”的任务，因此 Worker 取走后可在失败时重新入队。
type MemoryQueue struct {
	mu     sync.Mutex
	items  []domain.Topic
	queued map[int64]struct{}
	wake   chan struct{}
}

func New() *MemoryQueue {
	return &MemoryQueue{queued: make(map[int64]struct{}), wake: make(chan struct{}, 1)}
}

// Enqueue 将新主题追加到队列尾部。相同本地 ID 在未被领取前只会保留一份。
func (q *MemoryQueue) Enqueue(topics ...domain.Topic) int {
	q.mu.Lock()
	added := 0
	for _, topic := range topics {
		if _, exists := q.queued[topic.ID]; exists {
			continue
		}
		q.items = append(q.items, topic)
		q.queued[topic.ID] = struct{}{}
		added++
	}
	q.mu.Unlock()
	if added > 0 {
		q.notify()
	}
	return added
}

// Dequeue 阻塞等待下一个主题；Context 取消或 stop 被关闭时立即返回。
// stop 只用于优雅退役单个 Worker，不会取消该 Worker 已经开始的主题抓取。
func (q *MemoryQueue) Dequeue(ctx context.Context, stop <-chan struct{}) (domain.Topic, error) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			topic := q.items[0]
			q.items = q.items[1:]
			delete(q.queued, topic.ID)
			q.mu.Unlock()
			return topic, nil
		}
		q.mu.Unlock()

		select {
		case <-ctx.Done():
			return domain.Topic{}, ctx.Err()
		case <-stop:
			return domain.Topic{}, ErrStopped
		case <-q.wake:
		}
	}
}

// Reset 丢弃当前未领取的内存任务。重启调度器前会从数据库重新恢复，避免陈旧任务残留。
func (q *MemoryQueue) Reset() {
	q.mu.Lock()
	q.items = nil
	q.queued = make(map[int64]struct{})
	q.mu.Unlock()
}

func (q *MemoryQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *MemoryQueue) notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
