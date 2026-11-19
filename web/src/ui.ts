export type Mode = 'inbox' | 'shows' | 'downloads' | 'discover'
export type Filter = 'all' | 'new' | 'in_progress' | `cat:${number}`
export type DialogName =
  | 'category'
  | 'tags'
  | 'settings'
  | 'export'
  | null
export type ReaderTab = 'summary' | 'transcript' | 'article' | 'map' | 'ask'
export type TranscriptState = 'idle' | 'loading' | 'ready' | 'missing' | 'error'
