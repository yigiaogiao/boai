// Package store persists subscriptions, episodes, playback progress,
// categories, downloads and transcripts in SQLite.
//
// Every method takes an owner and scopes its queries to that owner. The
// storage layer never reads process configuration or LazyCat paths; callers
// hand it a database path so the core stays reusable outside the microserver.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaFS embed.FS

// ErrNotFound reports a missing or foreign record. The two are deliberately
// indistinguishable so one user cannot probe another user's IDs.
var ErrNotFound = errors.New("store: not found")

// ErrConflict reports a uniqueness violation (category names).
var ErrConflict = errors.New("store: conflict")

// Store is a handle to the SQLite database.
type Store struct {
	db *sql.DB
}

// Open creates missing parent directories, opens the database and applies the
// schema. Reopening an existing database is safe.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("store: empty database path")
	}
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("store: create data directory: %w", err)
			}
		}
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("store: read schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, string(schema)); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}
	// Existing databases predate the hidden flag used by show previews.
	// CREATE TABLE IF NOT EXISTS cannot add it, so migrate in place.
	if err := ensureColumn(ctx, db, "feeds", "hidden", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate feeds.hidden: %w", err)
	}
	return &Store{db: db}, nil
}

func ensureColumn(ctx context.Context, db *sql.DB, table, column, definition string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, kind string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition)
	return err
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for health checks and tests.
func (s *Store) DB() *sql.DB { return s.db }

// Category is a user-created grouping. A show may belong to several.
type Category struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	FeedCount int    `json:"feed_count"`
}

