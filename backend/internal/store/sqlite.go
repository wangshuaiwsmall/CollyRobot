package store

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
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
		CREATE TABLE IF NOT EXISTS topic_contents (
			topic_id INTEGER NOT NULL,
			uid TEXT NOT NULL,
			page_no INTEGER NOT NULL,
			floor INTEGER NOT NULL,
			text TEXT NOT NULL,
			text_md5 TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (topic_id, uid)
		);
		CREATE INDEX IF NOT EXISTS idx_topic_contents_order ON topic_contents(topic_id, page_no, floor);
		DROP TABLE IF EXISTS topic_page_content;
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

// PrepareFetch 在重新拉取模式下先删除该 Topic 的全部正文。
func (s *TopicStore) PrepareFetch(ctx context.Context, topicID int64, mode domain.FetchMode) error {
	if mode != domain.FetchReload {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM topic_contents WHERE topic_id = ?`, topicID); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveContents 以 topic_id + uid 为唯一键保存正文。
func (s *TopicStore) SaveContents(ctx context.Context, topicID int64, mode domain.FetchMode, contents []domain.PageContent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for _, content := range contents {
		if content.UID == "" {
			return fmt.Errorf("page %d floor %d has empty uid", content.PageNo, content.Floor)
		}
		hash := md5.Sum([]byte(content.Text))
		textMD5 := hex.EncodeToString(hash[:])
		result, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO topic_contents(topic_id, uid, page_no, floor, text, text_md5, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			topicID, content.UID, content.PageNo, content.Floor, content.Text, textMD5, now, now)
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if inserted > 0 || mode != domain.FetchValidate {
			continue
		}
		// 校验模式只在正文 MD5 变化时覆盖内容；UID 相同即视为同一 PageContent。
		if _, err := tx.ExecContext(ctx, `
			UPDATE topic_contents
			SET page_no = ?, floor = ?, text = ?, text_md5 = ?, updated_at = ?
			WHERE topic_id = ? AND uid = ? AND text_md5 <> ?`,
			content.PageNo, content.Floor, content.Text, textMD5, now, topicID, content.UID, textMD5); err != nil {
			return err
		}
	}
	return tx.Commit()
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
