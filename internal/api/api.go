// Package api exposes the same-origin HTTP API and serves the built frontend.
//
// The API is origin-agnostic: identity arrives as a trusted header when the
// deployment supplies one, otherwise the process uses a single local owner.
// LazyCat paths and gateway details never reach the service or store.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"boai/internal/service"
	"boai/internal/store"
)

// Options configures the HTTP server.
type Options struct {
	WebDir          string
	UserHeader      string
	TrustUserHeader bool
	MaxOPMLBytes    int64
	Version         string
	Logger          *log.Logger
}

// Server is the HTTP handler set.
type Server struct {
	svc          *service.Service
	store        *store.Store
	webDir       string
	userHeader   string
	trustUser    bool
	maxOPMLBytes int64
	version      string
	logger       *log.Logger
}

// New builds the HTTP layer.
func New(svc *service.Service, opts Options) *Server {
	if opts.UserHeader == "" {
		opts.UserHeader = "X-HC-User-ID"
	}
	if opts.MaxOPMLBytes <= 0 {
		opts.MaxOPMLBytes = 2 << 20
	}
	if opts.Version == "" {
		opts.Version = "0.1.0-dev"
	}
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	return &Server{
		svc:          svc,
		store:        svc.Store(),
		webDir:       opts.WebDir,
		userHeader:   opts.UserHeader,
		trustUser:    opts.TrustUserHeader,
		maxOPMLBytes: opts.MaxOPMLBytes,
		version:      opts.Version,
		logger:       opts.Logger,
	}
}

type ownerKey struct{}

func ownerFrom(ctx context.Context) string {
	value, _ := ctx.Value(ownerKey{}).(string)
	return value
}

// Handler returns the full HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/feeds", s.handleListFeeds)
	mux.HandleFunc("POST /api/feeds", s.handleSubscribe)
	mux.HandleFunc("POST /api/feeds/refresh", s.handleRefreshAll)
	mux.HandleFunc("POST /api/feeds/remove", s.handleRemoveFeeds)
	mux.HandleFunc("DELETE /api/feeds/{id}", s.handleDeleteFeed)
	mux.HandleFunc("POST /api/feeds/{id}/refresh", s.handleRefreshFeed)
	mux.HandleFunc("PUT /api/feeds/categories", s.handleSetFeedsCategories)
	mux.HandleFunc("PUT /api/feeds/{id}/categories", s.handleSetFeedCategories)

	mux.HandleFunc("GET /api/episodes", s.handleListEpisodes)
	mux.HandleFunc("POST /api/episodes/inbox", s.handleEpisodeInbox)
	mux.HandleFunc("GET /api/episodes/{id}", s.handleGetEpisode)
	mux.HandleFunc("GET /api/episodes/{id}/audio", s.handleAudio)
	mux.HandleFunc("HEAD /api/episodes/{id}/audio", s.handleAudio)
	mux.HandleFunc("POST /api/episodes/{id}/download", s.handleDownloadEpisode)
	mux.HandleFunc("DELETE /api/episodes/{id}/download", s.handleDeleteDownload)
	mux.HandleFunc("GET /api/downloads", s.handleListDownloads)
	mux.HandleFunc("POST /api/downloads/remove", s.handleRemoveDownloads)
	mux.HandleFunc("GET /api/episodes/{id}/transcript", s.handleGetTranscript)
	mux.HandleFunc("POST /api/episodes/{id}/transcript", s.handleFetchTranscript)
	mux.HandleFunc("GET /api/episodes/{id}/ai", s.handleEpisodeAI)
	mux.HandleFunc("POST /api/episodes/{id}/insights/{kind}", s.handleGenerateInsight)
	mux.HandleFunc("POST /api/episodes/{id}/translation", s.handleTranslateTranscript)
	mux.HandleFunc("POST /api/episodes/{id}/transcription", s.handleTranscribeEpisode)
	mux.HandleFunc("POST /api/episodes/{id}/ask", s.handleAskEpisode)
	mux.HandleFunc("PUT /api/episodes/{id}/progress", s.handleSaveProgress)

	mux.HandleFunc("GET /api/categories", s.handleListCategories)
	mux.HandleFunc("POST /api/categories", s.handleCreateCategory)
	mux.HandleFunc("PATCH /api/categories/{id}", s.handleRenameCategory)
	mux.HandleFunc("DELETE /api/categories/{id}", s.handleDeleteCategory)

	mux.HandleFunc("POST /api/links/resolve", s.handleResolve)
	mux.HandleFunc("GET /api/library/search", s.handleLibrarySearch)
	mux.HandleFunc("GET /api/directory/search", s.handleDirectorySearch)
	mux.HandleFunc("GET /api/discover", s.handleDiscover)
	mux.HandleFunc("POST /api/discover/show", s.handlePreviewShow)
	mux.HandleFunc("POST /api/opml", s.handleOPML)

	mux.HandleFunc("GET /api/ai/settings", s.handleAISettings)
	mux.HandleFunc("PUT /api/ai/settings/{kind}", s.handleSaveAISetting)
	mux.HandleFunc("POST /api/ai/settings/{kind}/test", s.handleTestAISetting)
	mux.HandleFunc("DELETE /api/ai/settings/{kind}", s.handleDeleteAISetting)
	mux.HandleFunc("GET /api/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("POST /api/tasks/{id}/retry", s.handleRetryTask)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "接口不存在")
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/", s.handleStatic)

	return s.withCommon(mux)
}

