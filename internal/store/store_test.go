package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "boai-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func sampleFeed(episodeCount int) FeedInput {
	now := time.Now().Unix()
	in := FeedInput{
		URL:         "https://example.com/feed.xml",
		SiteURL:     "https://example.com/",
		Title:       "测试节目",
		Description: "测试用节目",
		ImageURL:    "https://example.com/cover.jpg",
		ETag:        `"v1"`,
	}
	for i := 1; i <= episodeCount; i++ {
		in.Episodes = append(in.Episodes, EpisodeInput{
			GUID:            "episode-" + string(rune('0'+i)),
			Title:           "第 " + string(rune('0'+i)) + " 集",
			Description:     "单集简介",
			Link:            "https://example.com/episode",
			AudioURL:        "https://example.com/audio.mp3",
			AudioType:       "audio/mpeg",
			DurationSeconds: 120,
			PublishedAt:     now - int64(i*100),
			TranscriptRefs:  `[]`,
			Chapters:        `[]`,
		})
	}
	return in
}

func TestUpsertFeedDedupesAndKeepsProgress(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner := "user-a"

	feedID, added, err := st.UpsertFeed(ctx, owner, sampleFeed(2))
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("added = %d, want 2", added)
	}
	episodes, err := st.ListEpisodes(ctx, owner, EpisodeFilter{Kind: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 2 {
		t.Fatalf("episodes = %d, want 2", len(episodes))
	}
	if episodes[0].FeedURL != "https://example.com/feed.xml" {
		t.Fatalf("episode feed URL = %q", episodes[0].FeedURL)
	}
	episodeID := episodes[0].ID
	if _, err := st.SaveProgress(ctx, owner, episodeID, 30, 120); err != nil {
		t.Fatal(err)
	}

	_, added, err = st.UpsertFeed(ctx, owner, sampleFeed(3))
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added on second upsert = %d, want 1", added)
	}
	episodes, err = st.ListEpisodes(ctx, owner, EpisodeFilter{Kind: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 3 {
		t.Fatalf("episodes after upsert = %d, want 3", len(episodes))
	}
	started, err := st.ListEpisodes(ctx, owner, EpisodeFilter{Kind: "in_progress"})
	if err != nil {
		t.Fatal(err)
	}
	if len(started) != 1 || started[0].ID != episodeID || started[0].ProgressSeconds != 30 {
		t.Fatalf("in-progress after upsert = %#v", started)
	}

	feed, err := st.GetFeed(ctx, owner, feedID)
	if err != nil {
		t.Fatal(err)
	}
	if feed.EpisodeCount != 3 || feed.NewCount != 2 {
		t.Fatalf("feed counts = %d/%d, want 3/2", feed.EpisodeCount, feed.NewCount)
	}
}

func TestProgressCompletionAndInboxFilters(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner := "user-a"
	_, _, err := st.UpsertFeed(ctx, owner, sampleFeed(2))
	if err != nil {
		t.Fatal(err)
	}
	episodes, _ := st.ListEpisodes(ctx, owner, EpisodeFilter{Kind: "new"})
	if len(episodes) != 2 {
		t.Fatalf("new episodes = %d, want 2", len(episodes))
	}
	episodeID := episodes[0].ID

	progress, err := st.SaveProgress(ctx, owner, episodeID, 10, 120)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Completed {
		t.Fatal("10/120 must not be completed")
	}
	inProgress, _ := st.ListEpisodes(ctx, owner, EpisodeFilter{Kind: "in_progress"})
	if len(inProgress) != 1 {
		t.Fatalf("in progress = %d, want 1", len(inProgress))
	}
	if status, err := st.GetStatus(ctx, owner); err != nil || status.InProgress != 1 {
		t.Fatalf("status in_progress = %d (err %v), want 1", status.InProgress, err)
	}

	progress, err = st.SaveProgress(ctx, owner, episodeID, 119, 120)
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Completed {
		t.Fatal("119/120 must count as completed")
	}
	inProgress, _ = st.ListEpisodes(ctx, owner, EpisodeFilter{Kind: "in_progress"})
	if len(inProgress) != 0 {
		t.Fatalf("completed episode still in progress: %#v", inProgress)
	}
	if status, err := st.GetStatus(ctx, owner); err != nil || status.InProgress != 0 {
		t.Fatalf("status in_progress = %d (err %v), want 0", status.InProgress, err)
	}
	episode, err := st.GetEpisode(ctx, owner, episodeID)
	if err != nil {
		t.Fatal(err)
	}
	if episode.InboxState != "completed" || !episode.Completed {
		t.Fatalf("episode state = %q completed=%v", episode.InboxState, episode.Completed)
	}
	if episode.FeedURL != "https://example.com/feed.xml" {
		t.Fatalf("GetEpisode feed URL = %q", episode.FeedURL)
	}

	if _, err := st.SaveProgress(ctx, owner, episodeID, 0, 120); err != nil {
		t.Fatal(err)
	}
	episode, _ = st.GetEpisode(ctx, owner, episodeID)
	if episode.InboxState != "new" {
		t.Fatalf("rewinding to zero should return to new, got %q", episode.InboxState)
	}
}

func TestOwnerIsolation(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	_, _, err := st.UpsertFeed(ctx, "owner-a", sampleFeed(1))
	if err != nil {
		t.Fatal(err)
	}
	episodes, err := st.ListEpisodes(ctx, "owner-a", EpisodeFilter{Kind: "all"})
	if err != nil || len(episodes) != 1 {
		t.Fatalf("owner-a episodes = %#v, %v", episodes, err)
	}
	foreign, err := st.ListEpisodes(ctx, "owner-b", EpisodeFilter{Kind: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign) != 0 {
		t.Fatalf("owner-b sees %#v", foreign)
	}
	if _, err := st.GetEpisode(ctx, "owner-b", episodes[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign GetEpisode err = %v, want ErrNotFound", err)
	}
	if _, err := st.SaveProgress(ctx, "owner-b", episodes[0].ID, 10, 120); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign SaveProgress err = %v, want ErrNotFound", err)
	}
	if _, err := st.GetFeed(ctx, "owner-b", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign GetFeed err = %v, want ErrNotFound", err)
	}
}

func TestCategoriesAndFeedAssignments(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner := "user-a"
	feedID, _, err := st.UpsertFeed(ctx, owner, sampleFeed(1))
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.CreateCategory(ctx, owner, "设计与创作", "#5b826c")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateCategory(ctx, owner, "设计与创作", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate category err = %v, want ErrConflict", err)
	}
	second, err := st.CreateCategory(ctx, owner, "生活随想", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetFeedCategories(ctx, owner, feedID, []int64{first.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	feed, err := st.GetFeed(ctx, owner, feedID)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Categories) != 2 {
		t.Fatalf("feed categories = %#v", feed.Categories)
	}
	categories, _ := st.ListCategories(ctx, owner)
	for _, category := range categories {
		if category.ID == first.ID && category.FeedCount != 1 {
			t.Fatalf("category feed count = %d, want 1", category.FeedCount)
		}
	}
	if _, err := st.RenameCategory(ctx, "owner-b", first.ID, "越权"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign rename err = %v", err)
	}
	renamed, err := st.RenameCategory(ctx, owner, first.ID, "创作")
	if err != nil || renamed.Name != "创作" {
		t.Fatalf("rename = %#v, %v", renamed, err)
	}
	if err := st.DeleteCategory(ctx, owner, second.ID); err != nil {
		t.Fatal(err)
	}
	feed, _ = st.GetFeed(ctx, owner, feedID)
	if len(feed.Categories) != 1 || feed.Categories[0].ID != first.ID {
		t.Fatalf("feed categories after delete = %#v", feed.Categories)
	}
	if _, err := st.GetFeed(ctx, owner, feedID); err != nil {
		t.Fatalf("deleting a category must keep the subscription: %v", err)
	}

	secondInput := sampleFeed(2)
	secondInput.URL = "https://example.com/second.xml"
	secondFeedID, _, err := st.UpsertFeed(ctx, owner, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := st.SetFeedsCategories(ctx, owner, []int64{feedID, secondFeedID}, []int64{first.ID})
	if err != nil || updated != 2 {
		t.Fatalf("bulk categories = %d, %v", updated, err)
	}
	for _, id := range []int64{feedID, secondFeedID} {
		feed, err := st.GetFeed(ctx, owner, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(feed.Categories) != 1 || feed.Categories[0].ID != first.ID {
			t.Fatalf("feed %d categories = %#v", id, feed.Categories)
		}
	}
	if _, err := st.SetFeedsCategories(ctx, "owner-b", []int64{feedID}, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign bulk categories err = %v, want ErrNotFound", err)
	}
}

func TestTranscriptStorageAndSearch(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner := "user-a"
	_, _, _ = st.UpsertFeed(ctx, owner, sampleFeed(1))
	episodes, _ := st.ListEpisodes(ctx, owner, EpisodeFilter{Kind: "all"})
	episodeID := episodes[0].ID

	segments := []Segment{
		{Index: 0, Start: 0, End: 8, Speaker: "主持人", Text: "你有没有试过认真听一个人说话？"},
		{Index: 1, Start: 9, End: 18, Speaker: "嘉宾", Text: "倾听不是等着轮到自己说话，而是允许对方改变方向。"},
	}
	if err := st.SaveTranscript(ctx, owner, episodeID, "fixture.vtt", "zh-CN", "", true, segments); err != nil {
		t.Fatal(err)
	}
	stored, err := st.GetTranscript(ctx, owner, episodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Timed || len(stored.Segments) != 2 || stored.Segments[1].Speaker != "嘉宾" {
		t.Fatalf("stored transcript = %#v", stored)
	}
	episode, _ := st.GetEpisode(ctx, owner, episodeID)
	if !episode.HasTranscript {
		t.Fatal("episode must be marked as having a transcript")
	}
	if _, err := st.GetTranscript(ctx, "owner-b", episodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign transcript err = %v", err)
	}
	results, err := st.SearchLibrary(ctx, owner, "倾听", 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, result := range results {
		if result.Kind == "transcript" && result.EpisodeID == episodeID && result.TimeSeconds == 9 {
			found = true
		}
	}
	if !found {
		t.Fatalf("transcript search results = %#v", results)
	}
	for _, result := range results {
		if result.Kind == "transcript" && result.EpisodeID == 0 {
			t.Fatalf("transcript result lacks episode target: %#v", result)
		}
	}
}

func TestInboxManualActionsAndArchiveStickiness(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner := "user-a"
	feedID, _, err := st.UpsertFeed(ctx, owner, sampleFeed(2))
	if err != nil {
		t.Fatal(err)
	}
	episodes, err := st.ListEpisodes(ctx, owner, EpisodeFilter{Kind: "all"})
	if err != nil || len(episodes) != 2 {
		t.Fatalf("episodes = %#v, %v", episodes, err)
	}
	first, second := episodes[0].ID, episodes[1].ID

	updated, err := st.SetEpisodeInboxState(ctx, owner, []int64{first, second}, InboxRead)
	if err != nil || updated != 2 {
		t.Fatalf("mark read = %d, %v", updated, err)
	}
	episode, err := st.GetEpisode(ctx, owner, first)
	if err != nil {
		t.Fatal(err)
	}
	if episode.InboxState != "completed" || !episode.Completed || episode.ProgressDuration != 120 {
		t.Fatalf("read episode = %#v", episode)
	}
	if newEpisodes, _ := st.ListEpisodes(ctx, owner, EpisodeFilter{Kind: "new"}); len(newEpisodes) != 0 {
		t.Fatalf("marked-read episodes still new: %#v", newEpisodes)
	}
	if inProgress, _ := st.ListEpisodes(ctx, owner, EpisodeFilter{Kind: "in_progress"}); len(inProgress) != 0 {
		t.Fatalf("marked-read episodes still in progress: %#v", inProgress)
	}

	if _, err := st.SetEpisodeInboxState(ctx, owner, []int64{first}, InboxUnread); err != nil {
		t.Fatal(err)
	}
	episode, _ = st.GetEpisode(ctx, owner, first)
	if episode.InboxState != "new" || episode.Completed {
		t.Fatalf("unread episode = %#v", episode)
	}

	if _, err := st.SaveProgress(ctx, owner, first, 30, 120); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetEpisodeInboxState(ctx, owner, []int64{first}, InboxArchive); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveProgress(ctx, owner, first, 40, 120); err != nil {
		t.Fatal(err)
	}
	episode, _ = st.GetEpisode(ctx, owner, first)
	if episode.InboxState != "archived" || episode.ProgressSeconds != 40 {
		t.Fatalf("playback resurrected archived episode: %#v", episode)
	}
	if inbox, _ := st.ListEpisodes(ctx, owner, EpisodeFilter{Kind: "all"}); len(inbox) != 1 || inbox[0].ID != second {
		t.Fatalf("inbox after archive = %#v", inbox)
	}
	if show, _ := st.ListEpisodes(ctx, owner, EpisodeFilter{Kind: "all", FeedID: feedID}); len(show) != 2 {
		t.Fatalf("show page after archive = %#v", show)
	}

	if _, err := st.SetEpisodeInboxState(ctx, owner, []int64{first}, InboxUnread); err != nil {
		t.Fatal(err)
	}
	episode, _ = st.GetEpisode(ctx, owner, first)
	if episode.InboxState != "archived" {
		t.Fatalf("mark unread resurrected archived episode: %#v", episode)
	}
	if _, err := st.SetEpisodeInboxState(ctx, owner, []int64{first}, InboxUnarchive); err != nil {
		t.Fatal(err)
	}
	episode, _ = st.GetEpisode(ctx, owner, first)
	if episode.InboxState != "started" || episode.ProgressSeconds != 40 {
		t.Fatalf("restored in-progress episode = %#v", episode)
	}

	if _, err := st.SetEpisodeInboxState(ctx, owner, []int64{second}, InboxArchive); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetEpisodeInboxState(ctx, owner, []int64{second}, InboxUnarchive); err != nil {
		t.Fatal(err)
	}
	episode, _ = st.GetEpisode(ctx, owner, second)
	if episode.InboxState != "completed" {
		t.Fatalf("restored completed episode = %#v", episode)
	}

	if updated, err := st.SetEpisodeInboxState(ctx, "user-b", []int64{first}, InboxArchive); err != nil || updated != 0 {
		t.Fatalf("foreign mark archived = %d, %v", updated, err)
	}
	episode, _ = st.GetEpisode(ctx, owner, first)
	if episode.InboxState != "started" {
		t.Fatalf("foreign owner changed episode: %#v", episode)
	}
}

func TestDeleteFeedsCascadesAndScopesOwner(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner := "user-a"
	feedID, _, err := st.UpsertFeed(ctx, owner, sampleFeed(2))
	if err != nil {
		t.Fatal(err)
	}
	secondFeedID, _, err := st.UpsertFeed(ctx, owner, FeedInput{
		URL:      "https://example.com/another.xml",
		Title:    "另一个节目",
		Episodes: sampleFeed(1).Episodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	foreignFeedID, _, err := st.UpsertFeed(ctx, "user-b", sampleFeed(1))
	if err != nil {
		t.Fatal(err)
	}
	episodes, _ := st.ListEpisodes(ctx, owner, EpisodeFilter{Kind: "all", FeedID: feedID})
	if len(episodes) == 0 {
		t.Fatal("no episodes for cascade check")
	}
	if _, err := st.SaveProgress(ctx, owner, episodes[0].ID, 30, 120); err != nil {
		t.Fatal(err)
	}

	removed, err := st.DeleteFeeds(ctx, owner, []int64{feedID})
	if err != nil || removed != 1 {
		t.Fatalf("delete feed = %d, %v", removed, err)
	}
	if _, err := st.GetFeed(ctx, owner, feedID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted feed still present: %v", err)
	}
	for _, table := range []string{"episodes", "progress"} {
		var count int
		query := map[string]string{
			"episodes": `SELECT COUNT(*) FROM episodes WHERE feed_id = ?`,
			"progress": `SELECT COUNT(*) FROM progress`,
		}[table]
		args := []any{}
		if table == "episodes" {
			args = append(args, feedID)
		}
		if err := st.DB().QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows survived feed deletion: %d", table, count)
		}
	}

	if removed, err := st.DeleteFeeds(ctx, owner, []int64{foreignFeedID}); err != nil || removed != 0 {
		t.Fatalf("foreign feed delete = %d, %v", removed, err)
	}
	if _, err := st.GetFeed(ctx, "user-b", foreignFeedID); err != nil {
		t.Fatalf("foreign feed was removed: %v", err)
	}
	removed, err = st.DeleteFeeds(ctx, owner, []int64{secondFeedID, foreignFeedID, secondFeedID})
	if err != nil || removed != 1 {
		t.Fatalf("bulk feed delete = %d, %v", removed, err)
	}
	if feeds, _ := st.ListFeeds(ctx, owner); len(feeds) != 0 {
		t.Fatalf("owner feeds after bulk delete = %#v", feeds)
	}
	if _, err := st.GetFeed(ctx, "user-b", foreignFeedID); err != nil {
		t.Fatalf("bulk delete crossed owners: %v", err)
	}
}

func TestFeedFetchStateAndDueFeeds(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner := "user-a"
	feedID, _, err := st.UpsertFeed(ctx, owner, sampleFeed(1))
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := st.RecordFeedResult(ctx, owner, feedID, false, "", "", past, "测试失败"); err != nil {
		t.Fatal(err)
	}
	state, err := st.FeedFetchState(ctx, owner, feedID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ConsecutiveFailures != 1 || state.URL != "https://example.com/feed.xml" {
		t.Fatalf("fetch state = %#v", state)
	}
	due, err := st.DueFeeds(ctx, time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != feedID {
		t.Fatalf("due feeds = %#v", due)
	}
	if err := st.RecordFeedResult(ctx, owner, feedID, true, `"v2"`, "Wed, 19 Nov 2025 00:00:00 GMT",
		time.Now().Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	due, _ = st.DueFeeds(ctx, time.Now(), 10)
	if len(due) != 0 {
		t.Fatalf("future feed is due: %#v", due)
	}
	feed, _ := st.GetFeed(ctx, owner, feedID)
	if feed.LastError != "" || feed.ConsecutiveFailures != 0 {
		t.Fatalf("feed failure state was not cleared: %#v", feed)
	}
}
