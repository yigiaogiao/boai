package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AIConfig is one encrypted provider configuration. APIKeyEnc is opacity to
// the store; it is never exposed by the HTTP layer.
type AIConfig struct {
	Owner     string
	Kind      string
	Provider  string
	BaseURL   string
	Model     string
	APIKeyEnc string
	CreatedAt int64
	UpdatedAt int64
}

// GetAIConfig returns one owner-scoped provider configuration.
func (s *Store) GetAIConfig(ctx context.Context, owner, kind string) (AIConfig, error) {
	var out AIConfig
	err := s.db.QueryRowContext(ctx, `
		SELECT owner, kind, provider, base_url, model, api_key_enc, created_at, updated_at
		FROM ai_configs WHERE owner = ? AND kind = ?`, owner, kind).
		Scan(&out.Owner, &out.Kind, &out.Provider, &out.BaseURL, &out.Model, &out.APIKeyEnc,
			&out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AIConfig{}, ErrNotFound
	}
	return out, err
}

// ListAIConfigs returns both provider configurations without decrypting keys.
func (s *Store) ListAIConfigs(ctx context.Context, owner string) ([]AIConfig, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT owner, kind, provider, base_url, model, api_key_enc, created_at, updated_at
		FROM ai_configs WHERE owner = ? ORDER BY kind`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIConfig{}
	for rows.Next() {
		var item AIConfig
		if err := rows.Scan(&item.Owner, &item.Kind, &item.Provider, &item.BaseURL, &item.Model,
			&item.APIKeyEnc, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// SaveAIConfig inserts or replaces one provider configuration.
func (s *Store) SaveAIConfig(ctx context.Context, owner, kind, provider, baseURL, model, apiKeyEnc string, now int64) error {
	if owner == "" {
		return errors.New("store: empty owner")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ai_configs (owner, kind, provider, base_url, model, api_key_enc, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner, kind) DO UPDATE SET
			provider = excluded.provider,
			base_url = excluded.base_url,
			model = excluded.model,
			api_key_enc = excluded.api_key_enc,
			updated_at = excluded.updated_at`,
		owner, kind, provider, baseURL, model, apiKeyEnc, now, now)
	if err != nil {
		return fmt.Errorf("store: save AI config: %w", err)
	}
	return nil
}

