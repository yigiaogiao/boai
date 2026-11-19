package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Progress is the persisted playback position.
type Progress struct {
	PositionSeconds float64 `json:"position_seconds"`
	DurationSeconds float64 `json:"duration_seconds"`
	Completed       bool    `json:"completed"`
	UpdatedAt       int64   `json:"updated_at"`
}

// SaveProgress persists a playback position and transitions the inbox state.
// A position within 30 seconds of the end (or the last 2%) counts as
// completed, so a finished episode leaves "continue listening".
func (s *Store) SaveProgress(ctx context.Context, owner string, episodeID int64, position, duration float64) (Progress, error) {
	if math.IsNaN(position) || math.IsInf(position, 0) || position < 0 {
		position = 0
	}
	if math.IsNaN(duration) || math.IsInf(duration, 0) || duration < 0 {
		duration = 0
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM episodes WHERE id = ? AND owner = ?`, episodeID, owner).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Progress{}, ErrNotFound
		}
		return Progress{}, err
	}
	if duration == 0 {
		_ = s.db.QueryRowContext(ctx, `
			SELECT duration_seconds FROM progress WHERE owner = ? AND episode_id = ?`, owner, episodeID).Scan(&duration)
	}
	if duration > 0 && position > duration {
		position = duration
	}
	completed := duration > 0 && position > 0 && (duration-position <= 30 || position/duration >= 0.98)
	now := time.Now().Unix()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO progress (owner, episode_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (owner, episode_id) DO UPDATE SET
			position_seconds = excluded.position_seconds,
			duration_seconds = excluded.duration_seconds,
			completed = excluded.completed,
			updated_at = excluded.updated_at`,
		owner, episodeID, position, duration, boolInt(completed), now); err != nil {
		return Progress{}, err
	}
	state := "started"
	if completed {
		state = "completed"
	}
	// Playback must not resurrect an episode the user deliberately moved out of
	// the inbox. Its progress still updates; only the inbox state is sticky.
	if position <= 0 {
		if _, err := s.db.ExecContext(ctx, `
			UPDATE episodes SET inbox_state = 'new'
			WHERE id = ? AND owner = ? AND inbox_state <> 'archived'`,
			episodeID, owner); err != nil {
			return Progress{}, err
		}
	} else if _, err := s.db.ExecContext(ctx, `
		UPDATE episodes SET inbox_state = ?
		WHERE id = ? AND owner = ? AND inbox_state <> 'archived'`, state, episodeID, owner); err != nil {
		return Progress{}, err
	}
	return Progress{PositionSeconds: position, DurationSeconds: duration, Completed: completed, UpdatedAt: now}, nil
}

// Inbox actions accepted by SetEpisodeInboxState.
const (
	InboxRead      = "read"
	InboxUnread    = "unread"
	InboxArchive   = "archive"
	InboxUnarchive = "unarchive"
)

// MaxBulkEpisodes bounds one inbox action request.
const MaxBulkEpisodes = 500

