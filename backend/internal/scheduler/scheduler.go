package scheduler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"collyrobot/backend/internal/domain"
	"collyrobot/backend/internal/repository"
	"collyrobot/backend/internal/taskqueue"
	"collyrobot/backend/internal/worker"
)

// Limits 是调度器可动态修改的两级并发上限。
type Limits struct {
	// Workers 表示允许同时处理多少个主题；设置为 0 可暂停领取新主题。
	Workers int `json:"workers"`
	// SyncConcurrency 表示每个主题内部允许同时发起多少个页面请求。
	SyncConcurrency int `json:"sync_concurrency"`
}

// Status 是供管理 API 展示的调度器运行快照。
type Status struct {
	Running       bool   `json:"running"`
	ActiveWorkers int    `json:"active_workers"`
	Completed     uint64 `json:"completed"`
	Failed        uint64 `json:"failed"`
	Indexing      bool   `json:"indexing"`
	Indexed       uint64 `json:"indexed"`
	Queued        int    `json:"queued"`
	Limits        Limits `json:"limits"`
}

// IndexBuilder 是调度器依赖的索引工作流接口，具体实现通常是 indexer.Service。
type IndexBuilder interface {
	Build(context.Context, func([]domain.Topic)) (int, error)
}

// AppLogger 是调度器使用的最小日志接口，便于测试时传入 nil 或替代实现。
type AppLogger interface {
	Printf(string, ...any)
}

type depthLogger interface {
	PrintfDepth(int, string, ...any)
}

// workerSlot 保存单个 Worker 的退役信号。
// 独立退役信号可以在缩容时只停止指定 Worker，而无需取消整个调度器。
type workerSlot struct {
	retire chan struct{}
}

// Scheduler 是系统的全局工作流调度中心，负责：
//  1. 串联索引构建与主题任务消费；
//  2. 按 Workers 上限创建或优雅退役工作实例；
//  3. 向 Worker 下发当前主题内并发上限；
//  4. 汇总完成、失败和索引数量。
//
// mu 保护生命周期和 slots；高频读取的配置与计数器使用原子变量，避免 Worker 之间互相阻塞。
type Scheduler struct {
	repository  repository.TopicRepository
	fetcher     worker.TopicFetcher
	indexer     IndexBuilder
	logger      AppLogger
	indexLogger AppLogger
	crawlLogger AppLogger
	queue       *taskqueue.MemoryQueue

	mu      sync.Mutex
	workers sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
	slots   []workerSlot
	running bool
	nextID  int64
	// indexCancel 只取消当前索引流程，不影响全局 Worker 的生命周期。
	indexCancel context.CancelFunc
	// dispatched 保存 queued 与 running 的 Topic ID；它是运行期去重状态，绝不写入数据库。
	dispatched map[int64]struct{}
	limits     atomic.Pointer[Limits]
	complete   atomic.Uint64
	failed     atomic.Uint64
	indexing   atomic.Bool
	indexed    atomic.Uint64
}

// New 组装调度器，但不会自动启动 Worker；调用方必须显式调用 Start。
func New(topics repository.TopicRepository, fetcher worker.TopicFetcher, indexer IndexBuilder, logger AppLogger, limits Limits) *Scheduler {
	s := &Scheduler{repository: topics, fetcher: fetcher, indexer: indexer, logger: logger, queue: taskqueue.New(), dispatched: make(map[int64]struct{})}
	s.limits.Store(normalize(limits))
	return s
}

// SetWorkflowLoggers 配置索引和正文抓取两条独立日志流。
// 应在调度器启动前调用，避免运行中替换日志器导致工作流记录分散。
func (s *Scheduler) SetWorkflowLoggers(indexerLogger, crawlerLogger AppLogger) {
	s.indexLogger = indexerLogger
	s.crawlLogger = crawlerLogger
}

// BuildIndex 在全局调度器下执行一次索引工作流。
// CompareAndSwap 保证同一时间最多只有一个索引任务，成功后立即唤醒空闲 Worker。
func (s *Scheduler) BuildIndex(ctx context.Context) (int, error) {
	if err := s.reserveIndex(); err != nil {
		return 0, err
	}
	return s.buildReservedIndex(ctx)
}

// TriggerIndex 在调度器的根 Context 下异步启动一次索引构建。
// 它适合 HTTP 管理接口和命令行启动场景：调用方立即得到“已受理”结果，
// 而索引构建与内容 Worker 在后台同步推进。
func (s *Scheduler) TriggerIndex() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return errors.New("scheduler is not running")
	}
	s.mu.Unlock()

	if err := s.reserveIndex(); err != nil {
		return err
	}
	s.mu.Lock()
	if !s.running {
		s.indexing.Store(false)
		s.mu.Unlock()
		return errors.New("scheduler is not running")
	}
	ctx, cancelIndex := context.WithCancel(s.ctx)
	s.indexCancel = cancelIndex
	s.mu.Unlock()
	go func() {
		_, _ = s.buildReservedIndex(ctx)
	}()
	return nil
}

