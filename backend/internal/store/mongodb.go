package store

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"collyrobot/backend/internal/domain"
	"collyrobot/backend/internal/repository"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type MongoStore struct {
	client   *mongo.Client
	database *mongo.Database
	topics   *mongo.Collection
	contents *mongo.Collection
	settings *mongo.Collection
	counters *mongo.Collection
}

type mongoTopic struct {
	ID         int64              `bson:"id"`
	ExternalID string             `bson:"external_id"`
	Title      string             `bson:"title"`
	AuthorID   string             `bson:"author_id"`
	URL        string             `bson:"url"`
	Status     domain.TopicStatus `bson:"status"`
	LastError  string             `bson:"last_error"`
	CreatedAt  time.Time          `bson:"created_at"`
	UpdatedAt  time.Time          `bson:"updated_at"`
}

type mongoContent struct {
	TopicID   int64     `bson:"topic_id"`
	UID       string    `bson:"uid"`
	PageNo    int       `bson:"page_no"`
	Floor     int       `bson:"floor"`
	Text      string    `bson:"text"`
	TextMD5   string    `bson:"text_md5"`
	CreatedAt time.Time `bson:"created_at"`
	UpdatedAt time.Time `bson:"updated_at"`
}

func OpenMongoStore(ctx context.Context, uri, databaseName string) (*MongoStore, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return nil, err
	}
	database := client.Database(databaseName)
	store := &MongoStore{
		client: client, database: database,
		topics: database.Collection("topics"), contents: database.Collection("topic_contents"),
		settings: database.Collection("app_settings"), counters: database.Collection("counters"),
	}
	if err := store.ensureIndexes(ctx); err != nil {
		_ = client.Disconnect(ctx)
		return nil, err
	}
	return store, nil
}

func (s *MongoStore) ensureIndexes(ctx context.Context) error {
	_, err := s.topics.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "id", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "external_id", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "status", Value: 1}, {Key: "id", Value: 1}}},
	})
	if err != nil {
		return fmt.Errorf("create topic indexes: %w", err)
	}
	_, err = s.contents.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "topic_id", Value: 1}, {Key: "uid", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "topic_id", Value: 1}, {Key: "page_no", Value: 1}, {Key: "floor", Value: 1}, {Key: "uid", Value: 1}}},
	})
	if err != nil {
		return fmt.Errorf("create content indexes: %w", err)
	}
	_, err = s.settings.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "key", Value: 1}}, Options: options.Index().SetUnique(true)})
	return err
}

func (s *MongoStore) nextTopicID(ctx context.Context) (int64, error) {
	var counter struct {
		Value int64 `bson:"value"`
	}
	err := s.counters.FindOneAndUpdate(ctx, bson.M{"_id": "topics"}, bson.M{"$inc": bson.M{"value": 1}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&counter)
	return counter.Value, err
}

func (s *MongoStore) UpsertDiscovered(ctx context.Context, topics []domain.Topic) ([]domain.Topic, error) {
	created := make([]domain.Topic, 0)
	for _, topic := range topics {
		now := time.Now().UTC()
		update := bson.M{"$set": bson.M{"title": topic.Title, "author_id": topic.AuthorID, "url": topic.URL, "updated_at": now}}
		result, err := s.topics.UpdateOne(ctx, bson.M{"external_id": topic.ExternalID}, update)
		if err != nil {
			return nil, err
		}
		if result.MatchedCount > 0 {
			continue
		}
		id, err := s.nextTopicID(ctx)
		if err != nil {
			return nil, err
		}
		doc := mongoTopic{ID: id, ExternalID: topic.ExternalID, Title: topic.Title, AuthorID: topic.AuthorID, URL: topic.URL,
			Status: domain.TopicWaiting, CreatedAt: now, UpdatedAt: now}
		if _, err := s.topics.InsertOne(ctx, doc); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				_, err = s.topics.UpdateOne(ctx, bson.M{"external_id": topic.ExternalID}, update)
				if err == nil {
					continue
				}
			}
			return nil, err
		}
		topic.ID, topic.Status, topic.CreatedAt, topic.UpdatedAt = id, domain.TopicWaiting, now, now
		created = append(created, topic)
	}
	return created, nil
}

func (s *MongoStore) LoadWaiting(ctx context.Context) ([]domain.Topic, error) {
	return s.findTopics(ctx, bson.M{"status": domain.TopicWaiting})
}

func (s *MongoStore) ListTopics(ctx context.Context) ([]domain.Topic, error) {
	return s.findTopics(ctx, bson.M{})
}

