import { useEffect, useRef, useState } from 'react'
import type {
  Category,
  DirectoryItem,
  DiscoverBoard,
  DiscoverEpisode,
  DiscoverShow,
  Feed,
  OPMLResult,
  Resolution,
} from '../types'
import type { ShowTarget } from './ShowDetail'
import * as api from '../api'
import { episodeDate } from '../format'
import { Cover } from './Cover'
import { Icon } from './Icons'

interface DiscoverProps {
  feeds: Feed[]
  categories: Category[]
  focusToken: number
  onMode: (mode: 'inbox' | 'shows' | 'downloads' | 'discover') => void
  onSubscribed: (feed: Feed, newEpisodes: number) => void
  onImported: () => void
  onOpenShow: (target: ShowTarget) => void
  onOpenEpisode: (item: DiscoverEpisode) => void
  notify: (message: string) => void
}

function isLink(value: string): boolean {
  return /^https?:\/\//i.test(value.trim())
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : '操作失败，请稍后重试'
}

export function Discover(props: DiscoverProps) {
  const [query, setQuery] = useState('')
  const [searching, setSearching] = useState(false)
  const [directory, setDirectory] = useState<DirectoryItem[]>([])
  const [board, setBoard] = useState<DiscoverBoard | null>(null)
  const [boardError, setBoardError] = useState('')
  const [boardLoading, setBoardLoading] = useState(true)
  const [resolution, setResolution] = useState<Resolution | null>(null)
  const [resolving, setResolving] = useState(false)
  const [selectedCategories, setSelectedCategories] = useState<number[]>([])
  const [subscribing, setSubscribing] = useState('')
  const [message, setMessage] = useState('')
  const [opml, setOpml] = useState<OPMLResult | null>(null)
  const [importing, setImporting] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const fileRef = useRef<HTMLInputElement>(null)
  const linkMode = isLink(query)
  const opmlItems = opml
    ? [...opml.items].sort(
        (left, right) => Number(right.status === 'failed') - Number(left.status === 'failed'),
      )
    : []

  useEffect(() => {
    inputRef.current?.focus()
  }, [props.focusToken])

  useEffect(() => {
    let cancelled = false
    setBoardLoading(true)
    api
      .getDiscover()
      .then((value) => {
        if (!cancelled) {
          setBoard(value)
          setBoardError('')
        }
      })
      .catch((error) => {
        if (!cancelled) setBoardError(messageOf(error))
      })
      .finally(() => {
        if (!cancelled) setBoardLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    const value = query.trim()
    if (!value || isLink(value)) {
      setDirectory([])
      setSearching(false)
      setResolution(null)
      return
    }
    let cancelled = false
    setSearching(true)
    const timer = window.setTimeout(() => {
      api
        .searchDirectory(value)
        .then((items) => {
          if (!cancelled) setDirectory(items)
        })
        .catch((error) => {
          if (!cancelled) {
            setDirectory([])
            setMessage(messageOf(error))
          }
        })
        .finally(() => {
          if (!cancelled) setSearching(false)
        })
    }, 280)
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [query])

  async function handleSubmit(event: React.FormEvent) {
    event.preventDefault()
    const value = query.trim()
    if (!value) return
    if (!isLink(value)) {
      setSearching(true)
      setMessage('')
      try {
        setDirectory(await api.searchDirectory(value))
      } catch (error) {
        setMessage(messageOf(error))
      } finally {
        setSearching(false)
      }
      return
    }
    setResolving(true)
    setMessage('')
    setResolution(null)
    try {
      setResolution(await api.resolveLink(value))
    } catch (error) {
      setMessage(messageOf(error))
    } finally {
      setResolving(false)
    }
  }

  async function subscribeTo(input: string, feedUrl: string) {
    setSubscribing(feedUrl)
    setMessage('')
    try {
      const result = await api.subscribe(input, feedUrl, selectedCategories)
      setSubscribing('')
      setSelectedCategories([])
      setResolution(null)
      setQuery('')
      props.onSubscribed(result.feed, result.new_episodes)
    } catch (error) {
      setMessage(messageOf(error))
      setSubscribing('')
    }
  }

  async function handleFile(event: React.ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (!file || importing) return
    if (file.size > 2 * 1024 * 1024) {
      setMessage('OPML 文件不能超过 2 MB')
      return
    }
    setMessage('')
    setImporting(true)
    try {
      const result = await api.importOPML(file)
      setOpml(result)
      props.onImported()
      props.notify(result.message)
    } catch (error) {
      setMessage(messageOf(error))
    } finally {
      setImporting(false)
    }
  }

  function subscribed(feedUrl: string): boolean {
    return props.feeds.some((feed) => feed.url === feedUrl)
  }

  function showTarget(item: DiscoverShow): ShowTarget {
    return {
      feedUrl: item.feed_url,
      title: item.name,
      artist: item.artist,
      artwork: item.artwork_url,
      source: item.source === 'itunes_us' ? 'Apple Podcasts 美国区榜单' : 'Apple Podcasts 热门榜',
    }
  }

  function renderShows(shows: DiscoverShow[]) {
    return (
      <div className="discover-show-grid">
        {shows.map((item) => {
          const already = subscribed(item.feed_url)
          return (
            <button
              type="button"
              className="discover-show discover-show-link"
              key={`${item.source}-${item.rank}-${item.feed_url}`}
              onClick={() => props.onOpenShow(showTarget(item))}
            >
              <span className="rank">{item.rank}</span>
              <Cover imageURL={item.artwork_url ?? ''} title={item.name} />
              <span className="discover-show-copy">
                <strong>{item.name}</strong>
                <span>{item.artist || '未知主播'}</span>
                <small>
                  {already ? '已订阅 · ' : ''}
                  {item.episode_count ? `${item.episode_count} 集` : '热门榜'}
                </small>
              </span>
              <Icon name="chevron" />
            </button>
          )
        })}
      </div>
    )
  }

  function renderEpisodes(episodes: DiscoverEpisode[]) {
    return (
      <div className="discover-episodes">
        {episodes.map((item) => (
          <button
            type="button"
            className="discover-episode discover-episode-link"
            key={`${item.source}-${item.rank}-${item.title}`}
            onClick={() => props.onOpenEpisode(item)}
          >
            <Cover imageURL={item.artwork_url ?? ''} title={item.show_title} />
            <span className="discover-episode-copy">
              <span className="row-show">{item.show_title}</span>
              <strong>{item.title}</strong>
              <small>
                {episodeDate(item.published_at)}
                <span>·</span>节目榜第 {item.rank} 位
              </small>
            </span>
            <Icon name="chevron" />
          </button>
        ))}
      </div>
    )
  }

  return (
    <section className="discover" aria-labelledby="discover-title">
      <header className="discover-header">
        <div className="eyebrow">LIBRARY / 04</div>
        <div className="discover-title-row">
          <div>
            <h1 id="discover-title">订阅播客</h1>
            <p>搜索公开目录，或粘贴 RSS、Apple Podcasts 与分享链接。</p>
          </div>
          <div className="chip-bar discover-mobile-nav">
            <button className="chip" type="button" onClick={() => props.onMode('inbox')}>
              收件箱 ↗
            </button>
            <button className="chip" type="button" onClick={() => props.onMode('shows')}>
              我的订阅 ↗
            </button>
            <button className="chip" type="button" onClick={() => props.onMode('downloads')}>
              下载 ↗
            </button>
          </div>
        </div>
        <form className="discover-search" onSubmit={handleSubmit}>
          <Icon name="search" />
          <label className="sr-only" htmlFor="discover-search">
            搜索播客或粘贴链接
          </label>
          <input
            ref={inputRef}
            id="discover-search"
            type="search"
            placeholder="搜播客、主播，或粘贴 RSS／Apple Podcasts 链接"
            autoComplete="off"
            value={query}
            onChange={(event) => {
              setQuery(event.target.value)
              setMessage('')
            }}
          />
          <button type="submit" className="primary" disabled={resolving || !query.trim()}>
            {linkMode ? (resolving ? '识别中…' : '识别链接') : '搜索'}
          </button>
        </form>
        {message ? (
          <p className="discover-message" role="status">
            {message}
          </p>
        ) : null}
      </header>

      <section className="discover-opml">
        <div>
          <strong>从其他播客应用迁移</strong>
          <p>导入 OPML 文件，已存在的订阅会自动跳过。</p>
        </div>
        <button
          type="button"
          className="outline"
          disabled={importing}
          onClick={() => fileRef.current?.click()}
        >
          <Icon name="plus" />
          {importing ? '正在导入…' : '导入 OPML'}
        </button>
        <input
          ref={fileRef}
          type="file"
          accept=".opml,.xml,text/x-opml,text/xml,application/xml"
          hidden
          onChange={handleFile}
        />
        {opml ? (
          <div className="discover-opml-result" role="status">
            <strong>{opml.message}</strong>
            <ul className="discover-opml-items">
              {opmlItems.map((item) => (
                <li key={`${item.feed_url}-${item.status}`}>
                  {item.title || item.feed_url} ·{' '}
                  {item.status === 'added'
                    ? '已添加'
                    : item.status === 'existing'
                      ? '已存在'
                      : item.status === 'failed'
                        ? '失败'
                        : '跳过'}
                  {item.message ? ` · ${item.message}` : ''}
                </li>
              ))}
            </ul>
          </div>
        ) : null}
      </section>

      {linkMode ? (
        <section className="discover-panel">
          {resolution ? (
            <div className="source-preview">
              <strong>{resolution.title || resolution.feed_url || resolution.input}</strong>
              <span role="status">{resolution.message}</span>
              {resolution.status === 'subscribable' && resolution.feed_url ? (
                <>
                  {props.categories.length > 0 ? (
                    <div className="discover-categories">
                      <span>加入分类（可选）</span>
                      {props.categories.map((category) => (
                        <label key={category.id} className="chip">
                          <input
                            type="checkbox"
                            checked={selectedCategories.includes(category.id)}
                            onChange={(event) =>
                              setSelectedCategories((current) =>
                                event.target.checked
                                  ? [...current, category.id]
                                  : current.filter((id) => id !== category.id),
                              )
                            }
                          />
                          {category.name}
                        </label>
                      ))}
                    </div>
                  ) : null}
                  <button
                    type="button"
                    className="primary"
                    disabled={Boolean(subscribing)}
                    onClick={() => void subscribeTo(resolution.input, resolution.feed_url ?? '')}
                  >
                    {subscribing ? '订阅中…' : '订阅此节目'}
                  </button>
                </>
              ) : null}
              {resolution.site_url ? (
                <a className="source" href={resolution.site_url} target="_blank" rel="noreferrer noopener">
                  打开节目主页
                </a>
              ) : null}
            </div>
          ) : (
            <p className="muted">识别后会告诉你这个链接能否直接订阅；无法确认的链接不会被假称已订阅。</p>
          )}
        </section>
      ) : query.trim() ? (
        <section className="discover-panel">
          <div className="discover-section-head">
            <h2>公开目录</h2>
            <span>Apple Podcasts 搜索 · 中国区</span>
          </div>
          {searching ? <p className="muted">正在搜索…</p> : null}
          {!searching && directory.length === 0 ? (
            <p className="muted">没有找到匹配的播客。可以换一个关键词，或直接粘贴 RSS 链接。</p>
          ) : null}
          <div className="discover-show-grid">
            {directory.map((item) => {
              const already = subscribed(item.feed_url)
              return (
                <button
                  type="button"
                  className="discover-show discover-show-link"
                  key={`${item.source}-${item.feed_url}`}
                  onClick={() =>
                    props.onOpenShow({
                      feedUrl: item.feed_url,
                      title: item.name,
                      artist: item.artist,
                      artwork: item.artwork_url,
                      source: 'Apple Podcasts 公开目录',
                    })
                  }
                >
                  <Cover imageURL={item.artwork_url ?? ''} title={item.name} />
                  <span className="discover-show-copy">
                    <strong>{item.name}</strong>
                    <span>{item.artist || '未知主播'}</span>
                    <small>
                      {already ? '已订阅 · ' : ''}
                      {item.episode_count ? `${item.episode_count} 集` : '公开目录'}
                    </small>
                  </span>
                  <Icon name="chevron" />
                </button>
              )
            })}
          </div>
        </section>
      ) : (
        <>
          <section className="discover-panel">
            <div className="discover-section-head">
              <h2>热门播客</h2>
              <span>{board?.source || 'Apple Podcasts 中国区热门榜'}</span>
            </div>
            {boardLoading ? <p className="muted">正在载入榜单…</p> : null}
            {boardError ? (
              <p className="discover-message" role="status">
                {boardError}
              </p>
            ) : null}
            {renderShows(board?.shows ?? [])}
          </section>

          <section className="discover-panel">
            <div className="discover-section-head">
              <h2>热门节目最新一期</h2>
              <span>{board?.episode_source || '按节目榜位排序，不是单集收听榜'}</span>
            </div>
            {renderEpisodes(board?.episodes ?? [])}
          </section>

          <section className="discover-panel">
            <div className="discover-section-head">
              <h2>海外热门 · 英文精选单集</h2>
              <span>{board?.english_source || 'Apple Podcasts 美国区科技榜 / 商业榜'}</span>
            </div>
            {board?.english_error ? (
              <p className="discover-message" role="status">
                {board.english_error}
              </p>
            ) : (
              <>
                <div className="discover-subhead">
                  <strong>AI · 科技 · 互联网</strong>
                  <span>各节目最新一期 · 按节目榜位</span>
                </div>
                {renderEpisodes(board?.english_tech_episodes ?? [])}
                {!boardLoading &&
                !board?.english_episode_error &&
                (board?.english_tech_episodes ?? []).length === 0 ? (
                  <p className="muted">暂时没有可取回的英文单集。</p>
                ) : null}
                <div className="discover-subhead">
                  <strong>金融与商业</strong>
                  <span>各节目最新一期 · 按节目榜位</span>
                </div>
                {renderEpisodes(board?.english_business_episodes ?? [])}
                {!boardLoading &&
                !board?.english_episode_error &&
                (board?.english_business_episodes ?? []).length === 0 ? (
                  <p className="muted">暂时没有可取回的英文单集。</p>
                ) : null}
                {board?.english_episode_error ? (
                  <p className="discover-message" role="status">
                    {board.english_episode_error}
                  </p>
                ) : null}
              </>
            )}
            <p className="source-note">
              只保留 AI、科技、互联网与金融商业相关的节目和单集；Apple 公开榜单只提供榜位，不提供收听人数。
            </p>
          </section>
        </>
      )}

    </section>
  )
}