// CancelIndex 只请求取消当前异步索引任务；已持久化的 waiting Topic 会保留，Worker 不会停止。
func (s *Scheduler) CancelIndex() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.indexing.Load() || s.indexCancel == nil {
		return errors.New("no cancellable index build is running")
	}
	s.indexCancel()
	return nil
}

// reserveIndex 在启动索引前完成必要检查，并以原子方式独占索引任务。
func (s *Scheduler) reserveIndex() error {
	if s.indexer == nil {
		return errors.New("index builder is not configured")
	}
	if !s.indexing.CompareAndSwap(false, true) {
		return errors.New("index build is already running")
	}
	return nil
}

// buildReservedIndex 执行已经被 reserveIndex 独占的索引任务。
func (s *Scheduler) buildReservedIndex(ctx context.Context) (int, error) {
	// 即使构建失败也必须释放“索引中”标记，否则后续任务将永远无法启动。
	defer func() {
		s.indexing.Store(false)
		s.mu.Lock()
		s.indexCancel = nil
		s.mu.Unlock()
	}()
	count, err := s.indexer.Build(ctx, func(topics []domain.Topic) {
		// 索引仅建立 waiting 状态；是否进入抓取队列必须由用户指令明确决定。
		s.indexed.Add(uint64(len(topics)))
		s.indexLogf("level=INFO event=index_batch_discovered batch_size=%d", len(topics))
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			s.indexLogf("level=INFO event=index_build_cancelled discovered=%d", count)
			return count, err
		}
		s.indexLogf("level=ERROR event=index_build_failed discovered=%d error=%q", count, err)
		return count, err
	}
	s.indexLogf("level=INFO event=index_build_completed discovered=%d", count)
	return count, nil
}

// Topics 返回全部已索引主题。状态分组由 HTTP 层完成，以便 API 响应与 UI 保持直观。
func (s *Scheduler) Topics(ctx context.Context) ([]domain.Topic, error) {
	return s.repository.ListTopics(ctx)
}

// Start 创建处于待命状态的 Worker。它不会自动从数据库加载任何 Topic，
// 因此服务重启后不会意外开始抓取；入队必须由 QueueWaiting 或 RetryFailed 显式触发。
func (s *Scheduler) Start(parent context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}
	// 清空上一个生命周期的运行期状态。未完成主题在数据库中始终保持 waiting，
	// 下次由用户下达抓取指令时再重新加载。
	s.queue.Reset()
	s.dispatched = make(map[int64]struct{})
	// 所有 Worker 共享根 Context；停止服务时一次取消即可广播给全部实例。
	s.ctx, s.cancel = context.WithCancel(parent)
	s.running = true
	s.reconcileLocked()
	s.logf("level=INFO event=scheduler_started workers=%d sync_concurrency=%d", len(s.slots), s.limits.Load().SyncConcurrency)
	return nil
}

// Stop 取消索引之外的所有 Worker 循环并清理运行期槽位。重复调用是安全的。
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.cancel()
	s.slots = nil
	s.queue.Reset()
	s.dispatched = make(map[int64]struct{})
	s.running = false
	s.logf("level=INFO event=scheduler_stopped")
	s.mu.Unlock()
	// 等待 Worker 完成取消后的状态落库，避免调用方提前关闭 SQLite 和日志文件。
	s.workers.Wait()
}

// QueueWaiting 将全部 waiting Topic 加入当前运行期的内存队列。
// 已经 queued 或 running 的主题受 dispatched 去重保护，重复调用不会造成并发重复抓取。
func (s *Scheduler) QueueWaiting(ctx context.Context) (int, error) {
	topics, err := s.repository.LoadWaiting(ctx)
	if err != nil {
		return 0, err
	}
	return s.enqueueTopics(topics), nil
}

// RetryFailed 将失败主题恢复为 waiting 后加入内存队列。它是显式重试操作，不会自动执行。
func (s *Scheduler) RetryFailed(ctx context.Context) (int, error) {
	topics, err := s.repository.RetryFailed(ctx)
	if err != nil {
		return 0, err
	}
	return s.enqueueTopics(topics), nil
}

// enqueueTopics 维护运行期 queued/running 去重表，再将新任务放进 FIFO 队列。
func (s *Scheduler) enqueueTopics(topics []domain.Topic) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return 0
	}
	newTopics := make([]domain.Topic, 0, len(topics))
	for _, topic := range topics {
		if _, exists := s.dispatched[topic.ID]; exists {
			continue
		}
		newTopics = append(newTopics, topic)
		s.dispatched[topic.ID] = struct{}{}
	}
	queued := s.queue.Enqueue(newTopics...)
	if queued != len(newTopics) {
		// MemoryQueue 理论上不会在这里拒绝新 Topic；保守回滚，保证去重表与队列一致。
		for _, topic := range newTopics {
			delete(s.dispatched, topic.ID)
		}
	}
	return queued
}