// DeleteAIConfig removes a provider configuration. Generated insights, QA
// history and transcripts are intentionally left untouched.
func (s *Store) DeleteAIConfig(ctx context.Context, owner, kind string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ai_configs WHERE owner = ? AND kind = ?`, owner, kind)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Task is a persisted AI or transcription job.
type Task struct {
	ID         string  `json:"id"`
	Owner      string  `json:"-"`
	EpisodeID  int64   `json:"episode_id"`
	Kind       string  `json:"kind"`
	Status     string  `json:"status"`
	Provider   string  `json:"provider"`
	Error      string  `json:"error"`
	Progress   float64 `json:"progress"`
	Payload    string  `json:"payload"`
	Attempts   int     `json:"attempts"`
	CreatedAt  int64   `json:"created_at"`
	UpdatedAt  int64   `json:"updated_at"`
	StartedAt  int64   `json:"started_at"`
	FinishedAt int64   `json:"finished_at"`
}

// CreateTask inserts a queued task.
func (s *Store) CreateTask(ctx context.Context, task Task) (Task, error) {
	if task.Owner == "" || task.ID == "" || task.EpisodeID <= 0 || task.Kind == "" {
		return Task{}, errors.New("store: invalid task")
	}
	if task.Status == "" {
		task.Status = "queued"
	}
	now := task.CreatedAt
	if now == 0 {
		now = time.Now().Unix()
		task.CreatedAt = now
	}
	task.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks (id, owner, episode_id, kind, status, provider, error, progress, payload,
			attempts, created_at, updated_at, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.Owner, task.EpisodeID, task.Kind, task.Status, task.Provider, task.Error,
		task.Progress, task.Payload, task.Attempts, task.CreatedAt, task.UpdatedAt, task.StartedAt,
		task.FinishedAt)
	if err != nil {
		return Task{}, fmt.Errorf("store: create task: %w", err)
	}
	return task, nil
}

// FindActiveTask returns an identical in-flight task, if one exists. This is
// the guard against duplicate provider charges from repeated clicks.
func (s *Store) FindActiveTask(ctx context.Context, owner string, episodeID int64, kind, payload string) (Task, error) {
	var task Task
	err := s.db.QueryRowContext(ctx, `
		SELECT id, owner, episode_id, kind, status, provider, error, progress, payload, attempts,
			created_at, updated_at, started_at, finished_at
		FROM tasks
		WHERE owner = ? AND episode_id = ? AND kind = ? AND payload = ?
			AND status IN ('queued', 'processing')
		ORDER BY created_at DESC LIMIT 1`, owner, episodeID, kind, payload).
		Scan(&task.ID, &task.Owner, &task.EpisodeID, &task.Kind, &task.Status, &task.Provider,
			&task.Error, &task.Progress, &task.Payload, &task.Attempts, &task.CreatedAt,
			&task.UpdatedAt, &task.StartedAt, &task.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

// GetTask returns one owner-scoped task.
func (s *Store) GetTask(ctx context.Context, owner, id string) (Task, error) {
	var task Task
	err := s.db.QueryRowContext(ctx, `
		SELECT id, owner, episode_id, kind, status, provider, error, progress, payload, attempts,
			created_at, updated_at, started_at, finished_at
		FROM tasks WHERE owner = ? AND id = ?`, owner, id).
		Scan(&task.ID, &task.Owner, &task.EpisodeID, &task.Kind, &task.Status, &task.Provider,
			&task.Error, &task.Progress, &task.Payload, &task.Attempts, &task.CreatedAt,
			&task.UpdatedAt, &task.StartedAt, &task.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

// ListTasks returns an episode's tasks, newest first.
func (s *Store) ListTasks(ctx context.Context, owner string, episodeID int64) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, owner, episode_id, kind, status, provider, error, progress, payload, attempts,
			created_at, updated_at, started_at, finished_at
		FROM tasks WHERE owner = ? AND episode_id = ?
		ORDER BY created_at DESC, id DESC LIMIT 100`, owner, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var task Task
		if err := rows.Scan(&task.ID, &task.Owner, &task.EpisodeID, &task.Kind, &task.Status,
			&task.Provider, &task.Error, &task.Progress, &task.Payload, &task.Attempts,
			&task.CreatedAt, &task.UpdatedAt, &task.StartedAt, &task.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

// StartTask marks a queued task as processing and increments its attempt
// counter. It is a no-op if the task is no longer queued, which prevents a
// stale worker from resurrecting a cancelled or recovered task.
func (s *Store) StartTask(ctx context.Context, owner, id, provider string, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET status = 'processing', provider = ?, error = '', progress = 0,
			attempts = attempts + 1, started_at = ?, updated_at = ?, finished_at = 0
		WHERE owner = ? AND id = ? AND status = 'queued'`,
		provider, now, now, owner, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// FinishTask stores a terminal state.
func (s *Store) FinishTask(ctx context.Context, owner, id, status, message string, now int64) error {
	if status != "completed" && status != "failed" {
		return errors.New("store: invalid terminal task status")
	}
	progress := 0.0
	if status == "completed" {
		progress = 1
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET status = ?, error = ?, progress = ?, updated_at = ?, finished_at = ?
		WHERE owner = ? AND id = ?`, status, truncateRunes(message, 600), progress, now, now, owner, id)
	return err
}

// RetryTask moves a failed task back to queued.
func (s *Store) RetryTask(ctx context.Context, owner, id string, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET status = 'queued', error = '', progress = 0, updated_at = ?, finished_at = 0
		WHERE owner = ? AND id = ? AND status = 'failed'`, now, owner, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RecoverInterruptedTasks fails tasks that were in flight when the process
// stopped. They remain visible and retryable instead of hanging forever.
func (s *Store) RecoverInterruptedTasks(ctx context.Context, now int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET status = 'failed', error = ?, progress = 0, updated_at = ?, finished_at = ?
		WHERE status IN ('queued', 'processing')`,
		"服务重启，任务已中断，可重试", now, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Insight is one generated guide, deep read or mind map.
type Insight struct {
	EpisodeID int64  `json:"episode_id"`
	Kind      string `json:"kind"`
	Payload   string `json:"payload"`
	Model     string `json:"model"`
	Source    string `json:"source"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// SaveInsight inserts or replaces one insight for an episode and kind.
func (s *Store) SaveInsight(ctx context.Context, owner string, insight Insight, now int64) (Insight, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM episodes WHERE id = ? AND owner = ?`,
		insight.EpisodeID, owner).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Insight{}, ErrNotFound
		}
		return Insight{}, err
	}
	if insight.Source == "" {
		insight.Source = "ai"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO insights (owner, episode_id, kind, payload, model, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(episode_id, kind) DO UPDATE SET
			payload = excluded.payload, model = excluded.model, source = excluded.source,
			updated_at = excluded.updated_at`,
		owner, insight.EpisodeID, insight.Kind, insight.Payload, insight.Model, insight.Source, now, now)
	if err != nil {
		return Insight{}, err
	}
	insight.CreatedAt = now
	insight.UpdatedAt = now
	return insight, nil
}

// ListInsights returns generated content for one owned episode.
func (s *Store) ListInsights(ctx context.Context, owner string, episodeID int64) ([]Insight, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT episode_id, kind, payload, model, source, created_at, updated_at
		FROM insights WHERE owner = ? AND episode_id = ? ORDER BY kind`, owner, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Insight{}
	for rows.Next() {
		var item Insight
		if err := rows.Scan(&item.EpisodeID, &item.Kind, &item.Payload, &item.Model, &item.Source,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// QA is one grounded question and answer.
type QA struct {
	ID        int64  `json:"id"`
	EpisodeID int64  `json:"episode_id"`
	Question  string `json:"question"`
	Answer    string `json:"answer"`
	Citations string `json:"citations"`
	Grounded  bool   `json:"grounded"`
	Model     string `json:"model"`
	CreatedAt int64  `json:"created_at"`
}

// SaveQA appends a question and answer.
func (s *Store) SaveQA(ctx context.Context, owner string, qa QA, now int64) (QA, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM episodes WHERE id = ? AND owner = ?`,
		qa.EpisodeID, owner).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return QA{}, ErrNotFound
		}
		return QA{}, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO qa (owner, episode_id, question, answer, citations, grounded, model, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		owner, qa.EpisodeID, truncateRunes(qa.Question, 1000), qa.Answer, qa.Citations,
		boolInt(qa.Grounded), qa.Model, now)
	if err != nil {
		return QA{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return QA{}, err
	}
	qa.ID = id
	qa.CreatedAt = now
	return qa, nil
}

// ListQA returns an episode's question history, newest first.
func (s *Store) ListQA(ctx context.Context, owner string, episodeID int64) ([]QA, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, episode_id, question, answer, citations, grounded, model, created_at
		FROM qa WHERE owner = ? AND episode_id = ? ORDER BY created_at DESC, id DESC LIMIT 100`,
		owner, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QA{}
	for rows.Next() {
		var item QA
		var grounded int
		if err := rows.Scan(&item.ID, &item.EpisodeID, &item.Question, &item.Answer, &item.Citations,
			&grounded, &item.Model, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.Grounded = grounded != 0
		out = append(out, item)
	}
	return out, rows.Err()
}

// SaveTranscriptTranslations updates per-line translations for an existing
// transcript without re-uploading audio or discarding the original text.
func (s *Store) SaveTranscriptTranslations(ctx context.Context, owner string, episodeID int64, targetLanguage string, translations map[int]string) error {
	if len(translations) == 0 {
		return errors.New("store: no translations")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var transcriptID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM transcripts WHERE owner = ? AND episode_id = ?`,
		owner, episodeID).Scan(&transcriptID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	for index, text := range translations {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE transcript_segments SET translation = ?
			WHERE transcript_id = ? AND owner = ? AND idx = ?`,
			truncateRunes(trimmed, 8000), transcriptID, owner, index); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE transcripts SET translated_language = ? WHERE id = ? AND owner = ?`,
		truncateRunes(targetLanguage, 32), transcriptID, owner); err != nil {
		return err
	}
	return tx.Commit()
}