func (s *Server) withCommon(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			owner, err := s.identify(r)
			if err != nil {
				writeError(w, http.StatusUnauthorized, err.Error())
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			r = r.WithContext(context.WithValue(r.Context(), ownerKey{}, owner))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) identify(r *http.Request) (string, error) {
	if !s.trustUser {
		return "local", nil
	}
	value := strings.TrimSpace(r.Header.Get(s.userHeader))
	if value == "" {
		return "", fmt.Errorf("缺少网关用户标识")
	}
	if len([]rune(value)) > 200 {
		return "", fmt.Errorf("用户标识过长")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("用户标识无效")
		}
	}
	return value, nil
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	owner := ownerFrom(r.Context())
	status, err := s.store.GetStatus(r.Context(), owner)
	if err != nil {
		s.fail(w, err)
		return
	}
	aiStatus, err := s.svc.AIStatus(r.Context(), owner)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": s.version,
		"library": status,
		"ai":      aiStatus,
	})
}

func (s *Server) handleAISettings(w http.ResponseWriter, r *http.Request) {
	status, err := s.svc.AIStatus(r.Context(), ownerFrom(r.Context()))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ai": status})
}

func (s *Server) handleSaveAISetting(w http.ResponseWriter, r *http.Request) {
	var input service.AIConfigInput
	if !decodeJSON(w, r, &input) {
		return
	}
	status, err := s.svc.SaveAIConfig(r.Context(), ownerFrom(r.Context()), r.PathValue("kind"), input)
	if err != nil {
		s.aiFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": status})
}

func (s *Server) handleTestAISetting(w http.ResponseWriter, r *http.Request) {
	result, err := s.svc.TestAIConfig(r.Context(), ownerFrom(r.Context()), r.PathValue("kind"))
	if err != nil {
		s.aiFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleDeleteAISetting(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteAIConfig(r.Context(), ownerFrom(r.Context()), r.PathValue("kind")); err != nil {
		s.aiFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleEpisodeAI(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	aiResult, err := s.svc.ListEpisodeAI(r.Context(), ownerFrom(r.Context()), id)
	if err != nil {
		s.aiFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, aiResult)
}

func (s *Server) handleGenerateInsight(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	task, err := s.svc.EnqueueInsight(r.Context(), ownerFrom(r.Context()), id, r.PathValue("kind"))
	if err != nil {
		s.aiFail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"task": task})
}

func (s *Server) handleTranslateTranscript(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		TargetLanguage string `json:"target_language"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	task, err := s.svc.EnqueueTranslation(r.Context(), ownerFrom(r.Context()), id, input.TargetLanguage)
	if err != nil {
		s.aiFail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"task": task})
}

func (s *Server) handleTranscribeEpisode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	task, err := s.svc.EnqueueTranscription(r.Context(), ownerFrom(r.Context()), id)
	if err != nil {
		s.aiFail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"task": task})
}

func (s *Server) handleAskEpisode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Question string `json:"question"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	task, err := s.svc.EnqueueQuestion(r.Context(), ownerFrom(r.Context()), id, input.Question)
	if err != nil {
		s.aiFail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"task": task})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.store.GetTask(r.Context(), ownerFrom(r.Context()), r.PathValue("id"))
	if err != nil {
		s.aiFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task": map[string]any{
			"id": task.ID, "episode_id": task.EpisodeID, "kind": task.Kind, "status": task.Status,
			"provider": task.Provider, "error": task.Error, "progress": task.Progress,
			"payload": task.Payload, "attempts": task.Attempts, "created_at": task.CreatedAt,
			"updated_at": task.UpdatedAt, "started_at": task.StartedAt, "finished_at": task.FinishedAt,
		},
	})
}

func (s *Server) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.svc.RetryTask(r.Context(), ownerFrom(r.Context()), r.PathValue("id"))
	if err != nil {
		s.aiFail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"task": task})
}

