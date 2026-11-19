import { useEffect, useRef, useState } from 'react'
import type { AIStatus, EpisodeAI, EpisodeDetail, Transcript } from '../types'
import type { ReaderTab, TranscriptState } from '../ui'
import { clock, durationLabel, episodeDate, stripHTML } from '../format'
import { Cover } from './Cover'
import { Icon } from './Icons'
import {
  AnalysisAI,
  AskAI,
  MapAI,
  SummaryAI,
  TranscriptionAction,
  TranslationAction,
  type AIActions,
} from './AI'

const TABS: { key: ReaderTab; label: string }[] = [
  { key: 'summary', label: '导读' },
  { key: 'transcript', label: '逐字稿' },
  { key: 'article', label: '精读' },
  { key: 'map', label: '导图' },
  { key: 'ask', label: '问这集' },
]

type CaptionMode = 'bilingual' | 'original' | 'translation'

interface ReaderProps {
  detail: EpisodeDetail | null
  loading: boolean
  transcript: Transcript | null
  transcriptState: TranscriptState
  transcriptError: string
  tab: ReaderTab
  playingId: number | null
  playing: boolean
  position: number
  onTab: (tab: ReaderTab) => void
  onPlay: () => void
  onSeek: (seconds: number, reveal?: ReaderTab) => void
  onOpenFeed: () => void
  onOpenExport: () => void
  onOpenSettings: () => void
  onLoadTranscript: () => void
  ai: EpisodeAI | null
  aiStatus: AIStatus | undefined
  aiLoading: boolean
  onGenerateInsight: (kind: 'summary' | 'analysis' | 'map') => void
  onTranslate: (targetLanguage: string) => void
  onTranscribe: () => void
  onAsk: (question: string) => void
  onRetryTask: (taskId: string) => void
  onBack: () => void
  showBack: boolean
}

function currentIndexOf(transcript: Transcript | null, position: number): number {
  if (!transcript) return -1
  let found = -1
  for (const segment of transcript.segments) {
    if (segment.start > position) break
    found = segment.index
  }
  return found
}


