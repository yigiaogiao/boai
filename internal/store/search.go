package store

import (
	"context"
	"strings"
)

// SearchLibrary searches show names, episode titles/descriptions and stored
// transcript text. It never sends anything outside the process.
func (s *Store) SearchLibrary(ctx context.Context, owner, term string, limit int) ([]SearchResult, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return []SearchResult{}, nil
	}
	term = truncateRunes(term, 200)
	if limit <= 0 || limit > 50 {
		limit = 24
	}
	pattern := likePattern(term)
	results := []SearchResult{}

	feedRows, err := s.db.QueryContext(ctx, `
		SELECT id, title, description, image_url FROM feeds
		WHERE owner = ? AND hidden = 0 AND (title LIKE ? ESCAPE '\' OR description LIKE ? ESCAPE '\')
		ORDER BY title COLLATE NOCASE LIMIT ?`, owner, pattern, pattern, limit/3+1)
	if err != nil {
		return nil, err
	}
	for feedRows.Next() {
		var item SearchResult
		var description string
		if err := feedRows.Scan(&item.FeedID, &item.Title, &description, &item.ImageURL); err != nil {
			feedRows.Close()
			return nil, err
		}
		item.Kind = "feed"
		item.Snippet = truncateRunes(description, 160)
		results = append(results, item)
	}
	if err := feedRows.Close(); err != nil {
		return nil, err
	}

	episodeRows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.feed_id, e.title, f.title, COALESCE(NULLIF(e.image_url, ''), f.image_url), e.description
		FROM episodes e JOIN feeds f ON f.id = e.feed_id
		WHERE e.owner = ? AND f.hidden = 0 AND (e.title LIKE ? ESCAPE '\' OR e.description LIKE ? ESCAPE '\')
		ORDER BY e.published_at DESC, e.id DESC LIMIT ?`, owner, pattern, pattern, limit/2+1)
	if err != nil {
		return nil, err
	}
	for episodeRows.Next() {
		var item SearchResult
		var description string
		if err := episodeRows.Scan(&item.EpisodeID, &item.FeedID, &item.Title, &item.FeedTitle, &item.ImageURL, &description); err != nil {
			episodeRows.Close()
			return nil, err
		}
		item.Kind = "episode"
		item.Snippet = truncateRunes(description, 160)
		results = append(results, item)
	}
	if err := episodeRows.Close(); err != nil {
		return nil, err
	}

	transcriptRows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.feed_id, e.title, f.title, COALESCE(NULLIF(e.image_url, ''), f.image_url),
			ts.start_seconds, ts.text
		FROM transcript_segments ts
		JOIN episodes e ON e.id = ts.episode_id
		JOIN feeds f ON f.id = e.feed_id
		WHERE ts.owner = ? AND e.owner = ? AND f.hidden = 0 AND ts.text LIKE ? ESCAPE '\'
		ORDER BY e.published_at DESC, ts.start_seconds LIMIT ?`, owner, owner, pattern, limit/2+1)
	if err != nil {
		return nil, err
	}
	for transcriptRows.Next() {
		var item SearchResult
		var text string
		if err := transcriptRows.Scan(&item.EpisodeID, &item.FeedID, &item.Title, &item.FeedTitle,
			&item.ImageURL, &item.TimeSeconds, &text); err != nil {
			transcriptRows.Close()
			return nil, err
		}
		item.Kind = "transcript"
		item.Snippet = truncateRunes(text, 180)
		results = append(results, item)
	}
	if err := transcriptRows.Close(); err != nil {
		return nil, err
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func likePattern(term string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + replacer.Replace(term) + "%"
}