// SetEpisodeInboxState applies a manual inbox action to one or more owned
// episodes. Marking read also completes playback progress without discarding
// the saved position; archiving leaves progress untouched. Archived episodes
// stay archived through playback and can be restored explicitly.
func (s *Store) SetEpisodeInboxState(ctx context.Context, owner string, episodeIDs []int64, action string) (int, error) {
	ids, err := ownedIDs(episodeIDs, MaxBulkEpisodes)
	if err != nil {
		return 0, err
	}
	switch action {
	case InboxRead, InboxUnread, InboxArchive, InboxUnarchive:
	default:
		return 0, errors.New("store: unknown inbox action")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var count int64
	switch action {
	case InboxRead:
		readArgs := append([]any{now, owner}, args...)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO progress (owner, episode_id, position_seconds, duration_seconds, completed, updated_at)
			SELECT e.owner, e.id, 0, e.duration_seconds, 1, ?
			FROM episodes e WHERE e.owner = ? AND e.id IN (`+placeholders+`)
			ON CONFLICT (owner, episode_id) DO UPDATE SET
				completed = 1, updated_at = excluded.updated_at`, readArgs...); err != nil {
			return 0, err
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE episodes SET
				inbox_state = CASE WHEN inbox_state = 'archived' THEN 'archived' ELSE 'completed' END,
				updated_at = ?
			WHERE owner = ? AND id IN (`+placeholders+`)`, readArgs...)
		if err != nil {
			return 0, err
		}
		count, _ = res.RowsAffected()
	case InboxUnread:
		unreadArgs := append([]any{now, owner}, args...)
		if _, err := tx.ExecContext(ctx, `
			UPDATE progress SET completed = 0, updated_at = ?
			WHERE owner = ? AND episode_id IN (`+placeholders+`)`, unreadArgs...); err != nil {
			return 0, err
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE episodes SET
				inbox_state = CASE
					WHEN inbox_state = 'archived' THEN 'archived'
					WHEN EXISTS (
						SELECT 1 FROM progress p
						WHERE p.owner = episodes.owner AND p.episode_id = episodes.id
							AND p.position_seconds > 0
					) THEN 'started'
					ELSE 'new'
				END,
				updated_at = ?
			WHERE owner = ? AND id IN (`+placeholders+`)`, unreadArgs...)
		if err != nil {
			return 0, err
		}
		count, _ = res.RowsAffected()
	case InboxArchive:
		res, err := tx.ExecContext(ctx, `
			UPDATE episodes SET inbox_state = 'archived', updated_at = ?
			WHERE owner = ? AND id IN (`+placeholders+`)`, append([]any{now, owner}, args...)...)
		if err != nil {
			return 0, err
		}
		count, _ = res.RowsAffected()
	case InboxUnarchive:
		res, err := tx.ExecContext(ctx, `
			UPDATE episodes SET
				inbox_state = CASE
					WHEN EXISTS (
						SELECT 1 FROM progress p
						WHERE p.owner = episodes.owner AND p.episode_id = episodes.id AND p.completed = 1
					) THEN 'completed'
					WHEN EXISTS (
						SELECT 1 FROM progress p
						WHERE p.owner = episodes.owner AND p.episode_id = episodes.id
							AND p.position_seconds > 0
					) THEN 'started'
					ELSE 'new'
				END,
				updated_at = ?
			WHERE owner = ? AND id IN (`+placeholders+`)`, append([]any{now, owner}, args...)...)
		if err != nil {
			return 0, err
		}
		count, _ = res.RowsAffected()
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(count), nil
}

// ListCategories returns categories with their subscribed show counts.
func (s *Store) ListCategories(ctx context.Context, owner string) ([]Category, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.color,
			(SELECT COUNT(*) FROM feed_categories fc WHERE fc.category_id = c.id AND fc.owner = c.owner)
		FROM categories c WHERE c.owner = ? ORDER BY c.name COLLATE NOCASE`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Color, &c.FeedCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateCategory inserts a category. Names are compared case-insensitively.
func (s *Store) CreateCategory(ctx context.Context, owner, name, color string) (Category, error) {
	name, err := cleanCategoryName(name)
	if err != nil {
		return Category{}, err
	}
	color = cleanColor(color)
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO categories (owner, name, color, created_at) VALUES (?, ?, ?, ?)`,
		owner, name, color, now)
	if err != nil {
		return Category{}, mapConstraint(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Category{}, err
	}
	return Category{ID: id, Name: name, Color: color}, nil
}

// RenameCategory changes a category's name without touching subscriptions.
func (s *Store) RenameCategory(ctx context.Context, owner string, categoryID int64, name string) (Category, error) {
	name, err := cleanCategoryName(name)
	if err != nil {
		return Category{}, err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE categories SET name = ? WHERE id = ? AND owner = ?`, name, categoryID, owner)
	if err != nil {
		return Category{}, mapConstraint(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Category{}, ErrNotFound
	}
	var c Category
	if err := s.db.QueryRowContext(ctx, `SELECT id, name, color FROM categories WHERE id = ? AND owner = ?`,
		categoryID, owner).Scan(&c.ID, &c.Name, &c.Color); err != nil {
		return Category{}, err
	}
	return c, nil
}

// DeleteCategory removes a category. Subscriptions remain.
func (s *Store) DeleteCategory(ctx context.Context, owner string, categoryID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM categories WHERE id = ? AND owner = ?`, categoryID, owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetFeedCategories replaces all category assignments for a show.
func (s *Store) SetFeedCategories(ctx context.Context, owner string, feedID int64, categoryIDs []int64) error {
	_, err := s.SetFeedsCategories(ctx, owner, []int64{feedID}, categoryIDs)
	return err
}

// SetFeedsCategories replaces category assignments for several shows in one
// transaction. It is used by the subscription multi-select bar.
func (s *Store) SetFeedsCategories(ctx context.Context, owner string, feedIDs, categoryIDs []int64) (int, error) {
	if len(feedIDs) == 0 {
		return 0, nil
	}
	if len(feedIDs) > 200 {
		return 0, errors.New("store: too many feeds")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	cleanCategories := make([]int64, 0, len(categoryIDs))
	seenCategories := make(map[int64]bool, len(categoryIDs))
	for _, categoryID := range categoryIDs {
		if categoryID <= 0 || seenCategories[categoryID] {
			continue
		}
		seenCategories[categoryID] = true
		var owned int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM categories WHERE id = ? AND owner = ?`,
			categoryID, owner).Scan(&owned); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, fmt.Errorf("%w: category", ErrNotFound)
			}
			return 0, err
		}
		cleanCategories = append(cleanCategories, categoryID)
	}
	seenFeeds := make(map[int64]bool, len(feedIDs))
	updated := 0
	for _, feedID := range feedIDs {
		if feedID <= 0 || seenFeeds[feedID] {
			continue
		}
		seenFeeds[feedID] = true
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM feeds WHERE id = ? AND owner = ?`,
			feedID, owner).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, ErrNotFound
			}
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM feed_categories WHERE owner = ? AND feed_id = ?`,
			owner, feedID); err != nil {
			return 0, err
		}
		for _, categoryID := range cleanCategories {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO feed_categories (owner, feed_id, category_id) VALUES (?, ?, ?)`,
				owner, feedID, categoryID); err != nil {
				return 0, err
			}
		}
		updated++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return updated, nil
}

func cleanCategoryName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("store: category name is empty")
	}
	if len([]rune(name)) > 20 {
		return "", errors.New("store: category name is too long")
	}
	return name, nil
}

func cleanColor(color string) string {
	color = strings.TrimSpace(color)
	if len(color) != 7 || color[0] != '#' {
		return ""
	}
	for _, r := range color[1:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return ""
		}
	}
	return color
}

func mapConstraint(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
		return ErrConflict
	}
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
