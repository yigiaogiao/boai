package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const maxStoredSegments = 20000

// SaveTranscript replaces an episode's transcript and marks it available.
func (s *Store) SaveTranscript(ctx context.Context, owner string, episodeID int64, source, language, translatedLanguage string, timed bool, segments []Segment) error {
	if len(segments) == 0 {
		return errors.New("store: transcript has no segments")
	}
	if len(segments) > maxStoredSegments {
		segments = segments[:maxStoredSegments]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM episodes WHERE id = ? AND owner = ?`, episodeID, owner).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	now := time.Now().Unix()
	var transcriptID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM transcripts WHERE owner = ? AND episode_id = ?`,
		owner, episodeID).Scan(&transcriptID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, execErr := tx.ExecContext(ctx, `
			INSERT INTO transcripts (owner, episode_id, source, language, translated_language, timed, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			owner, episodeID, truncateRunes(source, 40), truncateRunes(language, 32),
			truncateRunes(translatedLanguage, 32), boolInt(timed), now)
		if execErr != nil {
			return execErr
		}
		transcriptID, err = res.LastInsertId()
		if err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if _, err := tx.ExecContext(ctx, `
			UPDATE transcripts SET source = ?, language = ?, translated_language = ?, timed = ?
			WHERE id = ? AND owner = ?`,
			truncateRunes(source, 40), truncateRunes(language, 32),
			truncateRunes(translatedLanguage, 32), boolInt(timed), transcriptID, owner); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM transcript_segments WHERE transcript_id = ?`, transcriptID); err != nil {
			return err
		}
	}
	for i, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO transcript_segments (transcript_id, owner, episode_id, idx, start_seconds,
				end_seconds, speaker, text, translation)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			transcriptID, owner, episodeID, i, segment.Start, segment.End,
			truncateRunes(segment.Speaker, 100), truncateRunes(text, 8000),
			truncateRunes(segment.Translation, 8000)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE episodes SET has_transcript = 1, updated_at = ? WHERE id = ? AND owner = ?`,
		now, episodeID, owner); err != nil {
		return err
	}
	return tx.Commit()
}

// GetTranscript returns the stored transcript, or ErrNotFound.
func (s *Store) GetTranscript(ctx context.Context, owner string, episodeID int64) (Transcript, error) {
	var out Transcript
	var id int64
	var timed int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, source, language, translated_language, timed FROM transcripts
		WHERE owner = ? AND episode_id = ?`, owner, episodeID).
		Scan(&id, &out.Source, &out.Language, &out.TranslatedLanguage, &timed)
	if errors.Is(err, sql.ErrNoRows) {
		return Transcript{}, ErrNotFound
	}
	if err != nil {
		return Transcript{}, err
	}
	out.Timed = timed != 0
	rows, err := s.db.QueryContext(ctx, `
		SELECT idx, start_seconds, end_seconds, speaker, text, translation
		FROM transcript_segments WHERE transcript_id = ? ORDER BY start_seconds, idx`, id)
	if err != nil {
		return Transcript{}, err
	}
	defer rows.Close()
	out.Segments = []Segment{}
	for rows.Next() {
		var segment Segment
		if err := rows.Scan(&segment.Index, &segment.Start, &segment.End, &segment.Speaker,
			&segment.Text, &segment.Translation); err != nil {
			return Transcript{}, err
		}
		out.Segments = append(out.Segments, segment)
	}
	return out, rows.Err()
}

// Status returns health counters for the dashboard.
type Status struct {
	Feeds       int   `json:"feeds"`
	Episodes    int   `json:"episodes"`
	NewEpisodes int   `json:"new_episodes"`
	InProgress  int   `json:"in_progress"`
	LastUpdated int64 `json:"last_updated"`
}

// GetStatus summarizes one owner's library.
func (s *Store) GetStatus(ctx context.Context, owner string) (Status, error) {
	var out Status
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM feeds WHERE owner = ? AND hidden = 0),
			(SELECT COUNT(*) FROM episodes e JOIN feeds f ON f.id = e.feed_id
				WHERE e.owner = ? AND f.hidden = 0),
			(SELECT COUNT(*) FROM episodes e JOIN feeds f ON f.id = e.feed_id
				WHERE e.owner = ? AND f.hidden = 0 AND e.inbox_state = 'new'),
			(SELECT COUNT(*) FROM episodes e JOIN feeds f ON f.id = e.feed_id
				JOIN progress p ON p.owner = e.owner AND p.episode_id = e.id
				WHERE e.owner = ? AND f.hidden = 0 AND e.inbox_state <> 'archived'
					AND p.position_seconds > 0 AND p.completed = 0),
			(SELECT COALESCE(MAX(last_checked_at), 0) FROM feeds WHERE owner = ? AND hidden = 0)`,
		owner, owner, owner, owner, owner).
		Scan(&out.Feeds, &out.Episodes, &out.NewEpisodes, &out.InProgress, &out.LastUpdated)
	return out, err
}