export function Reader(props: ReaderProps) {
  const [captionMode, setCaptionMode] = useState<CaptionMode>('original')
  const [follow, setFollow] = useState(true)
  const [query, setQuery] = useState('')
  const transcriptRef = useRef<HTMLDivElement>(null)

  const { detail, transcript, transcriptState, tab, playingId, playing, position } = props
  const episode = detail?.episode ?? null
  const aiActions: AIActions = {
    onGenerate: props.onGenerateInsight,
    onTranslate: props.onTranslate,
    onTranscribe: props.onTranscribe,
    onAsk: props.onAsk,
    onRetry: props.onRetryTask,
    onOpenSettings: props.onOpenSettings,
    onSeek: (seconds) => props.onSeek(seconds),
    onOpenTranscript: () => props.onTab('transcript'),
  }
  const aiProps = {
    ai: props.ai,
    status: props.aiStatus,
    loading: props.aiLoading,
    actions: aiActions,
  }
  const isCurrent = Boolean(episode && playingId === episode.id)
  const currentIndex = isCurrent && transcript ? currentIndexOf(transcript, position) : -1

  const hasOriginal = Boolean(transcript?.segments.some((segment) => segment.text))
  const hasTranslation = Boolean(transcript?.segments.some((segment) => segment.translation))
  const effectiveMode: CaptionMode = hasOriginal && hasTranslation
    ? captionMode
    : hasTranslation
      ? 'translation'
      : 'original'

  useEffect(() => {
    if (!transcript) return
    setCaptionMode(transcript.segments.some((segment) => segment.translation) ? 'bilingual' : 'original')
    setQuery('')
    setFollow(true)
  }, [transcript])

  useEffect(() => {
    if (!follow || query || tab !== 'transcript' || currentIndex < 0) return
    const container = transcriptRef.current
    const row = container?.querySelector<HTMLElement>(`[data-seg="${currentIndex}"]`)
    if (!container || !row) return
    const containerRect = container.getBoundingClientRect()
    const rowRect = row.getBoundingClientRect()
    if (rowRect.top < containerRect.top || rowRect.bottom > containerRect.bottom) {
      container.scrollTop += rowRect.top - containerRect.top - containerRect.height / 3
    }
  }, [currentIndex, follow, query, tab])

  if (!detail || !episode) {
    return (
      <main className="reader" aria-label="单集详情" data-back={props.showBack ? 'true' : undefined}>
        <div className="reader-inner">
          <div className="reader-top">
            <button className="quiet back" type="button" onClick={props.onBack}>
              <Icon name="back" />
              返回列表
            </button>
            <div className="breadcrumb">
              <span>单集</span>
            </div>
            <span className="demo-tag">RSS 单集</span>
          </div>
          <p className="empty">{props.loading ? '正在载入单集…' : '从左侧选择一集开始。'}</p>
        </div>
      </main>
    )
  }

  const description = stripHTML(episode.description ?? '')
  const savedPosition = isCurrent ? position : episode.progress_seconds
  const canResume = savedPosition > 0 && !episode.completed && savedPosition < (episode.duration_seconds || Infinity)
  const playLabel = isCurrent && playing ? '暂停' : canResume ? '继续播放' : '播放'
  const currentSegment = transcript?.segments.find((segment) => segment.index === currentIndex) ?? null
  let activeChapter = -1
  if (isCurrent) {
    for (const chapter of episode.chapters) {
      if (chapter.start <= position) activeChapter = chapter.start
    }
  }
  const filteredSegments = transcript
    ? transcript.segments.filter((segment) => {
        const term = query.trim().toLowerCase()
        if (!term) return true
        return `${segment.speaker} ${segment.text} ${segment.translation}`.toLowerCase().includes(term)
      })
    : []

  return (
    <main className="reader" aria-label="单集详情" data-back={props.showBack ? 'true' : undefined}>
      <div className="reader-inner">
        <div className="reader-top">
          <button className="quiet back" type="button" onClick={props.onBack}>
            <Icon name="back" />
            返回列表
          </button>
          <div className="breadcrumb">
            <button
              type="button"
              className="crumb-link"
              onClick={() => props.onOpenFeed()}
            >
              {episode.feed_title}
            </button>
          </div>
          <span className="demo-tag">RSS 单集</span>
        </div>

        <div className="hero">
          <button
            type="button"
            className="cover-link"
            onClick={() => props.onOpenFeed()}
            aria-label={`打开节目 ${episode.feed_title}`}
          >
            <Cover imageURL={episode.image_url || episode.feed_image_url} title={episode.feed_title} large />
          </button>
          <div className="hero-copy">
            <div className="eyebrow">
              <button
                type="button"
                className="eyebrow-link"
                onClick={() => props.onOpenFeed()}
              >
                {episode.feed_title}
              </button>
              {' / 单集'}
            </div>
            <h2>{episode.title}</h2>
            <div className="hero-meta">
              <span>{episodeDate(episode.published_at)}</span>
              <span>·</span>
              <span>{durationLabel(episode.duration_seconds)}</span>
            </div>
            <div className="hero-actions">
              <button type="button" className="primary" onClick={props.onPlay}>
                <Icon name={isCurrent && playing ? 'pause' : 'play'} />
                {playLabel}
              </button>
            </div>
          </div>
        </div>

        <nav className="tab-bar" role="tablist" aria-label="单集内容">
          {TABS.map((item) => (
            <button
              key={item.key}
              type="button"
              className="tab"
              role="tab"
              aria-selected={tab === item.key}
              onClick={() => props.onTab(item.key)}
            >
              {item.label}
            </button>
          ))}
        </nav>

        <div role="tabpanel" aria-label={TABS.find((item) => item.key === tab)?.label}>
          {tab === 'summary' ? (
            <>
              <div className="overview">
                <section>
                  <div className="section-top">
                    <h3>节目简介</h3>
                    <button type="button" className="quiet" onClick={props.onOpenExport}>
                      <Icon name="export" />
                      导出
                    </button>
                  </div>
                  <p className="source-label">来自节目 RSS · 先展示来源内容</p>
                  {description ? (
                    <p className="summary-text">{description}</p>
                  ) : (
                    <p className="muted">节目源没有提供简介。</p>
                  )}
                  <SummaryAI
                    {...aiProps}
                    insight={props.ai?.insights.find((item) => item.kind === 'summary') ?? null}
                  />
                </section>
                <aside className="chapter-panel">
                  <header>
                    <h3>章节</h3>
                    <small>{episode.chapters.length ? `${episode.chapters.length} 个时间点` : '节目源未提供'}</small>
                  </header>
                  {episode.chapters.length > 0 ? (
                    <>
                      <button
                        type="button"
                        className="quiet chapter-hint"
                        onClick={() => props.onSeek(episode.chapters[0].start)}
                      >
                        <Icon name="play" />
                        从第一段开始 · {clock(episode.chapters[0].start)}
                      </button>
                      {episode.chapters.map((chapter) => (
                        <button
                          key={`${chapter.start}-${chapter.label}`}
                          className={`chapter${chapter.start === activeChapter ? ' active' : ''}`}
                          type="button"
                          onClick={() => props.onSeek(chapter.start)}
                        >
                          <time>{clock(chapter.start)}</time>
                          <span>{chapter.label}</span>
                        </button>
                      ))}
                    </>
                  ) : (
                    <p className="chapter-hint">
                      节目源没有带时间的章节。配置 AI 后，导读会给出带时间点的章节结构。
                    </p>
                  )}
                </aside>
              </div>
            </>
          ) : null}

          {tab === 'transcript' ? (
            <section className="text-panel">
              <div className="section-top">
                <h3>逐字稿</h3>
                <div className="source-tools">
                  {transcript ? (
                    <>
                      <button
                        type="button"
                        className={`quiet${follow ? ' follow-active' : ''}`}
                        onClick={() => setFollow(!follow)}
                        aria-pressed={follow}
                      >
                        {follow ? '正在跟读' : '回到当前句'}
                      </button>
                      <button type="button" className="quiet" onClick={props.onOpenExport}>
                        <Icon name="export" />
                        导出
                      </button>
                    </>
                  ) : null}
                </div>
              </div>
              {transcriptState === 'loading' ? <p className="muted">正在读取逐字稿…</p> : null}
              {transcriptState === 'error' ? (
                <>
                  <p className="muted">获取逐字稿失败：{props.transcriptError}</p>
                  {episode.transcript_refs_available ? (
                    <button type="button" className="outline" onClick={props.onLoadTranscript}>
                      重试
                    </button>
                  ) : null}
                </>
              ) : null}
              {transcriptState !== 'loading' && transcriptState !== 'error' && !transcript ? (
                <div className="guide-more">
                  <TranscriptionAction {...aiProps} sourcesAvailable={episode.transcript_refs_available} />
                  {episode.transcript_refs_available ? (
                    <div className="feature-options">
                      <button type="button" className="outline" onClick={props.onLoadTranscript}>
                        <Icon name="cc" />
                        获取节目提供的逐字稿
                      </button>
                    </div>
                  ) : null}
                </div>
              ) : null}
              {transcript ? (
                <>
                  {isCurrent && currentSegment ? (
                    <div className="live-caption">
                      <span>当前句 · {clock(currentSegment.start)}</span>
                      <p>
                        {effectiveMode === 'translation'
                          ? currentSegment.translation || currentSegment.text
                          : currentSegment.text}
                      </p>
                      {effectiveMode === 'bilingual' && currentSegment.translation ? (
                        <p className="translation" lang={transcript.translated_language || undefined}>
                          {currentSegment.translation}
                        </p>
                      ) : null}
                    </div>
                  ) : null}
                  <div className="reader-tools">
                    {hasTranslation && hasOriginal ? (
                      <div className="caption-options" aria-label="字幕模式">
                        {([
                          ['bilingual', '双语'],
                          ['original', '原文'],
                          ['translation', '译文'],
                        ] as [CaptionMode, string][]).map(([key, label]) => (
                          <button
                            key={key}
                            type="button"
                            className="chip"
                            data-caption={key}
                            aria-pressed={effectiveMode === key}
                            onClick={() => setCaptionMode(key)}
                          >
                            {label}
                          </button>
                        ))}
                      </div>
                    ) : null}
                    <label className="sr-only" htmlFor="transcript-search">
                      搜索逐字稿
                    </label>
                    <input
                      id="transcript-search"
                      type="search"
                      placeholder="搜索本集逐字稿…"
                      value={query}
                      onChange={(event) => setQuery(event.target.value)}
                    />
                  </div>
                  <p className="source-label">
                    逐字稿 · {transcript.source || '节目提供'}
                    {transcript.language ? ` · ${transcript.language}` : ''}
                    {transcript.timed ? ' · 点击一行跳到原声时间' : ' · 没有时间戳'}
                  </p>
                  {!hasTranslation ? <TranslationAction {...aiProps} transcript={transcript} /> : null}
                  <div
                    className="transcript-scroll"
                    id="transcript-lines"
                    ref={transcriptRef}
                    onWheel={() => setFollow(false)}
                    onTouchMove={() => setFollow(false)}
                  >
                    {filteredSegments.map((segment) => (
                      <button
                        key={segment.index}
                        className={`transcript-row${segment.index === currentIndex ? ' active' : ''}`}
                        type="button"
                        data-seg={segment.index}
                        onClick={() => props.onSeek(segment.start, 'transcript')}
                      >
                        <time>{transcript.timed ? clock(segment.start) : '—'}</time>
                        <span>
                          {segment.speaker ? <strong>{segment.speaker}</strong> : null}
                          {effectiveMode !== 'translation' && segment.text ? segment.text : null}
                          {effectiveMode !== 'original' && segment.translation ? (
                            <span className="translation" lang={transcript.translated_language || undefined}>
                              {segment.translation}
                            </span>
                          ) : null}
                        </span>
                      </button>
                    ))}
                    {filteredSegments.length === 0 ? <p className="empty">没有匹配的段落。</p> : null}
                  </div>
                </>
              ) : null}
            </section>
          ) : null}

          {tab === 'article' ? (
            <AnalysisAI
              {...aiProps}
              insight={props.ai?.insights.find((item) => item.kind === 'analysis') ?? null}
            />
          ) : null}

          {tab === 'map' ? (
            <MapAI
              {...aiProps}
              insight={props.ai?.insights.find((item) => item.kind === 'map') ?? null}
            />
          ) : null}

          {tab === 'ask' ? <AskAI {...aiProps} /> : null}
        </div>
      </div>
    </main>
  )
}