func (s *Server) aiFail(w http.ResponseWriter, err error) {
	var unavailable *service.AIUnavailableError
	switch {
	case errors.As(err, &unavailable):
		writeError(w, http.StatusConflict, unavailable.Message)
	case errors.Is(err, service.ErrAIInput):
		message := strings.TrimPrefix(err.Error(), service.ErrAIInput.Error()+": ")
		writeError(w, http.StatusUnprocessableEntity, message)
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, err)
	default:
		s.fail(w, err)
	}
}

func (s *Server) handleListFeeds(w http.ResponseWriter, r *http.Request) {
	feeds, err := s.store.ListFeeds(r.Context(), ownerFrom(r.Context()))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feeds": feeds})
}

type subscribeRequest struct {
	Input       string  `json:"input"`
	FeedURL     string  `json:"feed_url"`
	CategoryIDs []int64 `json:"category_ids"`
}

func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	var body subscribeRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	feed, newEpisodes, err := s.svc.Subscribe(r.Context(), ownerFrom(r.Context()),
		body.Input, body.FeedURL, body.CategoryIDs)
	if err != nil {
		var resolveFailure *service.ResolveFailure
		if errors.As(err, &resolveFailure) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error":      resolveFailure.Result.Message,
				"resolution": resolveFailure.Result,
			})
			return
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"feed": feed, "new_episodes": newEpisodes})
}

func (s *Server) handleRefreshAll(w http.ResponseWriter, r *http.Request) {
	updated, added, failed := s.svc.RefreshAll(r.Context(), ownerFrom(r.Context()))
	writeJSON(w, http.StatusOK, map[string]any{
		"updated": updated, "new_episodes": added, "failed": failed,
	})
}