// SetLimits 在运行时修改两级并发上限。
// Worker 数量立即开始扩缩容；主题内并发上限从每个 Worker 的下一次任务开始生效，
// 从而避免处理中途改变信号量容量导致竞态或丢失请求。
func (s *Scheduler) SetLimits(limits Limits) Limits {
	normalized := normalize(limits)
	s.limits.Store(normalized)
	s.mu.Lock()
	s.reconcileLocked()
	s.mu.Unlock()
	s.logf("level=INFO event=scheduler_limits_updated workers=%d sync_concurrency=%d", normalized.Workers, normalized.SyncConcurrency)
	return *normalized
}

// Status 返回某一时刻的一致运行快照。
func (s *Scheduler) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		Running: s.running, ActiveWorkers: len(s.slots),
		Completed: s.complete.Load(), Failed: s.failed.Load(), Limits: *s.limits.Load(),
		Indexing: s.indexing.Load(), Indexed: s.indexed.Load(),
		Queued: s.queue.Len(),
	}
}

// reconcileLocked 将实际 Worker 槽位调整到配置目标值。
// 调用者必须持有 s.mu，防止两个配置请求同时扩缩容。
func (s *Scheduler) reconcileLocked() {
	if !s.running {
		return
	}
	target := s.limits.Load().Workers
	for len(s.slots) < target {
		// 扩容：每个实例拥有独立 retire 通道，但共享调度器根 Context。
		s.nextID++
		retire := make(chan struct{})
		s.slots = append(s.slots, workerSlot{retire: retire})
		instance := worker.New(s.nextID, s.fetcher)
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			s.runWorker(s.ctx, retire, instance)
		}()
	}
	for len(s.slots) > target {
		// 缩容：关闭 retire 只阻止实例领取下一任务；当前主题仍会完整执行。
		last := len(s.slots) - 1
		close(s.slots[last].retire)
		s.slots = s.slots[:last]
	}
}

// runWorker 是单个工作实例的常驻消费循环。
// 每轮最多领取一个主题，处理结束并 Reset 后再回到仓库领取下一条。
func (s *Scheduler) runWorker(ctx context.Context, retire <-chan struct{}, instance *worker.Worker) {
	for {
		// 在领取任务前检查停止/退役信号，确保缩容后的实例不会获取新主题。
		select {
		case <-ctx.Done():
			return
		case <-retire:
			return
		default:
		}
		// 高频领取操作只访问内存队列，避免多个 Worker 在 SQLite 写锁上竞争。
		topic, err := s.queue.Dequeue(ctx, retire)
		if errors.Is(err, taskqueue.ErrStopped) || errors.Is(err, context.Canceled) {
			return
		}
		if err != nil {
			continue
		}
		// 在任务边界读取最新配置，本主题整个处理期间保持该值不变。
		concurrency := s.limits.Load().SyncConcurrency
		s.crawlLogf("level=INFO event=topic_started worker_id=%d topic_id=%d concurrency=%d", instance.ID(), topic.ID, concurrency)
		if err := instance.Run(ctx, topic, concurrency); err != nil {
			s.releaseTopic(topic.ID)
			if ctx.Err() != nil {
				// 主动停止属于运行期取消，不是业务失败；Topic 保持 waiting，等待下次显式入队。
				s.crawlLogf("level=INFO event=topic_cancelled worker_id=%d topic_id=%d", instance.ID(), topic.ID)
				return
			}
			s.failed.Add(1)
			s.crawlLogf("level=ERROR event=topic_failed worker_id=%d topic_id=%d error=%q", instance.ID(), topic.ID, err)
			_ = s.repository.MarkFailed(context.WithoutCancel(ctx), topic.ID, err.Error())
			continue
		}
		s.releaseTopic(topic.ID)
		s.complete.Add(1)
		s.crawlLogf("level=INFO event=topic_completed worker_id=%d topic_id=%d", instance.ID(), topic.ID)
		_ = s.repository.MarkDone(context.WithoutCancel(ctx), topic.ID)
	}
}

func (s *Scheduler) releaseTopic(topicID int64) {
	s.mu.Lock()
	delete(s.dispatched, topicID)
	s.mu.Unlock()
}

// logf 允许测试不注入日志器，同时让生产环境的调度事件统一进入后端日志文件。
func (s *Scheduler) logf(format string, args ...any) {
	if s.logger != nil {
		if logger, ok := s.logger.(depthLogger); ok {
			logger.PrintfDepth(1, format, args...)
			return
		}
		s.logger.Printf(format, args...)
	}
}

func (s *Scheduler) indexLogf(format string, args ...any) {
	if s.indexLogger != nil {
		s.indexLogger.Printf(format, args...)
		return
	}
	s.logf(format, args...)
}

func (s *Scheduler) crawlLogf(format string, args ...any) {
	if s.crawlLogger != nil {
		s.crawlLogger.Printf(format, args...)
		return
	}
	s.logf(format, args...)
}

// normalize 把外部输入约束到安全范围：Worker 可为 0，主题内并发至少为 1。
func normalize(limits Limits) *Limits {
	if limits.Workers < 0 {
		limits.Workers = 0
	}
	if limits.SyncConcurrency < 1 {
		limits.SyncConcurrency = 1
	}
	return &limits
}