func (s *MongoStore) findTopics(ctx context.Context, filter bson.M) ([]domain.Topic, error) {
	cursor, err := s.topics.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	items := make([]domain.Topic, 0)
	for cursor.Next(ctx) {
		var doc mongoTopic
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		items = append(items, topicFromMongo(doc))
	}
	return items, cursor.Err()
}

func (s *MongoStore) RetryFailed(ctx context.Context) ([]domain.Topic, error) {
	items, err := s.findTopics(ctx, bson.M{"status": domain.TopicFailed})
	if err != nil || len(items) == 0 {
		return items, err
	}
	now := time.Now().UTC()
	_, err = s.topics.UpdateMany(ctx, bson.M{"status": domain.TopicFailed}, bson.M{"$set": bson.M{"status": domain.TopicWaiting, "last_error": "", "updated_at": now}})
	for index := range items {
		items[index].Status, items[index].LastError, items[index].UpdatedAt = domain.TopicWaiting, "", now
	}
	return items, err
}

func (s *MongoStore) MarkDone(ctx context.Context, id int64) error {
	return s.setResult(ctx, id, domain.TopicDone, "")
}

func (s *MongoStore) MarkFailed(ctx context.Context, id int64, message string) error {
	return s.setResult(ctx, id, domain.TopicFailed, message)
}

func (s *MongoStore) setResult(ctx context.Context, id int64, status domain.TopicStatus, message string) error {
	result, err := s.topics.UpdateOne(ctx, bson.M{"id": id}, bson.M{"$set": bson.M{"status": status, "last_error": message, "updated_at": time.Now().UTC()}})
	if err == nil && result.MatchedCount == 0 {
		return fmt.Errorf("topic %d not found", id)
	}
	return err
}

func (s *MongoStore) PrepareFetch(ctx context.Context, topicID int64, mode domain.FetchMode) error {
	if mode != domain.FetchReload {
		return nil
	}
	_, err := s.contents.DeleteMany(ctx, bson.M{"topic_id": topicID})
	return err
}

func (s *MongoStore) SaveContents(ctx context.Context, topicID int64, mode domain.FetchMode, contents []domain.PageContent) error {
	now := time.Now().UTC()
	for _, content := range contents {
		if content.UID == "" {
			return fmt.Errorf("page %d floor %d has empty uid", content.PageNo, content.Floor)
		}
		hash := md5.Sum([]byte(content.Text))
		doc := mongoContent{TopicID: topicID, UID: content.UID, PageNo: content.PageNo, Floor: content.Floor, Text: content.Text,
			TextMD5: hex.EncodeToString(hash[:]), CreatedAt: now, UpdatedAt: now}
		if mode == domain.FetchValidate {
			var existing struct {
				TextMD5 string `bson:"text_md5"`
			}
			err := s.contents.FindOne(ctx, bson.M{"topic_id": topicID, "uid": content.UID}, options.FindOne().SetProjection(bson.M{"text_md5": 1})).Decode(&existing)
			switch {
			case errors.Is(err, mongo.ErrNoDocuments):
				if _, err := s.contents.InsertOne(ctx, doc); err != nil && !mongo.IsDuplicateKeyError(err) {
					return err
				}
			case err != nil:
				return err
			case existing.TextMD5 != doc.TextMD5:
				if _, err := s.contents.UpdateOne(ctx, bson.M{"topic_id": topicID, "uid": content.UID}, bson.M{"$set": bson.M{
					"page_no": doc.PageNo, "floor": doc.Floor, "text": doc.Text, "text_md5": doc.TextMD5, "updated_at": now,
				}}); err != nil {
					return err
				}
			}
			continue
		}
		if _, err := s.contents.InsertOne(ctx, doc); err != nil && !mongo.IsDuplicateKeyError(err) {
			return err
		}
	}
	return nil
}

