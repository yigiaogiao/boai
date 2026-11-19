import type {
  AIProviderStatus,
  AITask,
  AITestResult,
  Category,
  DirectoryItem,
  DiscoverBoard,
  Download,
  Episode,
  EpisodeAI,
  EpisodeDetail,
  Feed,
  OPMLResult,
  Progress,
  Resolution,
  SearchResult,
  ShowPreview,
  StatusResponse,
  Transcript,
} from './types'

export class ApiError extends Error {
  status: number
  payload: unknown

  constructor(message: string, status: number, payload: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.payload = payload
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, init)
  const text = await response.text()
  let payload: unknown = null
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = null
    }
  }
  if (!response.ok) {
    const message =
      payload && typeof payload === 'object' && 'error' in payload
        ? String((payload as { error: unknown }).error)
        : `请求失败（HTTP ${response.status}）`
    throw new ApiError(message, response.status, payload)
  }
  return payload as T
}

function jsonInit(method: string, body: unknown): RequestInit {
  return {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }
}

export function getStatus(): Promise<StatusResponse> {
  return request<StatusResponse>('/api/status')
}

export function getFeeds(): Promise<Feed[]> {
  return request<{ feeds: Feed[] }>('/api/feeds').then((value) => value.feeds)
}

export type EpisodeListFilter =
  | { kind: 'all' | 'new' | 'in_progress' }
  | { kind: 'category'; categoryId: number }

export function getEpisodes(filter: EpisodeListFilter, feedId?: number, limit?: number): Promise<Episode[]> {
  const params = new URLSearchParams()
  params.set('filter', filter.kind)
  if (filter.kind === 'category') params.set('category', String(filter.categoryId))
  if (feedId) params.set('feed', String(feedId))
  if (limit) params.set('limit', String(limit))
  return request<{ episodes: Episode[] }>(`/api/episodes?${params.toString()}`).then(
    (value) => value.episodes ?? [],
  )
}

export function getEpisode(id: number): Promise<EpisodeDetail> {
  return request<EpisodeDetail>(`/api/episodes/${id}`)
}

export function getTranscript(id: number): Promise<Transcript | null> {
  return request<{ transcript: Transcript | null }>(`/api/episodes/${id}/transcript`).then(
    (value) => value.transcript,
  )
}

export function fetchTranscript(id: number): Promise<Transcript> {
  return request<{ transcript: Transcript }>(`/api/episodes/${id}/transcript`, {
    method: 'POST',
  }).then((value) => value.transcript)
}

export function saveProgress(
  id: number,
  position: number,
  duration: number,
  keepalive = false,
): Promise<Progress> {
  return request<Progress>(`/api/episodes/${id}/progress`, {
    ...jsonInit('PUT', { position_seconds: position, duration_seconds: duration }),
    keepalive,
  })
}

export function getCategories(): Promise<Category[]> {
  return request<{ categories: Category[] }>('/api/categories').then(
    (value) => value.categories ?? [],
  )
}

export function createCategory(name: string, color = ''): Promise<Category> {
  return request<Category>('/api/categories', jsonInit('POST', { name, color }))
}

export function renameCategory(id: number, name: string): Promise<Category> {
  return request<Category>(`/api/categories/${id}`, jsonInit('PATCH', { name }))
}

export function deleteCategory(id: number): Promise<void> {
  return request<unknown>(`/api/categories/${id}`, { method: 'DELETE' }).then(() => undefined)
}

export function setFeedsCategories(feedIds: number[], categoryIds: number[]): Promise<number> {
  return request<{ updated: number }>(
    '/api/feeds/categories',
    jsonInit('PUT', { feed_ids: feedIds, category_ids: categoryIds }),
  ).then((value) => value.updated)
}

export function setFeedCategories(feedId: number, categoryIds: number[]): Promise<Feed> {
  return request<{ feed: Feed }>(`/api/feeds/${feedId}/categories`, jsonInit('PUT', {
    category_ids: categoryIds,
  })).then((value) => value.feed)
}

export function subscribe(
  input: string,
  feedUrl: string,
  categoryIds: number[],
): Promise<{ feed: Feed; new_episodes: number }> {
  return request('/api/feeds', jsonInit('POST', {
    input,
    feed_url: feedUrl,
    category_ids: categoryIds,
  }))
}

export function refreshAll(): Promise<{ updated: number; new_episodes: number; failed: number }> {
  return request('/api/feeds/refresh', { method: 'POST' })
}

