export interface Category {
  id: number
  name: string
  color: string
  feed_count: number
}

export interface Feed {
  id: number
  url: string
  site_url: string
  title: string
  description: string
  image_url: string
  last_checked_at: number
  last_success_at: number
  last_error: string
  consecutive_failures: number
  episode_count: number
  new_count: number
  categories: Category[]
}

export interface Chapter {
  start: number
  label: string
}

export interface Episode {
  id: number
  feed_id: number
  feed_title: string
  feed_image_url: string
  feed_site_url: string
  feed_url: string
  guid: string
  title: string
  description?: string
  link: string
  audio_type: string
  image_url: string
  duration_seconds: number
  published_at: number
  inbox_state: 'new' | 'started' | 'completed' | 'archived'
  has_transcript: boolean
  transcript_refs_available: boolean
  chapters: Chapter[]
  progress_seconds: number
  progress_duration: number
  completed: boolean
  progress_updated_at: number
  categories: Category[]
}

export interface Download {
  episode_id: number
  feed_id: number
  title: string
  link: string
  feed_title: string
  feed_site_url: string
  image_url: string
  feed_image_url: string
  duration_seconds: number
  published_at: number
  content_type: string
  size_bytes: number
  created_at: number
}

export interface Progress {
  position_seconds: number
  duration_seconds: number
  completed: boolean
  updated_at: number
}

export interface Segment {
  index: number
  start: number
  end: number
  speaker: string
  text: string
  translation: string
}

export interface Transcript {
  source: string
  language: string
  translated_language: string
  timed: boolean
  segments: Segment[]
}

export interface SearchResult {
  kind: 'feed' | 'episode' | 'transcript'
  feed_id?: number
  episode_id?: number
  title: string
  feed_title?: string
  image_url?: string
  snippet?: string
  time_seconds?: number
}

export interface DirectoryItem {
  name: string
  artist: string
  feed_url: string
  artwork_url?: string
  episode_count: number
  source: string
}

export interface DiscoverShow {
  rank: number
  name: string
  artist: string
  feed_url: string
  artwork_url?: string
  episode_count: number
  source: string
}

export interface DiscoverEpisode {
  rank: number
  title: string
  show_title: string
  feed_url: string
  artwork_url?: string
  published_at: number
  source: string
}

export interface DiscoverBoard {
  country: string
  source: string
  episode_source: string
  english_source: string
  english_error?: string
  english_episode_error?: string
  updated_at: number
  shows: DiscoverShow[]
  english_tech: DiscoverShow[]
  english_business: DiscoverShow[]
  english_tech_episodes: DiscoverEpisode[]
  english_business_episodes: DiscoverEpisode[]
  episodes: DiscoverEpisode[]
}

export interface ShowPreview {
  feed: Feed
  episodes: Episode[]
  subscribed: boolean
}

export interface Resolution {
  input: string
  kind: 'feed' | 'show' | 'episode' | 'unsupported' | 'error'
  status: 'subscribable' | 'needs_feed' | 'unsupported' | 'error'
  feed_url?: string
  title?: string
  site_url?: string
  source?: string
  message: string
}

export interface LibraryStatus {
  feeds: number
  episodes: number
  new_episodes: number
  in_progress: number
  last_updated: number
}

export interface AIProviderStatus {
  configured: boolean
  provider: string
  base_url: string
  model: string
  key_set: boolean
  updated_at: number
}

export interface AIStatus {
  configured: boolean
  text: AIProviderStatus
  speech: AIProviderStatus
  secret_ready: boolean
  message: string
}

export interface StatusResponse {
  version: string
  library: LibraryStatus
  ai: AIStatus
}

export interface AITestResult {
  ok: boolean
  message: string
  disclosure: string
}

export type TaskStatus = 'queued' | 'processing' | 'failed' | 'completed'

export interface AITask {
  id: string
  episode_id: number
  kind: 'summary' | 'analysis' | 'map' | 'translation' | 'transcription' | 'ask'
  status: TaskStatus
  provider: string
  error: string
  progress: number
  payload: string
  attempts: number
  created_at: number
  updated_at: number
  started_at: number
  finished_at: number
  sends: 'text' | 'audio'
  cost_notice: string
}

export interface AICitation {
  segment: number
  start: number
  quote: string
}

export interface AISummaryPayload {
  overview: string
  chapters: { start: number; label: string }[]
  points: { text: string; citations: AICitation[] }[]
  quotes: AICitation[]
}

export interface AIAnalysisPayload {
  title: string
  summary: string
  sections: { heading: string; body: string; citations: AICitation[] }[]
  takeaways: { text: string; citations: AICitation[] }[]
}

export interface AIMapPayload {
  title: string
  branches: { title: string; children: { title: string; citations: AICitation[] }[] }[]
}

export interface AIInsight {
  kind: 'summary' | 'analysis' | 'map' | string
  payload: AISummaryPayload | AIAnalysisPayload | AIMapPayload | Record<string, unknown>
  model: string
  source: string
  created_at: number
  updated_at: number
}

export interface AIQuestion {
  id: number
  question: string
  answer: string
  citations: AICitation[]
  grounded: boolean
  model: string
  created_at: number
  cost_notice: string
}

export interface EpisodeAI {
  insights: AIInsight[]
  questions: AIQuestion[]
  tasks: AITask[]
}

export interface EpisodeDetail {
  episode: Episode
}

export interface OPMLItemResult {
  title: string
  feed_url: string
  status: 'added' | 'existing' | 'failed' | 'skipped'
  message?: string
}

export interface OPMLResult {
  added: number
  existing: number
  failed: number
  skipped: number
  items: OPMLItemResult[]
  message: string
}
