PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS feeds (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    owner TEXT NOT NULL,
    url TEXT NOT NULL,
    site_url TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    image_url TEXT NOT NULL DEFAULT '',
    etag TEXT NOT NULL DEFAULT '',
    last_modified TEXT NOT NULL DEFAULT '',
    last_checked_at INTEGER NOT NULL DEFAULT 0,
    last_success_at INTEGER NOT NULL DEFAULT 0,
    next_check_at INTEGER NOT NULL DEFAULT 0,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    hidden INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (owner, url)
);
CREATE INDEX IF NOT EXISTS idx_feeds_due ON feeds (next_check_at);
CREATE INDEX IF NOT EXISTS idx_feeds_owner ON feeds (owner, title);

CREATE TABLE IF NOT EXISTS episodes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    owner TEXT NOT NULL,
    feed_id INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
    guid TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    link TEXT NOT NULL DEFAULT '',
    audio_url TEXT NOT NULL,
    audio_type TEXT NOT NULL DEFAULT '',
    image_url TEXT NOT NULL DEFAULT '',
    duration_seconds INTEGER NOT NULL DEFAULT 0,
    published_at INTEGER NOT NULL DEFAULT 0,
    inbox_state TEXT NOT NULL DEFAULT 'new',
    has_transcript INTEGER NOT NULL DEFAULT 0,
    transcript_refs TEXT NOT NULL DEFAULT '[]',
    chapters TEXT NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (feed_id, guid)
);
CREATE INDEX IF NOT EXISTS idx_episodes_owner_pub ON episodes (owner, published_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_episodes_feed ON episodes (feed_id, published_at DESC);

CREATE TABLE IF NOT EXISTS downloads (
    owner TEXT NOT NULL,
    episode_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    file_name TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (owner, episode_id)
);
CREATE INDEX IF NOT EXISTS idx_downloads_owner_created ON downloads (owner, created_at DESC);

CREATE TABLE IF NOT EXISTS progress (
    owner TEXT NOT NULL,
    episode_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    position_seconds REAL NOT NULL DEFAULT 0,
    duration_seconds REAL NOT NULL DEFAULT 0,
    completed INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (owner, episode_id)
);

CREATE TABLE IF NOT EXISTS categories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    owner TEXT NOT NULL,
    name TEXT NOT NULL COLLATE NOCASE,
    color TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    UNIQUE (owner, name)
);

CREATE TABLE IF NOT EXISTS feed_categories (
    owner TEXT NOT NULL,
    feed_id INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
    category_id INTEGER NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    PRIMARY KEY (feed_id, category_id)
);
CREATE INDEX IF NOT EXISTS idx_feed_categories_category ON feed_categories (category_id);

CREATE TABLE IF NOT EXISTS transcripts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    owner TEXT NOT NULL,
    episode_id INTEGER NOT NULL UNIQUE REFERENCES episodes(id) ON DELETE CASCADE,
    source TEXT NOT NULL DEFAULT 'feed',
    language TEXT NOT NULL DEFAULT '',
    translated_language TEXT NOT NULL DEFAULT '',
    timed INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS transcript_segments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    transcript_id INTEGER NOT NULL REFERENCES transcripts(id) ON DELETE CASCADE,
    owner TEXT NOT NULL,
    episode_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    idx INTEGER NOT NULL,
    start_seconds REAL NOT NULL DEFAULT 0,
    end_seconds REAL NOT NULL DEFAULT 0,
    speaker TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL,
    translation TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_transcript_segments_episode ON transcript_segments (episode_id, start_seconds);

CREATE TABLE IF NOT EXISTS insights (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    owner TEXT NOT NULL,
    episode_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    payload TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT 'ai',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (episode_id, kind)
);

CREATE TABLE IF NOT EXISTS qa (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    owner TEXT NOT NULL,
    episode_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    question TEXT NOT NULL,
    answer TEXT NOT NULL,
    citations TEXT NOT NULL DEFAULT '[]',
    grounded INTEGER NOT NULL DEFAULT 0,
    model TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_qa_episode ON qa (owner, episode_id, created_at DESC);

CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    owner TEXT NOT NULL,
    episode_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    provider TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    progress REAL NOT NULL DEFAULT 0,
    payload TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    started_at INTEGER NOT NULL DEFAULT 0,
    finished_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks (status, created_at);
CREATE INDEX IF NOT EXISTS idx_tasks_episode ON tasks (owner, episode_id, created_at DESC);

CREATE TABLE IF NOT EXISTS ai_configs (
    owner TEXT NOT NULL,
    kind TEXT NOT NULL,
    provider TEXT NOT NULL,
    base_url TEXT NOT NULL,
    model TEXT NOT NULL,
    api_key_enc TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (owner, kind)
);

CREATE UNIQUE INDEX IF NOT EXISTS tasks_active_unique
    ON tasks(owner, episode_id, kind, payload)
    WHERE status IN ('queued', 'processing');