// Feed is one subscription.
type Feed struct {
	ID                  int64      `json:"id"`
	URL                 string     `json:"url"`
	SiteURL             string     `json:"site_url"`
	Title               string     `json:"title"`
	Description         string     `json:"description"`
	ImageURL            string     `json:"image_url"`
	LastCheckedAt       int64      `json:"last_checked_at"`
	LastSuccessAt       int64      `json:"last_success_at"`
	LastError           string     `json:"last_error"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	EpisodeCount        int        `json:"episode_count"`
	NewCount            int        `json:"new_count"`
	Categories          []Category `json:"categories"`
}

// Episode is one playable item joined with its show and playback progress.
type Episode struct {
	ID                      int64      `json:"id"`
	FeedID                  int64      `json:"feed_id"`
	FeedTitle               string     `json:"feed_title"`
	FeedImageURL            string     `json:"feed_image_url"`
	FeedSiteURL             string     `json:"feed_site_url"`
	FeedURL                 string     `json:"feed_url"`
	GUID                    string     `json:"guid"`
	Title                   string     `json:"title"`
	Description             string     `json:"description,omitempty"`
	Link                    string     `json:"link"`
	AudioURL                string     `json:"-"`
	AudioType               string     `json:"audio_type"`
	ImageURL                string     `json:"image_url"`
	DurationSeconds         int        `json:"duration_seconds"`
	PublishedAt             int64      `json:"published_at"`
	InboxState              string     `json:"inbox_state"`
	HasTranscript           bool       `json:"has_transcript"`
	TranscriptRefsAvailable bool       `json:"transcript_refs_available"`
	ProgressSeconds         float64    `json:"progress_seconds"`
	ProgressDuration        float64    `json:"progress_duration"`
	Completed               bool       `json:"completed"`
	ProgressUpdatedAt       int64      `json:"progress_updated_at"`
	Categories              []Category `json:"categories"`
	Chapters                []Chapter  `json:"chapters"`
}

// Download is a locally saved episode joined with its show. FileName is kept
// server-side so the HTTP layer never exposes a filesystem path.
type Download struct {
	EpisodeID       int64  `json:"episode_id"`
	FeedID          int64  `json:"feed_id"`
	Title           string `json:"title"`
	Link            string `json:"link"`
	FeedTitle       string `json:"feed_title"`
	FeedSiteURL     string `json:"feed_site_url"`
	ImageURL        string `json:"image_url"`
	FeedImageURL    string `json:"feed_image_url"`
	DurationSeconds int    `json:"duration_seconds"`
	PublishedAt     int64  `json:"published_at"`
	FileName        string `json:"-"`
	ContentType     string `json:"content_type"`
	SizeBytes       int64  `json:"size_bytes"`
	CreatedAt       int64  `json:"created_at"`
}

// Chapter is a timestamp published in the episode notes.
type Chapter struct {
	Start int    `json:"start"`
	Label string `json:"label"`
}

// Segment is one timed transcript line.
type Segment struct {
	Index       int     `json:"index"`
	Start       float64 `json:"start"`
	End         float64 `json:"end"`
	Speaker     string  `json:"speaker"`
	Text        string  `json:"text"`
	Translation string  `json:"translation"`
}

// Transcript is the stored transcript for an episode.
type Transcript struct {
	Source             string    `json:"source"`
	Language           string    `json:"language"`
	TranslatedLanguage string    `json:"translated_language"`
	Timed              bool      `json:"timed"`
	Segments           []Segment `json:"segments"`
}

// SearchResult is one library hit, either a show, an episode or a transcript
// line that points back into an episode.
type SearchResult struct {
	Kind        string  `json:"kind"` // feed, episode, transcript
	FeedID      int64   `json:"feed_id,omitempty"`
	EpisodeID   int64   `json:"episode_id,omitempty"`
	Title       string  `json:"title"`
	FeedTitle   string  `json:"feed_title,omitempty"`
	ImageURL    string  `json:"image_url,omitempty"`
	Snippet     string  `json:"snippet,omitempty"`
	TimeSeconds float64 `json:"time_seconds,omitempty"`
}

// FeedInput is normalized feed data handed to UpsertFeed.
type FeedInput struct {
	URL          string
	SiteURL      string
	Title        string
	Description  string
	ImageURL     string
	ETag         string
	LastModified string
	Episodes     []EpisodeInput
}

// EpisodeInput is one normalized episode handed to UpsertFeed.
type EpisodeInput struct {
	GUID            string
	Title           string
	Description     string
	Link            string
	AudioURL        string
	AudioType       string
	ImageURL        string
	DurationSeconds int
	PublishedAt     int64
	TranscriptRefs  string
	Chapters        string
}

// UpsertFeed inserts or updates a subscription and its episodes. Existing
// episodes keep their inbox state, progress and transcripts. The return value
// counts episodes seen for the first time.
func (s *Store) UpsertFeed(ctx context.Context, owner string, in FeedInput) (int64, int, error) {
	if owner == "" {
		return 0, 0, errors.New("store: empty owner")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	var feedID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM feeds WHERE owner = ? AND url = ?`, owner, in.URL).Scan(&feedID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, execErr := tx.ExecContext(ctx, `
			INSERT INTO feeds (owner, url, site_url, title, description, image_url, etag, last_modified,
				last_checked_at, last_success_at, next_check_at, consecutive_failures, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, '', ?, ?)`,
			owner, in.URL, in.SiteURL, in.Title, in.Description, in.ImageURL, in.ETag, in.LastModified,
			now, now, now, now, now)
		if execErr != nil {
			return 0, 0, fmt.Errorf("store: insert feed: %w", execErr)
		}
		feedID, err = res.LastInsertId()
		if err != nil {
			return 0, 0, err
		}
	case err != nil:
		return 0, 0, err
	default:
		if _, err := tx.ExecContext(ctx, `
			UPDATE feeds SET
				site_url = CASE WHEN ? <> '' THEN ? ELSE site_url END,
				title = CASE WHEN ? <> '' THEN ? ELSE title END,
				description = CASE WHEN ? <> '' THEN ? ELSE description END,
				image_url = CASE WHEN ? <> '' THEN ? ELSE image_url END,
				etag = CASE WHEN ? <> '' THEN ? ELSE etag END,
				last_modified = CASE WHEN ? <> '' THEN ? ELSE last_modified END,
				updated_at = ?
			WHERE id = ? AND owner = ?`,
			in.SiteURL, in.SiteURL, in.Title, in.Title, in.Description, in.Description,
			in.ImageURL, in.ImageURL, in.ETag, in.ETag, in.LastModified, in.LastModified, now, feedID, owner); err != nil {
			return 0, 0, fmt.Errorf("store: update feed: %w", err)
		}
	}

	newCount := 0
	for _, ep := range in.Episodes {
		if strings.TrimSpace(ep.GUID) == "" || strings.TrimSpace(ep.AudioURL) == "" {
			continue
		}
		refs := strings.TrimSpace(ep.TranscriptRefs)
		if refs == "" || refs == "null" {
			refs = "[]"
		}
		chapters := strings.TrimSpace(ep.Chapters)
		if chapters == "" || chapters == "null" {
			chapters = "[]"
		}
		published := ep.PublishedAt
		if published < 0 {
			published = 0
		}
		res, execErr := tx.ExecContext(ctx, `
			INSERT INTO episodes (owner, feed_id, guid, title, description, link, audio_url, audio_type,
				image_url, duration_seconds, published_at, inbox_state, has_transcript, transcript_refs, chapters, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'new', 0, ?, ?, ?, ?)
			ON CONFLICT (feed_id, guid) DO NOTHING`,
			owner, feedID, ep.GUID, ep.Title, ep.Description, ep.Link, ep.AudioURL, ep.AudioType,
			ep.ImageURL, ep.DurationSeconds, published, refs, chapters, now, now)
		if execErr != nil {
			return 0, 0, fmt.Errorf("store: insert episode: %w", execErr)
		}
		if rows, rowsErr := res.RowsAffected(); rowsErr == nil && rows == 1 {
			newCount++
		}
		if _, execErr := tx.ExecContext(ctx, `
			UPDATE episodes SET title = ?, description = ?, link = ?, audio_url = ?, audio_type = ?,
				image_url = ?, duration_seconds = ?, published_at = ?, transcript_refs = ?, chapters = ?, updated_at = ?
			WHERE feed_id = ? AND guid = ?`,
			ep.Title, ep.Description, ep.Link, ep.AudioURL, ep.AudioType, ep.ImageURL,
			ep.DurationSeconds, published, refs, chapters, now, feedID, ep.GUID); execErr != nil {
			return 0, 0, fmt.Errorf("store: update episode: %w", execErr)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return feedID, newCount, nil
}

// RecordFeedResult updates refresh bookkeeping after a fetch attempt.
func (s *Store) RecordFeedResult(ctx context.Context, owner string, feedID int64, success bool, etag, lastModified string, next time.Time, message string) error {
	now := time.Now().Unix()
	nextUnix := next.Unix()
	var err error
	if success {
		_, err = s.db.ExecContext(ctx, `
			UPDATE feeds SET last_checked_at = ?, last_success_at = ?, next_check_at = ?,
				consecutive_failures = 0, last_error = '',
				etag = CASE WHEN ? <> '' THEN ? ELSE etag END,
				last_modified = CASE WHEN ? <> '' THEN ? ELSE last_modified END,
				updated_at = ?
			WHERE id = ? AND owner = ?`,
			now, now, nextUnix, etag, etag, lastModified, lastModified, now, feedID, owner)
	} else {
		_, err = s.db.ExecContext(ctx, `
			UPDATE feeds SET last_checked_at = ?, next_check_at = ?,
				consecutive_failures = consecutive_failures + 1,
				last_error = ?, updated_at = ?
			WHERE id = ? AND owner = ?`,
			now, nextUnix, truncateRunes(message, 500), now, feedID, owner)
	}
	return err
}

// DueFeed is the minimum state the refresher needs.
type DueFeed struct {
	ID                  int64
	Owner               string
	URL                 string
	ETag                string
	LastModified        string
	ConsecutiveFailures int
}

// DueFeeds returns subscriptions whose next check time has passed.
func (s *Store) DueFeeds(ctx context.Context, now time.Time, limit int) ([]DueFeed, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, owner, url, etag, last_modified, consecutive_failures FROM feeds
		WHERE hidden = 0 AND next_check_at <= ? ORDER BY next_check_at LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DueFeed
	for rows.Next() {
		var f DueFeed
		if err := rows.Scan(&f.ID, &f.Owner, &f.URL, &f.ETag, &f.LastModified, &f.ConsecutiveFailures); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FeedFetchState returns the conditional-request state for one owned feed.
func (s *Store) FeedFetchState(ctx context.Context, owner string, feedID int64) (DueFeed, error) {
	var f DueFeed
	err := s.db.QueryRowContext(ctx, `
		SELECT id, owner, url, etag, last_modified, consecutive_failures FROM feeds WHERE id = ? AND owner = ?`,
		feedID, owner).Scan(&f.ID, &f.Owner, &f.URL, &f.ETag, &f.LastModified, &f.ConsecutiveFailures)
	if errors.Is(err, sql.ErrNoRows) {
		return DueFeed{}, ErrNotFound
	}
	return f, err
}

// FeedVisibilityByURL reports whether the owner has this feed and whether it
// is a hidden preview rather than a real subscription.
func (s *Store) FeedVisibilityByURL(ctx context.Context, owner, rawURL string) (exists, hidden bool, err error) {
	var value int
	err = s.db.QueryRowContext(ctx,
		`SELECT hidden FROM feeds WHERE owner = ? AND url = ?`, owner, rawURL).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, value != 0, nil
}

// SetFeedHidden marks a feed as a preview (hidden from the library) or turns a
// preview into a real subscription. Episodes and progress are preserved.
func (s *Store) SetFeedHidden(ctx context.Context, owner string, feedID int64, hidden bool) error {
	value := 0
	if hidden {
		value = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE feeds SET hidden = ?, updated_at = ? WHERE id = ? AND owner = ?`,
		value, time.Now().Unix(), feedID, owner)
	if err != nil {
		return err
	}
	if changed, err := res.RowsAffected(); err == nil && changed == 0 {
		return ErrNotFound
	}
	return nil
}

// GetFeed returns one subscription with categories and counts.
func (s *Store) GetFeed(ctx context.Context, owner string, feedID int64) (Feed, error) {
	var f Feed
	err := s.db.QueryRowContext(ctx, `
		SELECT id, url, site_url, title, description, image_url, last_checked_at, last_success_at,
			last_error, consecutive_failures,
			(SELECT COUNT(*) FROM episodes e WHERE e.feed_id = f.id AND e.owner = f.owner),
			(SELECT COUNT(*) FROM episodes e WHERE e.feed_id = f.id AND e.owner = f.owner AND e.inbox_state = 'new')
		FROM feeds f WHERE f.id = ? AND f.owner = ?`, feedID, owner).
		Scan(&f.ID, &f.URL, &f.SiteURL, &f.Title, &f.Description, &f.ImageURL, &f.LastCheckedAt,
			&f.LastSuccessAt, &f.LastError, &f.ConsecutiveFailures, &f.EpisodeCount, &f.NewCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Feed{}, ErrNotFound
	}
	if err != nil {
		return Feed{}, err
	}
	f.Categories = []Category{}
	categories, err := s.feedCategories(ctx, owner)
	if err != nil {
		return Feed{}, err
	}
	if list, ok := categories[f.ID]; ok {
		f.Categories = list
	}
	return f, nil
}

// ListFeeds returns all subscriptions ordered by title.
func (s *Store) ListFeeds(ctx context.Context, owner string) ([]Feed, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, f.url, f.site_url, f.title, f.description, f.image_url, f.last_checked_at,
			f.last_success_at, f.last_error, f.consecutive_failures,
			(SELECT COUNT(*) FROM episodes e WHERE e.feed_id = f.id AND e.owner = f.owner),
			(SELECT COUNT(*) FROM episodes e WHERE e.feed_id = f.id AND e.owner = f.owner AND e.inbox_state = 'new')
		FROM feeds f WHERE f.owner = ? AND f.hidden = 0 ORDER BY f.title COLLATE NOCASE`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	feeds := []Feed{}
	for rows.Next() {
		var f Feed
		if err := rows.Scan(&f.ID, &f.URL, &f.SiteURL, &f.Title, &f.Description, &f.ImageURL,
			&f.LastCheckedAt, &f.LastSuccessAt, &f.LastError, &f.ConsecutiveFailures,
			&f.EpisodeCount, &f.NewCount); err != nil {
			return nil, err
		}
		f.Categories = []Category{}
		feeds = append(feeds, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	categories, err := s.feedCategories(ctx, owner)
	if err != nil {
		return nil, err
	}
	for i := range feeds {
		if list, ok := categories[feeds[i].ID]; ok {
			feeds[i].Categories = list
		}
	}
	return feeds, nil
}

// DeleteFeeds unsubscribes one or more shows. Episodes, playback progress,
// downloads, transcripts and AI results for those shows are removed by the
// schema's cascading foreign keys. Categories themselves are kept.
func (s *Store) DeleteFeeds(ctx context.Context, owner string, feedIDs []int64) (int, error) {
	ids, err := ownedIDs(feedIDs, 200)
	if err != nil {
		return 0, err
	}
	var total int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		args := []any{owner}
		for _, id := range ids {
			args = append(args, id)
		}
		res, execErr := tx.ExecContext(ctx,
			`DELETE FROM feeds WHERE owner = ? AND id IN (`+placeholders+`)`, args...)
		if execErr != nil {
			return execErr
		}
		total, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int(total), nil
}

func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// feedCategories returns categories keyed by feed ID for one owner.
func (s *Store) feedCategories(ctx context.Context, owner string) (map[int64][]Category, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT fc.feed_id, c.id, c.name, c.color
		FROM feed_categories fc JOIN categories c ON c.id = fc.category_id
		WHERE fc.owner = ? ORDER BY c.name COLLATE NOCASE`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]Category)
	for rows.Next() {
		var feedID int64
		var c Category
		if err := rows.Scan(&feedID, &c.ID, &c.Name, &c.Color); err != nil {
			return nil, err
		}
		out[feedID] = append(out[feedID], c)
	}
	return out, rows.Err()
}

// EpisodeFilter narrows a library listing.
type EpisodeFilter struct {
	Kind       string // all, new, in_progress, category
	CategoryID int64
	FeedID     int64
	Limit      int
}

// ListEpisodes returns the latest episodes for the inbox.
func (s *Store) ListEpisodes(ctx context.Context, owner string, filter EpisodeFilter) ([]Episode, error) {
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 300
	}
	query := `
		SELECT e.id, e.feed_id, f.title, f.image_url, f.site_url, f.url, e.guid, e.title, e.link,
			e.audio_url, e.audio_type, e.image_url, e.duration_seconds, e.published_at,
			e.inbox_state, e.has_transcript,
			CASE WHEN e.transcript_refs <> '' AND e.transcript_refs <> '[]' THEN 1 ELSE 0 END,
			COALESCE(p.position_seconds, 0), COALESCE(p.duration_seconds, 0),
			COALESCE(p.completed, 0), COALESCE(p.updated_at, 0)
		FROM episodes e JOIN feeds f ON f.id = e.feed_id
		LEFT JOIN progress p ON p.owner = e.owner AND p.episode_id = e.id
		WHERE e.owner = ?`
	args := []any{owner}
	switch filter.Kind {
	case "new":
		query += ` AND e.inbox_state = 'new'`
	case "in_progress":
		query += ` AND p.position_seconds > 0 AND p.completed = 0`
	case "category":
		query += ` AND EXISTS (SELECT 1 FROM feed_categories fc WHERE fc.feed_id = e.feed_id AND fc.category_id = ? AND fc.owner = e.owner)`
		args = append(args, filter.CategoryID)
	}
	if filter.FeedID > 0 {
		// A show page lists everything, including episodes the user moved out
		// of the inbox, so they can be found and restored.
		query += ` AND e.feed_id = ?`
		args = append(args, filter.FeedID)
	} else {
		query += ` AND f.hidden = 0 AND e.inbox_state <> 'archived'`
	}
	query += ` ORDER BY e.published_at DESC, e.id DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	episodes := []Episode{}
	for rows.Next() {
		ep, err := scanEpisode(rows)
		if err != nil {
			return nil, err
		}
		episodes = append(episodes, ep)
	}
	return episodes, rows.Err()
}

// GetEpisode returns one episode with its description, show and categories.
func (s *Store) GetEpisode(ctx context.Context, owner string, episodeID int64) (Episode, error) {
	var ep Episode
	var description, chapters string
	err := s.db.QueryRowContext(ctx, `
		SELECT e.id, e.feed_id, f.title, f.image_url, f.site_url, f.url, e.guid, e.title, e.description,
			e.link, e.audio_url, e.audio_type, e.image_url, e.duration_seconds, e.published_at,
			e.inbox_state, e.has_transcript,
			CASE WHEN e.transcript_refs <> '' AND e.transcript_refs <> '[]' THEN 1 ELSE 0 END,
			e.chapters,
			COALESCE(p.position_seconds, 0), COALESCE(p.duration_seconds, 0),
			COALESCE(p.completed, 0), COALESCE(p.updated_at, 0)
		FROM episodes e JOIN feeds f ON f.id = e.feed_id
		LEFT JOIN progress p ON p.owner = e.owner AND p.episode_id = e.id
		WHERE e.id = ? AND e.owner = ?`, episodeID, owner).
		Scan(&ep.ID, &ep.FeedID, &ep.FeedTitle, &ep.FeedImageURL, &ep.FeedSiteURL, &ep.FeedURL, &ep.GUID, &ep.Title, &description,
			&ep.Link, &ep.AudioURL, &ep.AudioType, &ep.ImageURL, &ep.DurationSeconds, &ep.PublishedAt,
			&ep.InboxState, &ep.HasTranscript, &ep.TranscriptRefsAvailable, &chapters,
			&ep.ProgressSeconds, &ep.ProgressDuration, &ep.Completed, &ep.ProgressUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Episode{}, ErrNotFound
	}
	if err != nil {
		return Episode{}, err
	}
	ep.Description = description
	ep.Chapters = []Chapter{}
	if chapters != "" {
		_ = json.Unmarshal([]byte(chapters), &ep.Chapters)
	}
	ep.Categories = []Category{}
	categories, err := s.feedCategories(ctx, owner)
	if err != nil {
		return Episode{}, err
	}
	if list, ok := categories[ep.FeedID]; ok {
		ep.Categories = list
	}
	return ep, nil
}

// EpisodeAudio returns the remote audio URL for one owned episode.
func (s *Store) EpisodeAudio(ctx context.Context, owner string, episodeID int64) (string, string, error) {
	var audioURL, audioType string
	err := s.db.QueryRowContext(ctx,
		`SELECT audio_url, audio_type FROM episodes WHERE id = ? AND owner = ?`, episodeID, owner).
		Scan(&audioURL, &audioType)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return audioURL, audioType, err
}

const downloadSelect = `
	SELECT d.episode_id, e.feed_id, e.title, e.link, f.title, f.site_url,
		e.image_url, f.image_url, e.duration_seconds, e.published_at,
		d.file_name, d.content_type, d.size_bytes, d.created_at
	FROM downloads d
	JOIN episodes e ON e.id = d.episode_id AND e.owner = d.owner
	JOIN feeds f ON f.id = e.feed_id
	WHERE d.owner = ?`

func scanDownload(row rowScanner) (Download, error) {
	var item Download
	err := row.Scan(&item.EpisodeID, &item.FeedID, &item.Title, &item.Link, &item.FeedTitle,
		&item.FeedSiteURL, &item.ImageURL, &item.FeedImageURL, &item.DurationSeconds,
		&item.PublishedAt, &item.FileName, &item.ContentType, &item.SizeBytes, &item.CreatedAt)
	return item, err
}

// ListDownloads returns every local download for one owner, newest first.
func (s *Store) ListDownloads(ctx context.Context, owner string) ([]Download, error) {
	rows, err := s.db.QueryContext(ctx, downloadSelect+` ORDER BY d.created_at DESC, d.episode_id DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Download{}
	for rows.Next() {
		item, err := scanDownload(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetDownload returns one owned download record.
func (s *Store) GetDownload(ctx context.Context, owner string, episodeID int64) (Download, error) {
	item, err := scanDownload(s.db.QueryRowContext(ctx, downloadSelect+` AND d.episode_id = ?`, owner, episodeID))
	if errors.Is(err, sql.ErrNoRows) {
		return Download{}, ErrNotFound
	}
	return item, err
}

// SaveDownload inserts or replaces the local metadata for an episode.
func (s *Store) SaveDownload(ctx context.Context, owner string, item Download) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO downloads (owner, episode_id, file_name, content_type, size_bytes, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner, episode_id) DO UPDATE SET
			file_name = excluded.file_name,
			content_type = excluded.content_type,
			size_bytes = excluded.size_bytes,
			created_at = excluded.created_at`,
		owner, item.EpisodeID, item.FileName, item.ContentType, item.SizeBytes, item.CreatedAt)
	return err
}

// DeleteDownload removes the metadata row and returns it so the service can
// delete the matching local file.
func (s *Store) DeleteDownload(ctx context.Context, owner string, episodeID int64) (Download, error) {
	item, err := s.GetDownload(ctx, owner, episodeID)
	if err != nil {
		return Download{}, err
	}
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM downloads WHERE owner = ? AND episode_id = ?`, owner, episodeID)
	if err != nil {
		return Download{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return Download{}, ErrNotFound
	}
	return item, nil
}

// TranscriptRefs returns the publisher-provided transcript URLs stored as
// normalized JSON by the feed layer.
func (s *Store) TranscriptRefs(ctx context.Context, owner string, episodeID int64) (string, error) {
	var refs string
	err := s.db.QueryRowContext(ctx,
		`SELECT transcript_refs FROM episodes WHERE id = ? AND owner = ?`, episodeID, owner).Scan(&refs)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return refs, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEpisode(row rowScanner) (Episode, error) {
	var ep Episode
	err := row.Scan(&ep.ID, &ep.FeedID, &ep.FeedTitle, &ep.FeedImageURL, &ep.FeedSiteURL, &ep.FeedURL,
		&ep.GUID, &ep.Title, &ep.Link, &ep.AudioURL, &ep.AudioType, &ep.ImageURL,
		&ep.DurationSeconds, &ep.PublishedAt, &ep.InboxState, &ep.HasTranscript,
		&ep.TranscriptRefsAvailable, &ep.ProgressSeconds, &ep.ProgressDuration,
		&ep.Completed, &ep.ProgressUpdatedAt)
	ep.Categories = []Category{}
	ep.Chapters = []Chapter{}
	return ep, err
}

// ownedIDs validates and de-duplicates a bulk identifier list. Callers still
// scope their SQL by owner; this only keeps queries bounded and predictable.
func ownedIDs(ids []int64, max int) ([]int64, error) {
	if len(ids) == 0 {
		return nil, errors.New("store: no ids")
	}
	if len(ids) > max {
		return nil, errors.New("store: too many ids")
	}
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, errors.New("store: no ids")
	}
	return out, nil
}

func truncateRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