func (s *Server) handleRefreshFeed(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	result, err := s.svc.RefreshFeed(r.Context(), ownerFrom(r.Context()), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleDeleteFeed(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	removed, err := s.svc.RemoveFeeds(r.Context(), ownerFrom(r.Context()), []int64{id})
	if err != nil {
		s.fail(w, err)
		return
	}
	if removed == 0 {
		s.fail(w, store.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

func (s *Server) handleRemoveFeeds(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.IDs) == 0 || len(body.IDs) > 200 {
		writeError(w, http.StatusBadRequest, "请选择要取消关注的节目")
		return
	}
	removed, err := s.svc.RemoveFeeds(r.Context(), ownerFrom(r.Context()), body.IDs)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

type categoriesRequest struct {
	CategoryIDs []int64 `json:"category_ids"`
}

func (s *Server) handleSetFeedCategories(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body categoriesRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	owner := ownerFrom(r.Context())
	if err := s.store.SetFeedCategories(r.Context(), owner, id, body.CategoryIDs); err != nil {
		s.fail(w, err)
		return
	}
	feed, err := s.store.GetFeed(r.Context(), owner, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feed": feed})
}

func (s *Server) handleSetFeedsCategories(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FeedIDs     []int64 `json:"feed_ids"`
		CategoryIDs []int64 `json:"category_ids"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.FeedIDs) == 0 || len(body.FeedIDs) > 200 {
		writeError(w, http.StatusBadRequest, "请选择要分类的节目")
		return
	}
	updated, err := s.store.SetFeedsCategories(r.Context(), ownerFrom(r.Context()), body.FeedIDs, body.CategoryIDs)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated})
}

func (s *Server) handleListEpisodes(w http.ResponseWriter, r *http.Request) {
	filter := store.EpisodeFilter{Kind: r.URL.Query().Get("filter")}
	switch filter.Kind {
	case "", "all":
		filter.Kind = "all"
	case "new", "in_progress":
	case "category":
		id, err := strconv.ParseInt(r.URL.Query().Get("category"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "分类参数无效")
			return
		}
		filter.CategoryID = id
	default:
		writeError(w, http.StatusBadRequest, "筛选参数无效")
		return
	}
	if rawFeed := r.URL.Query().Get("feed"); rawFeed != "" {
		id, err := strconv.ParseInt(rawFeed, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "节目参数无效")
			return
		}
		filter.FeedID = id
	}
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit <= 0 || limit > 500 {
			writeError(w, http.StatusBadRequest, "数量参数无效")
			return
		}
		filter.Limit = limit
	}
	episodes, err := s.store.ListEpisodes(r.Context(), ownerFrom(r.Context()), filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"episodes": episodes})
}

func (s *Server) handleEpisodeInbox(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs    []int64 `json:"ids"`
		Action string  `json:"action"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.IDs) == 0 || len(body.IDs) > store.MaxBulkEpisodes {
		writeError(w, http.StatusBadRequest, "请选择要操作的单集")
		return
	}
	switch body.Action {
	case store.InboxRead, store.InboxUnread, store.InboxArchive, store.InboxUnarchive:
	default:
		writeError(w, http.StatusBadRequest, "收件箱操作无效")
		return
	}
	updated, err := s.store.SetEpisodeInboxState(r.Context(), ownerFrom(r.Context()), body.IDs, body.Action)
	if err != nil {
		s.fail(w, err)
		return
	}
	if updated == 0 {
		s.fail(w, store.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated})
}

func (s *Server) handleGetEpisode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	owner := ownerFrom(r.Context())
	episode, err := s.store.GetEpisode(r.Context(), owner, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"episode": episode})
}

func (s *Server) handleSaveProgress(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		Position float64 `json:"position_seconds"`
		Duration float64 `json:"duration_seconds"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	progress, err := s.store.SaveProgress(r.Context(), ownerFrom(r.Context()), id, body.Position, body.Duration)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, progress)
}

func (s *Server) handleGetTranscript(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	transcript, err := s.store.GetTranscript(r.Context(), ownerFrom(r.Context()), id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"transcript": nil})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transcript": transcript})
}

func (s *Server) handleFetchTranscript(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	transcript, err := s.svc.FetchTranscript(r.Context(), ownerFrom(r.Context()), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transcript": transcript})
}

func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	owner := ownerFrom(r.Context())
	asAttachment := r.URL.Query().Get("download") == "1"
	if file, item, err := s.svc.OpenDownload(r.Context(), owner, id); err == nil {
		defer file.Close()
		w.Header().Set("Content-Type", item.ContentType)
		w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
		if asAttachment {
			w.Header().Set("Content-Disposition", attachmentDisposition(item.Title, item.ContentType, item.EpisodeID))
		}
		http.ServeContent(w, r, item.FileName, time.Unix(item.CreatedAt, 0), file)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	resp, contentType, err := s.svc.OpenAudio(r.Context(), owner, id, r.Header.Get("Range"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, err)
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
	case http.StatusRequestedRangeNotSatisfiable:
		// Forward only the range metadata. Never relay the upstream error
		// body or its content type: it is commonly HTML from a CDN.
		for _, header := range []string{"Content-Range", "Accept-Ranges"} {
			if value := resp.Header.Get(header); value != "" {
				w.Header().Set(header, value)
			}
		}
		w.Header().Set("Content-Length", "0")
		w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	default:
		writeError(w, http.StatusBadGateway, fmt.Sprintf("音频源返回 HTTP %d", resp.StatusCode))
		return
	}
	for _, header := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if value := resp.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	if asAttachment {
		title := ""
		if episode, err := s.store.GetEpisode(r.Context(), owner, id); err == nil {
			title = episode.Title
		}
		w.Header().Set("Content-Disposition", attachmentDisposition(title, contentType, id))
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	copyStream(w, resp.Body)
}

func (s *Server) handleDownloadEpisode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, err := s.svc.DownloadEpisode(r.Context(), ownerFrom(r.Context()), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, err)
	case errors.Is(err, service.ErrDownloadsUnavailable):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, service.ErrDownloadTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
	case err != nil:
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"download": item})
	}
}

func (s *Server) handleDeleteDownload(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	removed, err := s.svc.DeleteDownload(r.Context(), ownerFrom(r.Context()), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !removed {
		s.fail(w, store.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": 1})
}

func (s *Server) handleListDownloads(w http.ResponseWriter, r *http.Request) {
	items, err := s.svc.ListDownloads(r.Context(), ownerFrom(r.Context()))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"downloads": items})
}

func (s *Server) handleRemoveDownloads(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.IDs) == 0 || len(body.IDs) > store.MaxBulkEpisodes {
		writeError(w, http.StatusBadRequest, "请选择要删除的下载")
		return
	}
	removed, err := s.svc.DeleteDownloads(r.Context(), ownerFrom(r.Context()), body.IDs)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

// attachmentDisposition builds a safe download filename. The ASCII fallback
// keeps old browsers working; RFC 5987 carries the podcast title in UTF-8.
func attachmentDisposition(title, contentType string, episodeID int64) string {
	ext := service.DownloadExtension(contentType)
	name := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			return ' '
		}
		return r
	}, title)
	name = strings.TrimSpace(strings.Join(strings.Fields(name), " "))
	runes := []rune(name)
	if len(runes) > 80 {
		name = string(runes[:80])
	}
	if name == "" {
		name = fmt.Sprintf("episode-%d", episodeID)
	}
	fallback := fmt.Sprintf("episode-%d%s", episodeID, ext)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, fallback, url.PathEscape(name+ext))
}

func copyStream(w http.ResponseWriter, src io.Reader) {
	flusher, _ := w.(http.Flusher)
	buffer := make([]byte, 32<<10)
	for {
		n, err := src.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *Server) handleListCategories(w http.ResponseWriter, r *http.Request) {
	categories, err := s.store.ListCategories(r.Context(), ownerFrom(r.Context()))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": categories})
}

func (s *Server) handleCreateCategory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	category, err := s.store.CreateCategory(r.Context(), ownerFrom(r.Context()), body.Name, body.Color)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "同名的分类已存在")
			return
		}
		if strings.Contains(err.Error(), "category name") {
			writeError(w, http.StatusBadRequest, "分类名称需要 1–20 个字符")
			return
		}
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, category)
}

func (s *Server) handleRenameCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	category, err := s.store.RenameCategory(r.Context(), ownerFrom(r.Context()), id, body.Name)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "同名的分类已存在")
			return
		}
		if strings.Contains(err.Error(), "category name") {
			writeError(w, http.StatusBadRequest, "分类名称需要 1–20 个字符")
			return
		}
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, category)
}

func (s *Server) handleDeleteCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteCategory(r.Context(), ownerFrom(r.Context()), id); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Input string `json:"input"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Input) == "" {
		writeError(w, http.StatusBadRequest, "请输入链接")
		return
	}
	writeJSON(w, http.StatusOK, s.svc.Resolve(r.Context(), body.Input))
}

func (s *Server) handleLibrarySearch(w http.ResponseWriter, r *http.Request) {
	term := r.URL.Query().Get("q")
	results, err := s.store.SearchLibrary(r.Context(), ownerFrom(r.Context()), term, 24)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) handleDirectorySearch(w http.ResponseWriter, r *http.Request) {
	term := strings.TrimSpace(r.URL.Query().Get("q"))
	if term == "" {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	items, err := s.svc.SearchDirectory(r.Context(), term)
	if err != nil {
		s.logger.Printf("api: directory search %q: %v", term, err)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "公开节目目录暂时不可用，本库搜索不受影响",
			"items": []any{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	board, err := s.svc.Discover(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "热门榜单暂时不可用，可以继续搜索或粘贴订阅链接",
		})
		return
	}
	writeJSON(w, http.StatusOK, board)
}

func (s *Server) handlePreviewShow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FeedURL string `json:"feed_url"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.FeedURL) == "" {
		writeError(w, http.StatusBadRequest, "缺少节目地址")
		return
	}
	feedRow, episodes, subscribed, err := s.svc.PreviewShow(r.Context(), ownerFrom(r.Context()), body.FeedURL)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"feed": feedRow, "episodes": episodes, "subscribed": subscribed,
	})
}

func (s *Server) handleOPML(w http.ResponseWriter, r *http.Request) {
	data, err := s.readOPML(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.svc.ImportOPML(r.Context(), ownerFrom(r.Context()), data)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) readOPML(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		limit := s.maxOPMLBytes + (1 << 20)
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		if err := r.ParseMultipartForm(limit); err != nil {
			return nil, errors.New("无法读取 OPML 文件")
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			return nil, errors.New("缺少 OPML 文件")
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, s.maxOPMLBytes+1))
		if err != nil {
			return nil, errors.New("无法读取 OPML 文件")
		}
		if int64(len(data)) > s.maxOPMLBytes {
			return nil, errors.New("OPML 文件不能超过 2 MB")
		}
		return data, nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxOPMLBytes+1)
	data, err := io.ReadAll(io.LimitReader(r.Body, s.maxOPMLBytes+1))
	if err != nil {
		return nil, errors.New("无法读取 OPML 文件")
	}
	if int64(len(data)) > s.maxOPMLBytes {
		return nil, errors.New("OPML 文件不能超过 2 MB")
	}
	if len(data) == 0 {
		return nil, errors.New("OPML 文件为空")
	}
	return data, nil
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cleanPath := filepath.Clean("/" + r.URL.Path)
	if cleanPath == "/" {
		cleanPath = "/index.html"
	}
	full := filepath.Join(s.webDir, filepath.FromSlash(cleanPath))
	if info, err := os.Stat(full); err == nil && !info.IsDir() {
		if strings.HasPrefix(cleanPath, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFile(w, r, full)
		return
	}
	if filepath.Ext(cleanPath) != "" {
		http.NotFound(w, r)
		return
	}
	index := filepath.Join(s.webDir, "index.html")
	if _, err := os.Stat(index); err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "前端尚未构建。开发时运行 npm run dev，或先 npm run build。")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, index)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "未找到该内容")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "同名的分类已存在")
	default:
		s.logger.Printf("api: %v", err)
		writeError(w, http.StatusInternalServerError, "操作失败，请稍后重试")
	}
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "无效的 ID")
		return 0, false
	}
	return id, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "请求内容格式错误")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(value); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}