export type EpisodeInboxAction = 'read' | 'unread' | 'archive' | 'unarchive'

export function setEpisodesInboxState(
  ids: number[],
  action: EpisodeInboxAction,
): Promise<{ updated: number }> {
  return request('/api/episodes/inbox', jsonInit('POST', { ids, action }))
}

export function removeFeeds(ids: number[]): Promise<{ removed: number }> {
  return request('/api/feeds/remove', jsonInit('POST', { ids }))
}

export function resolveLink(input: string): Promise<Resolution> {
  return request<Resolution>('/api/links/resolve', jsonInit('POST', { input }))
}

export function searchLibrary(query: string): Promise<SearchResult[]> {
  return request<{ results: SearchResult[] }>(
    `/api/library/search?q=${encodeURIComponent(query)}`,
  ).then((value) => value.results ?? [])
}

export function searchDirectory(query: string): Promise<DirectoryItem[]> {
  return request<{ items: DirectoryItem[]; error?: string }>(
    `/api/directory/search?q=${encodeURIComponent(query)}`,
  ).then((value) => value.items ?? [])
}

export function getDiscover(): Promise<DiscoverBoard> {
  return request<DiscoverBoard>('/api/discover')
}

export function previewShow(feedUrl: string): Promise<ShowPreview> {
  return request<ShowPreview>('/api/discover/show', jsonInit('POST', { feed_url: feedUrl }))
}

export function importOPML(file: File): Promise<OPMLResult> {
  const form = new FormData()
  form.append('file', file)
  return request<OPMLResult>('/api/opml', { method: 'POST', body: form })
}

export function audioURL(episodeId: number): string {
  return `/api/episodes/${episodeId}/audio`
}

export function getDownloads(): Promise<Download[]> {
  return request<{ downloads: Download[] }>('/api/downloads').then(
    (value) => value.downloads ?? [],
  )
}

export function downloadEpisode(episodeId: number): Promise<Download> {
  return request<{ download: Download }>(`/api/episodes/${episodeId}/download`, {
    method: 'POST',
  }).then((value) => value.download)
}

export function removeDownloads(ids: number[]): Promise<{ removed: number }> {
  return request('/api/downloads/remove', jsonInit('POST', { ids }))
}

export interface AIConfigInput {
  provider: string
  base_url: string
  model: string
  api_key: string
}

export function saveAISetting(kind: 'text' | 'speech', input: AIConfigInput): Promise<AIProviderStatus> {
  return request<{ config: AIProviderStatus }>(`/api/ai/settings/${kind}`, jsonInit('PUT', input)).then(
    (value) => value.config,
  )
}

export function testAISetting(kind: 'text' | 'speech'): Promise<AITestResult> {
  return request<AITestResult>(`/api/ai/settings/${kind}/test`, { method: 'POST' })
}

export function deleteAISetting(kind: 'text' | 'speech'): Promise<void> {
  return request<unknown>(`/api/ai/settings/${kind}`, { method: 'DELETE' }).then(() => undefined)
}

export function getEpisodeAI(episodeId: number): Promise<EpisodeAI> {
  return request<EpisodeAI>(`/api/episodes/${episodeId}/ai`)
}

export function generateInsight(
  episodeId: number,
  kind: 'summary' | 'analysis' | 'map',
): Promise<AITask> {
  return request<{ task: AITask }>(`/api/episodes/${episodeId}/insights/${kind}`, {
    method: 'POST',
  }).then((value) => value.task)
}

export function translateTranscript(episodeId: number, targetLanguage: string): Promise<AITask> {
  return request<{ task: AITask }>(`/api/episodes/${episodeId}/translation`, jsonInit('POST', {
    target_language: targetLanguage,
  })).then((value) => value.task)
}

export function transcribeEpisode(episodeId: number): Promise<AITask> {
  return request<{ task: AITask }>(`/api/episodes/${episodeId}/transcription`, {
    method: 'POST',
  }).then((value) => value.task)
}

export function askEpisode(episodeId: number, question: string): Promise<AITask> {
  return request<{ task: AITask }>(`/api/episodes/${episodeId}/ask`, jsonInit('POST', {
    question,
  })).then((value) => value.task)
}

export function retryTask(taskId: string): Promise<AITask> {
  return request<{ task: AITask }>(`/api/tasks/${encodeURIComponent(taskId)}/retry`, {
    method: 'POST',
  }).then((value) => value.task)
}
