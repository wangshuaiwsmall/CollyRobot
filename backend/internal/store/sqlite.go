package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"collyrobot/backend/internal/domain"
	"collyrobot/backend/internal/repository"
	_ "modernc.org/sqlite"
)

// TopicStore 是 TopicRepository 的 SQLite 实现。
// 它只封装主题索引和任务状态，不包含任何论坛页面解析逻辑。
type TopicStore struct{ db *sql.DB }

// OpenSQLite 打开数据库、验证连接并执行幂等建表。
func OpenSQLite(path string) (*sql.DB, error) {
	// SQLite 文件所在目录可能尚不存在，启动时自动创建以简化部署。
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	// sql.Open 只创建连接池，不保证文件此时可访问，因此需要主动 Ping。
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return db, nil
}

// NewTopicStore 使用已初始化的共享连接池创建主题仓库。
func NewTopicStore(db *sql.DB) *TopicStore { return &TopicStore{db: db} }

// migrate 创建当前版本需要的数据表和查询索引。
// IF NOT EXISTS 让该操作可在每次启动时安全重复执行；后续复杂升级应迁移到版本化 migration。
func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS topics (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			external_id TEXT NOT NULL UNIQUE,
			title TEXT NOT NULL,
			author_id TEXT NOT NULL,
			url TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'waiting',
			last_error TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_topics_status_id ON topics(status, id);
		CREATE TABLE IF NOT EXISTS topic_page_content (
			topic_id INTEGER NOT NULL,
			page_no INTEGER NOT NULL,
			contents_json TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (topic_id, page_no)
		);
	CREATE INDEX IF NOT EXISTS idx_topic_page_content_topic_page ON topic_page_content(topic_id, page_no);
		CREATE TABLE IF NOT EXISTS app_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		);
	`)
	if err != nil {
		return err
	}
	// 兼容旧版本数据库：pending/running 是过去的持久化调度状态，升级后统一视为等待抓取。
	_, err = db.Exec(`UPDATE topics SET status = 'waiting' WHERE status IN ('pending', 'running')`)
	return err
}

// UpsertDiscovered 在一个事务中保存本次索引发现的全部主题，并返回真正新增的记录。
// 只有新增记录会进入内存队列，重复索引不会造成重复抓取。
func (s *TopicStore) UpsertDiscovered(ctx context.Context, topics []domain.Topic) ([]domain.Topic, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Commit 后 Rollback 会成为无操作；提前 defer 可覆盖中途任意失败分支。
	defer tx.Rollback()
	now := time.Now().Unix()
	newTopics := make([]domain.Topic, 0, len(topics))
	for _, topic := range topics {
		// 任务分配已移到内存队列，因此这里使用简单的 INSERT OR IGNORE 去重，
		// 不再依赖 UPDATE ... RETURNING 等数据库特有的原子领取能力。
		result, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO topics(external_id, title, author_id, url, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'waiting', ?, ?)`,
			topic.ExternalID, topic.Title, topic.AuthorID, topic.URL, now, now,
		)
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected == 1 {
			topic.ID, err = result.LastInsertId()
			if err != nil {
				return nil, err
			}
			topic.Status = domain.TopicWaiting
			topic.CreatedAt = time.Unix(now, 0)
			topic.UpdatedAt = time.Unix(now, 0)
			newTopics = append(newTopics, topic)
			continue
		}
		// 已存在主题仅刷新索引元数据，保留 done/failed/running 状态，且绝不重新进入队列。
		if _, err := tx.ExecContext(ctx,
			`UPDATE topics SET title = ?, author_id = ?, url = ?, updated_at = ? WHERE external_id = ?`,
			topic.Title, topic.AuthorID, topic.URL, now, topic.ExternalID,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return newTopics, nil
}

// LoadWaiting 读取等待主题。数据库不记录 queued/running，是否派发由调度器内存状态决定。
func (s *TopicStore) LoadWaiting(ctx context.Context) ([]domain.Topic, error) {
	return s.listTopics(ctx, `WHERE status = 'waiting'`)
}

// ListTopics 返回全部 Topic；实时 queued/running 状态由调度器内存管理，不在此查询范围内。
func (s *TopicStore) ListTopics(ctx context.Context) ([]domain.Topic, error) {
	return s.listTopics(ctx, "")
}