func (s *MongoStore) ListTopicPage(ctx context.Context, status domain.TopicStatus, page, pageSize int) (repository.TopicPage, error) {
	counts := repository.TopicCounts{}
	for value, target := range map[domain.TopicStatus]*int{domain.TopicWaiting: &counts.Waiting, domain.TopicDone: &counts.Done, domain.TopicFailed: &counts.Failed} {
		count, err := s.topics.CountDocuments(ctx, bson.M{"status": value})
		if err != nil {
			return repository.TopicPage{}, err
		}
		*target = int(count)
	}
	total := counts.Waiting
	if status == domain.TopicDone {
		total = counts.Done
	} else if status == domain.TopicFailed {
		total = counts.Failed
	}
	totalPages := max(1, (total+pageSize-1)/pageSize)
	page = min(max(1, page), totalPages)
	cursor, err := s.topics.Find(ctx, bson.M{"status": status}, options.Find().SetSort(bson.D{{Key: "id", Value: 1}}).SetSkip(int64((page-1)*pageSize)).SetLimit(int64(pageSize)))
	if err != nil {
		return repository.TopicPage{}, err
	}
	defer cursor.Close(ctx)
	items := make([]domain.Topic, 0, pageSize)
	for cursor.Next(ctx) {
		var doc mongoTopic
		if err := cursor.Decode(&doc); err != nil {
			return repository.TopicPage{}, err
		}
		items = append(items, topicFromMongo(doc))
	}
	return repository.TopicPage{Items: items, Counts: counts, Page: page, PageSize: pageSize, Total: total, TotalPages: totalPages}, cursor.Err()
}

func (s *MongoStore) PreviewContents(ctx context.Context, topicID int64, limit int) (repository.ContentPreview, error) {
	total, err := s.contents.CountDocuments(ctx, bson.M{"topic_id": topicID})
	if err != nil {
		return repository.ContentPreview{}, err
	}
	cursor, err := s.contents.Find(ctx, bson.M{"topic_id": topicID}, options.Find().SetSort(contentSort()).SetLimit(int64(limit)))
	if err != nil {
		return repository.ContentPreview{}, err
	}
	defer cursor.Close(ctx)
	items := make([]domain.PageContent, 0, min(int(total), limit))
	for cursor.Next(ctx) {
		var doc mongoContent
		if err := cursor.Decode(&doc); err != nil {
			return repository.ContentPreview{}, err
		}
		items = append(items, domain.PageContent{UID: doc.UID, PageNo: doc.PageNo, Floor: doc.Floor, Text: doc.Text})
	}
	return repository.ContentPreview{Contents: items, Total: int(total), Displayed: len(items), Truncated: int(total) > len(items)}, cursor.Err()
}

func (s *MongoStore) FullContent(ctx context.Context, topicID int64) (repository.FullTopicContent, error) {
	var topic mongoTopic
	if err := s.topics.FindOne(ctx, bson.M{"id": topicID}).Decode(&topic); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return repository.FullTopicContent{}, repository.ErrTopicNotFound
		}
		return repository.FullTopicContent{}, err
	}
	cursor, err := s.contents.Find(ctx, bson.M{"topic_id": topicID}, options.Find().SetSort(contentSort()))
	if err != nil {
		return repository.FullTopicContent{}, err
	}
	defer cursor.Close(ctx)
	parts := make([]string, 0)
	for cursor.Next(ctx) {
		var doc mongoContent
		if err := cursor.Decode(&doc); err != nil {
			return repository.FullTopicContent{}, err
		}
		parts = append(parts, doc.Text)
	}
	return repository.FullTopicContent{Title: topic.Title, ContentCount: len(parts), Text: strings.Join(parts, "\n\n")}, cursor.Err()
}

func contentSort() bson.D {
	return bson.D{{Key: "page_no", Value: 1}, {Key: "floor", Value: 1}, {Key: "uid", Value: 1}}
}

func (s *MongoStore) LoadSetting(ctx context.Context, key string) (string, bool, error) {
	var doc struct {
		Value string `bson:"value"`
	}
	err := s.settings.FindOne(ctx, bson.M{"key": key}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", false, nil
	}
	return doc.Value, err == nil, err
}

func (s *MongoStore) SaveSetting(ctx context.Context, key, value string, updatedAt time.Time) error {
	_, err := s.settings.UpdateOne(ctx, bson.M{"key": key}, bson.M{"$set": bson.M{"value": value, "updated_at": updatedAt}}, options.UpdateOne().SetUpsert(true))
	return err
}

func (s *MongoStore) Ping(ctx context.Context) error  { return s.client.Ping(ctx, nil) }
func (s *MongoStore) Close(ctx context.Context) error { return s.client.Disconnect(ctx) }

func topicFromMongo(doc mongoTopic) domain.Topic {
	return domain.Topic{ID: doc.ID, ExternalID: doc.ExternalID, Title: doc.Title, AuthorID: doc.AuthorID, URL: doc.URL,
		Status: doc.Status, LastError: doc.LastError, CreatedAt: doc.CreatedAt, UpdatedAt: doc.UpdatedAt}
}

var _ repository.DataStore = (*MongoStore)(nil)
