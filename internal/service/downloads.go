package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"boai/internal/store"
)

const maxDownloadBytes int64 = 512 << 20

var (
	// ErrDownloadsUnavailable means the deployment did not configure a local
	// download directory.
	ErrDownloadsUnavailable = errors.New("本地下载目录未配置")
	// ErrDownloadTooLarge is returned before a file over the per-episode limit
	// is committed to disk.
	ErrDownloadTooLarge = fmt.Errorf("音频文件超过 %d MiB，无法下载", maxDownloadBytes>>20)
)

// DownloadEpisode fetches an owned episode through the SSRF-safe audio path
// and saves it under the owner's private download directory. Repeated calls
// are idempotent while both the metadata row and file still exist.
func (s *Service) DownloadEpisode(ctx context.Context, owner string, episodeID int64) (store.Download, error) {
	if strings.TrimSpace(s.downloadDir) == "" {
		return store.Download{}, ErrDownloadsUnavailable
	}
	if existing, err := s.store.GetDownload(ctx, owner, episodeID); err == nil {
		if _, statErr := os.Stat(s.downloadPath(owner, existing.FileName)); statErr == nil {
			return existing, nil
		}
		// A stale row without a file is repairable by downloading again.
		_, _ = s.store.DeleteDownload(ctx, owner, episodeID)
	}
	episode, err := s.store.GetEpisode(ctx, owner, episodeID)
	if err != nil {
		return store.Download{}, err
	}
	resp, contentType, err := s.OpenAudio(ctx, owner, episodeID, "")
	if err != nil {
		return store.Download{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return store.Download{}, fmt.Errorf("音频源返回 HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxDownloadBytes {
		return store.Download{}, ErrDownloadTooLarge
	}

	dir := filepath.Join(s.downloadDir, ownerDirectory(owner))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return store.Download{}, fmt.Errorf("创建下载目录失败：%w", err)
	}
	temp, err := os.CreateTemp(dir, ".partial-*")
	if err != nil {
		return store.Download{}, fmt.Errorf("创建下载文件失败：%w", err)
	}
	tempName := temp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = temp.Close()
			_ = os.Remove(tempName)
		}
	}()

	size, err := io.Copy(temp, io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return store.Download{}, fmt.Errorf("下载音频失败：%w", err)
	}
	if size > maxDownloadBytes {
		return store.Download{}, ErrDownloadTooLarge
	}
	if size == 0 {
		return store.Download{}, errors.New("音频源返回了空文件")
	}
	if err := temp.Chmod(0o600); err != nil {
		return store.Download{}, fmt.Errorf("设置下载文件权限失败：%w", err)
	}
	if err := temp.Close(); err != nil {
		return store.Download{}, fmt.Errorf("写入下载文件失败：%w", err)
	}
	fileName := strconv.FormatInt(episodeID, 10) + DownloadExtension(contentType)
	finalPath := filepath.Join(dir, fileName)
	if err := os.Rename(tempName, finalPath); err != nil {
		return store.Download{}, fmt.Errorf("保存下载文件失败：%w", err)
	}
	committed = true

	item := store.Download{
		EpisodeID:       episode.ID,
		FeedID:          episode.FeedID,
		Title:           episode.Title,
		Link:            episode.Link,
		FeedTitle:       episode.FeedTitle,
		FeedSiteURL:     episode.FeedSiteURL,
		ImageURL:        episode.ImageURL,
		FeedImageURL:    episode.FeedImageURL,
		DurationSeconds: episode.DurationSeconds,
		PublishedAt:     episode.PublishedAt,
		FileName:        fileName,
		ContentType:     contentType,
		SizeBytes:       size,
		CreatedAt:       s.now().Unix(),
	}
	if err := s.store.SaveDownload(ctx, owner, item); err != nil {
		_ = os.Remove(finalPath)
		return store.Download{}, err
	}
	return item, nil
}

// ListDownloads returns owned local downloads.
func (s *Service) ListDownloads(ctx context.Context, owner string) ([]store.Download, error) {
	return s.store.ListDownloads(ctx, owner)
}

// OpenDownload opens a local downloaded file. A metadata row whose file has
// disappeared is removed and reported as not found so callers can fall back to
// the remote audio source.
func (s *Service) OpenDownload(ctx context.Context, owner string, episodeID int64) (*os.File, store.Download, error) {
	item, err := s.store.GetDownload(ctx, owner, episodeID)
	if err != nil {
		return nil, store.Download{}, err
	}
	file, err := os.Open(s.downloadPath(owner, item.FileName))
	if errors.Is(err, os.ErrNotExist) {
		_, _ = s.store.DeleteDownload(ctx, owner, episodeID)
		return nil, store.Download{}, store.ErrNotFound
	}
	if err != nil {
		return nil, store.Download{}, fmt.Errorf("打开本地下载失败：%w", err)
	}
	return file, item, nil
}

// DeleteDownload removes one local file and its metadata.
func (s *Service) DeleteDownload(ctx context.Context, owner string, episodeID int64) (bool, error) {
	item, err := s.store.GetDownload(ctx, owner, episodeID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := os.Remove(s.downloadPath(owner, item.FileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("删除本地文件失败：%w", err)
	}
	if _, err := s.store.DeleteDownload(ctx, owner, episodeID); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteDownloads removes several downloads and reports how many existed.
func (s *Service) DeleteDownloads(ctx context.Context, owner string, episodeIDs []int64) (int, error) {
	removed := 0
	for _, episodeID := range episodeIDs {
		ok, err := s.DeleteDownload(ctx, owner, episodeID)
		if err != nil {
			return removed, err
		}
		if ok {
			removed++
		}
	}
	return removed, nil
}

// RemoveFeeds deletes subscriptions and the local files belonging to them.
// The database cascade removes the metadata rows; files are removed from a
// snapshot taken before the delete.
func (s *Service) RemoveFeeds(ctx context.Context, owner string, feedIDs []int64) (int, error) {
	downloads, err := s.store.ListDownloads(ctx, owner)
	if err != nil {
		return 0, err
	}
	removed, err := s.store.DeleteFeeds(ctx, owner, feedIDs)
	if err != nil {
		return removed, err
	}
	wanted := make(map[int64]bool, len(feedIDs))
	for _, feedID := range feedIDs {
		wanted[feedID] = true
	}
	for _, item := range downloads {
		if wanted[item.FeedID] {
			// The user is unsubscribing; an unreadable leftover file should not
			// block removal of the subscription itself.
			_ = os.Remove(s.downloadPath(owner, item.FileName))
		}
	}
	return removed, nil
}

func ownerDirectory(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return hex.EncodeToString(sum[:16])
}

func (s *Service) downloadPath(owner, fileName string) string {
	// FileName comes from our own rows, but keeping only the base name means a
	// corrupted row still cannot escape the per-owner directory.
	return filepath.Join(s.downloadDir, ownerDirectory(owner), filepath.Base(fileName))
}

// DownloadExtension returns the local file suffix for a normalized audio
// content type.
func DownloadExtension(contentType string) string {
	base := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch base {
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/mp4", "video/mp4", "audio/x-m4a", "audio/m4a":
		return ".m4a"
	case "audio/aac":
		return ".aac"
	case "audio/ogg", "application/ogg":
		return ".ogg"
	case "audio/opus":
		return ".opus"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	case "audio/webm", "video/webm":
		return ".webm"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	default:
		return ".audio"
	}
}