func (s *TopicStore) listTopics(ctx context.Context, filter string) ([]domain.Topic, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, external_id, title, author_id, url, status, last_error, created_at, updated_at
		FROM topics `+filter+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var topics []domain.Topic
	for rows.Next() {
		topic, err := scanTopic(rows)
		if err != nil {
			return nil, err
		}
		topics = append(topics, topic)
	}
	return topics, rows.Err()
}

// RetryFailed 将所有失败主题恢复为 waiting，并仅返回本次恢复的主题。
// 先读取再更新可避免把原本 waiting 的主题误认为重试目标。
func (s *TopicStore) RetryFailed(ctx context.Context) ([]domain.Topic, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, external_id, title, author_id, url, status, last_error, created_at, updated_at
		FROM topics WHERE status = 'failed' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var topics []domain.Topic
	for rows.Next() {
		topic, err := scanTopic(rows)
		if err != nil {
			return nil, err
		}
		topic.Status = domain.TopicWaiting
		topic.LastError = ""
		topics = append(topics, topic)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 关闭读取游标后再更新，避免 SQLite 在单连接或严格锁配置下读写互相阻塞。
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(topics) == 0 {
		return topics, nil
	}
	now := time.Now().Unix()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE topics SET status = 'waiting', last_error = '', updated_at = ? WHERE status = 'failed'`, now); err != nil {
		return nil, err
	}
	return topics, nil
}

// LoadPages 返回主题已经缓存的全部论坛页。
// 这是启动断点续爬时唯一需要读取的正文缓存，不参与高频任务分配。
func (s *TopicStore) LoadPages(ctx context.Context, topicID int64) (map[int][]domain.PageContent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT page_no, contents_json FROM topic_page_content WHERE topic_id = ? ORDER BY page_no`, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pages := make(map[int][]domain.PageContent)
	for rows.Next() {
		var pageNo int
		var encoded string
		if err := rows.Scan(&pageNo, &encoded); err != nil {
			return nil, err
		}
		var contents []domain.PageContent
		if err := json.Unmarshal([]byte(encoded), &contents); err != nil {
			return nil, fmt.Errorf("decode cached page %d: %w", pageNo, err)
		}
		pages[pageNo] = contents
	}
	return pages, rows.Err()
}

// SavePage 立即持久化单页抓取结果。先 UPDATE、再按需 INSERT，
// 只依赖常见 SQL 语句，不需要特定数据库的 UPSERT 语法。
func (s *TopicStore) SavePage(ctx context.Context, topicID int64, pageNo int, contents []domain.PageContent) error {
	encoded, err := json.Marshal(contents)
	if err != nil {
		return fmt.Errorf("encode page %d: %w", pageNo, err)
	}
	now := time.Now().Unix()
	result, err := s.db.ExecContext(ctx,
		`UPDATE topic_page_content SET contents_json = ?, updated_at = ? WHERE topic_id = ? AND page_no = ?`,
		string(encoded), now, topicID, pageNo,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO topic_page_content(topic_id, page_no, contents_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		topicID, pageNo, string(encoded), now, now,
	)
	return err
}

// MarkDone 清空历史错误并把主题置为完成状态。
func (s *TopicStore) MarkDone(ctx context.Context, id int64) error {
	return s.setResult(ctx, id, domain.TopicDone, "")
}

// MarkFailed 保存失败原因。后续增加重试机制时可在此扩展重试次数和下次执行时间。
func (s *TopicStore) MarkFailed(ctx context.Context, id int64, message string) error {
	return s.setResult(ctx, id, domain.TopicFailed, message)
}

// setResult 汇总完成/失败的公共状态更新逻辑，并检查目标主题是否存在。
func (s *TopicStore) setResult(ctx context.Context, id int64, status domain.TopicStatus, message string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE topics SET status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		status, message, time.Now().Unix(), id,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err == nil && affected == 0 {
		return fmt.Errorf("topic %d not found", id)
	}
	return err
}

// rowScanner 同时兼容 *sql.Row 和 *sql.Rows，使映射逻辑可以复用和单元测试。
type rowScanner interface{ Scan(...any) error }

// scanTopic 将 SQLite 原始字段转换为领域模型，并统一翻译“无记录”错误。
func scanTopic(row rowScanner) (domain.Topic, error) {
	var topic domain.Topic
	var createdAt, updatedAt int64
	err := row.Scan(&topic.ID, &topic.ExternalID, &topic.Title, &topic.AuthorID, &topic.URL,
		&topic.Status, &topic.LastError, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Topic{}, repository.ErrNoWaitingTopic
	}
	if err != nil {
		return domain.Topic{}, err
	}
	// 数据库存 Unix 秒，领域层使用 time.Time，避免上层依赖具体存储格式。
	topic.CreatedAt = time.Unix(createdAt, 0)
	topic.UpdatedAt = time.Unix(updatedAt, 0)
	return topic, nil
}
