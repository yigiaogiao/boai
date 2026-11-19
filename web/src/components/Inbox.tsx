import { useEffect, useRef, useState } from 'react'
import type {
  KeyboardEvent as ReactKeyboardEvent,
  MouseEvent as ReactMouseEvent,
  PointerEvent as ReactPointerEvent,
} from 'react'
import type { Download, Episode, Feed, SearchResult } from '../types'
import * as api from '../api'
import type { EpisodeInboxAction } from '../api'
import type { Filter, Mode } from '../ui'
import { bytesLabel, clock, durationLabel, episodeDate, truncate } from '../format'
import { Cover } from './Cover'
import { Icon } from './Icons'

interface InboxProps {
  mode: Mode
  filter: Filter
  feeds: Feed[]
  episodes: Episode[]
  downloads: Download[]
  selectedId: number | null
  refreshing: boolean
  downloading: boolean
  scopeFeed: Feed | null
  onMode: (mode: Mode) => void
  onFilter: (filter: Filter) => void
  onSelect: (episodeId: number) => void
  onShow: (feedId: number) => void
  onClearScope: () => void
  onOpenDiscover: () => void
  onOpenTags: (feedId: number) => void
  onBulkTags: (feedIds: number[]) => void
  onOpenCategory: () => void
  onEpisodesState: (ids: number[], action: EpisodeInboxAction) => void
  onRemoveFeeds: (ids: number[]) => void
  onDownload: (episodes: Episode[]) => void
  onDeleteDownloads: (episodeIds: number[]) => void
  onShare: (item: { title: string; link: string; feed_site_url: string }) => void
  onRefresh: () => void
}

type MenuTarget =
  | { kind: 'episode'; episode: Episode }
  | { kind: 'feed'; feed: Feed }
  | { kind: 'download'; download: Download }

interface MenuState {
  target: MenuTarget
  x: number
  y: number
}

function ProgressNote({ episode }: { episode: Episode }) {
  if (episode.inbox_state === 'archived') return <b>已移出</b>
  if (episode.completed || episode.inbox_state === 'completed') return <b>已听完</b>
  if (episode.inbox_state === 'started' && episode.progress_seconds > 0) {
    return <b>继续听 · {clock(episode.progress_seconds)}</b>
  }
  if (episode.inbox_state === 'new') return <b>未听</b>
  return null
}

export function Inbox(props: InboxProps) {
  const { mode, filter, feeds, episodes, downloads, selectedId, scopeFeed } = props
  const [selecting, setSelecting] = useState(false)
  const [pickedEpisodes, setPickedEpisodes] = useState<Set<number>>(new Set())
  const [pickedFeeds, setPickedFeeds] = useState<Set<number>>(new Set())
  const [pickedDownloads, setPickedDownloads] = useState<Set<number>>(new Set())
  const [menu, setMenu] = useState<MenuState | null>(null)
  const [query, setQuery] = useState('')
  const [searchHits, setSearchHits] = useState<SearchResult[]>([])
  const [searching, setSearching] = useState(false)
  const menuRef = useRef<HTMLDivElement>(null)
  const pressTimer = useRef<number | null>(null)
  const pressStart = useRef<{ x: number; y: number } | null>(null)
  const suppressClickUntil = useRef(0)

  const freshCount = episodes.filter((episode) => episode.inbox_state === 'new').length
  const title =
    scopeFeed
      ? scopeFeed.title
      : mode === 'shows'
        ? '我的订阅'
        : mode === 'downloads'
          ? '下载'
          : filter === 'in_progress'
            ? '继续收听'
            : '收件箱'
  const visibleFeeds = feeds.filter((feed) => {
    if (!filter.startsWith('cat:')) return true
    const categoryId = Number(filter.slice(4))
    return feed.categories.some((category) => category.id === categoryId)
  })
  const term = query.trim().toLocaleLowerCase()
  const filteredFeeds = term
    ? visibleFeeds.filter(
        (feed) =>
          feed.title.toLocaleLowerCase().includes(term) ||
          feed.description.toLocaleLowerCase().includes(term) ||
          feed.categories.some((category) => category.name.toLocaleLowerCase().includes(term)),
      )
    : visibleFeeds
  const hitFeedIDs = new Set<number>()
  const hitEpisodeIDs = new Set<number>()
  for (const hit of searchHits) {
    if (hit.kind === 'feed' && hit.feed_id) hitFeedIDs.add(hit.feed_id)
    if ((hit.kind === 'episode' || hit.kind === 'transcript') && hit.episode_id) {
      hitEpisodeIDs.add(hit.episode_id)
    }
  }
  const filteredEpisodes = term
    ? episodes.filter(
        (episode) =>
          hitEpisodeIDs.has(episode.id) ||
          hitFeedIDs.has(episode.feed_id) ||
          episode.title.toLocaleLowerCase().includes(term) ||
          episode.feed_title.toLocaleLowerCase().includes(term) ||
          (episode.description ?? '').toLocaleLowerCase().includes(term),
      )
    : episodes
  const description = term
    ? `${mode === 'shows' ? filteredFeeds.length : filteredEpisodes.length} 条匹配 · 搜索「${query.trim()}」`
    : scopeFeed
      ? `${episodes.length} 集 · 来自「${scopeFeed.title}」`
      : mode === 'shows'
        ? `${feeds.length} 个节目 · 按你的分类整理`
        : mode === 'downloads'
          ? `${downloads.length} 集 · 已保存在本机，可离线播放`
          : filter === 'in_progress'
            ? `${episodes.length} 集 · 从上次停下的位置接着听`
            : `${freshCount} 集未听 · 来自你订阅的节目`
  const pickCount =
    mode === 'shows' ? pickedFeeds.size : mode === 'downloads' ? pickedDownloads.size : pickedEpisodes.size
  const allSelected =
    mode === 'shows'
      ? filteredFeeds.length > 0 && filteredFeeds.every((feed) => pickedFeeds.has(feed.id))
      : mode === 'downloads'
        ? downloads.length > 0 && downloads.every((item) => pickedDownloads.has(item.episode_id))
        : filteredEpisodes.length > 0 &&
          filteredEpisodes.every((episode) => pickedEpisodes.has(episode.id))
  const selectedEpisodes = filteredEpisodes.filter((episode) => pickedEpisodes.has(episode.id))
  const pickedEpisodeIDs = selectedEpisodes.map((episode) => episode.id)
  const pickedFeedIDs = filteredFeeds.filter((feed) => pickedFeeds.has(feed.id)).map((feed) => feed.id)
  const pickedDownloadIDs = downloads
    .filter((item) => pickedDownloads.has(item.episode_id))
    .map((item) => item.episode_id)
  const pickedAllArchived =
    pickedEpisodeIDs.length > 0 && selectedEpisodes.every((episode) => episode.inbox_state === 'archived')
  const scopeId = scopeFeed?.id ?? null
  const downloaded = new Set(downloads.map((item) => item.episode_id))

  useEffect(() => {
    setSelecting(false)
    setPickedEpisodes(new Set())
    setPickedFeeds(new Set())
    setPickedDownloads(new Set())
    setMenu(null)
  }, [mode, filter, scopeId, query])

  useEffect(() => {
    const value = query.trim()
    if (!value || mode === 'shows' || mode === 'downloads') {
      setSearchHits([])
      setSearching(false)
      return
    }
    let cancelled = false
    setSearching(true)
    const timer = window.setTimeout(() => {
      api
        .searchLibrary(value)
        .then((hits) => {
          if (!cancelled) setSearchHits(hits)
        })
        .catch(() => {
          if (!cancelled) setSearchHits([])
        })
        .finally(() => {
          if (!cancelled) setSearching(false)
        })
    }, 260)
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [query, mode])

  function cancelPress() {
    if (pressTimer.current != null) {
      window.clearTimeout(pressTimer.current)
      pressTimer.current = null
    }
    pressStart.current = null
  }

  useEffect(() => () => cancelPress(), [])

  useEffect(() => {
    if (!menu) return
    menuRef.current?.querySelector<HTMLButtonElement>('button')?.focus()
    function onPointerDown(event: PointerEvent) {
      const target = event.target as Element | null
      if (target?.closest('.context-menu')) return
      setMenu(null)
    }
    function onKey(event: KeyboardEvent) {
      if (event.key === 'Escape') setMenu(null)
    }
    function close() {
      setMenu(null)
    }
    window.addEventListener('pointerdown', onPointerDown, true)
    window.addEventListener('keydown', onKey)
    window.addEventListener('resize', close)
    window.addEventListener('scroll', close, true)
    return () => {
      window.removeEventListener('pointerdown', onPointerDown, true)
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('resize', close)
      window.removeEventListener('scroll', close, true)
    }
  }, [menu])

  function endSelection() {
    setSelecting(false)
    setPickedEpisodes(new Set())
    setPickedFeeds(new Set())
    setPickedDownloads(new Set())
  }

  function toggleEpisode(id: number) {
    setPickedEpisodes((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function toggleFeed(id: number) {
    setPickedFeeds((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function toggleDownload(id: number) {
    setPickedDownloads((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function toggleAll() {
    if (mode === 'shows') {
      setPickedFeeds(allSelected ? new Set() : new Set(filteredFeeds.map((feed) => feed.id)))
    } else if (mode === 'downloads') {
      setPickedDownloads(
        allSelected ? new Set() : new Set(downloads.map((item) => item.episode_id)),
      )
    } else {
      setPickedEpisodes(
        allSelected ? new Set() : new Set(filteredEpisodes.map((episode) => episode.id)),
      )
    }
  }

  function runEpisodeAction(ids: number[], action: EpisodeInboxAction) {
    if (ids.length === 0) return
    props.onEpisodesState(ids, action)
    endSelection()
  }

  function confirmFeedUnsubscribe(feed: Feed) {
    if (
      !window.confirm(
        `取消关注「${feed.title}」？\n\n该节目的单集、本地下载和收听进度会一并删除，无法恢复。`,
      )
    ) {
      return
    }
    props.onRemoveFeeds([feed.id])
    endSelection()
  }

  function confirmBulkUnsubscribe() {
    if (pickedFeedIDs.length === 0) return
    const selected = visibleFeeds.filter((feed) => pickedFeeds.has(feed.id))
    const names = selected
      .slice(0, 3)
      .map((feed) => `「${feed.title}」`)
      .join('、')
    const more = selected.length > 3 ? `等 ${selected.length} 个节目` : ''
    if (
      !window.confirm(
        `取消关注${names}${more}？\n\n这些节目的单集、本地下载和收听进度会一并删除，无法恢复。`,
      )
    ) {
      return
    }
    props.onRemoveFeeds(pickedFeedIDs)
    endSelection()
  }

  function confirmDeleteDownloads(ids: number[]) {
    if (ids.length === 0) return
    if (
      !window.confirm(
        `删除 ${ids.length} 个本地下载？\n\n会删除保存在本机的音频文件，不会取消关注，也不会把单集移出收件箱。`,
      )
    ) {
      return
    }
    props.onDeleteDownloads(ids)
    endSelection()
  }

  function openMenuAt(target: MenuTarget, x: number, y: number) {
    cancelPress()
    const count = target.kind === 'episode' ? 5 : 3
    const width = 196
    const height = count * 39 + 18
    setMenu({
      target,
      x: Math.max(8, Math.min(x, window.innerWidth - width - 8)),
      y: Math.max(8, Math.min(y, window.innerHeight - height - 8)),
    })
  }

  function openContext(event: ReactMouseEvent<HTMLElement>, target: MenuTarget) {
    event.preventDefault()
    event.stopPropagation()
    openMenuAt(target, event.clientX, event.clientY)
  }

  function startPress(event: ReactPointerEvent<HTMLElement>, target: MenuTarget) {
    if (event.pointerType !== 'touch' || selecting) return
    cancelPress()
    pressStart.current = { x: event.clientX, y: event.clientY }
    pressTimer.current = window.setTimeout(() => {
      pressTimer.current = null
      suppressClickUntil.current = Date.now() + 900
      openMenuAt(target, event.clientX, event.clientY)
    }, 520)
  }

  function movePress(event: ReactPointerEvent<HTMLElement>) {
    if (!pressStart.current) return
    if (
      Math.abs(event.clientX - pressStart.current.x) > 12 ||
      Math.abs(event.clientY - pressStart.current.y) > 12
    ) {
      cancelPress()
    }
  }

  function rowClick(event: ReactMouseEvent<HTMLElement>, action: () => void) {
    if (Date.now() < suppressClickUntil.current) {
      event.preventDefault()
      return
    }
    action()
  }

  function rowKey(event: ReactKeyboardEvent<HTMLElement>, target: MenuTarget) {
    if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) {
      event.preventDefault()
      const rect = event.currentTarget.getBoundingClientRect()
      openMenuAt(target, rect.left + 24, rect.top + 24)
    }
  }

  function beginSelection(target: MenuTarget) {
    setMenu(null)
    setSelecting(true)
    if (target.kind === 'feed') {
      setPickedFeeds((current) => new Set(current).add(target.feed.id))
    } else if (target.kind === 'download') {
      setPickedDownloads((current) => new Set(current).add(target.download.episode_id))
    } else {
      setPickedEpisodes((current) => new Set(current).add(target.episode.id))
    }
  }

  function menuAction(action: () => void) {
    setMenu(null)
    action()
  }

  const menuTarget = menu?.target ?? null
  const episodeMenu = menuTarget?.kind === 'episode' ? menuTarget.episode : null
  const feedMenu = menuTarget?.kind === 'feed' ? menuTarget.feed : null
  const downloadMenu = menuTarget?.kind === 'download' ? menuTarget.download : null

  return (
    <section className="inbox" aria-labelledby="list-title">
      <header className="inbox-header">
        <div className="inbox-top">
          <div>
            <div className="eyebrow">
              {mode === 'shows' ? 'LIBRARY / 02' : mode === 'downloads' ? 'LIBRARY / 03' : 'LIBRARY / 01'}
            </div>
            <h1 id="list-title">{title}</h1>
          </div>
          <div className="mobile-actions">
            {mode === 'shows' && !selecting ? (
              <button type="button" className="outline" onClick={props.onOpenCategory}>
                <Icon name="tag" />
                分类
              </button>
            ) : null}
            {mode === 'downloads' || selecting ? null : (
              <button
                type="button"
                className="outline"
                onClick={props.onRefresh}
                disabled={props.refreshing}
              >
                <Icon name="refresh" />
                {props.refreshing ? '更新中' : '更新'}
              </button>
            )}
          </div>
          <div className="desktop-actions">
            {mode === 'shows' && !selecting ? (
              <button type="button" className="quiet" onClick={props.onOpenCategory}>
                <Icon name="tag" />
                分类
              </button>
            ) : null}
            {mode === 'downloads' || selecting ? null : (
              <button
                type="button"
                className="quiet"
                onClick={props.onRefresh}
                disabled={props.refreshing}
              >
                <Icon name="refresh" />
                {props.refreshing ? '更新中' : '更新订阅'}
              </button>
            )}
          </div>
        </div>
        <p>{description}</p>
        {mode === 'downloads' ? null : (
          <div className="list-search-field">
            <Icon name="search" />
            <label className="sr-only" htmlFor="list-search">
              {mode === 'shows' ? '搜索已订阅的播客' : '搜索单集、节目或逐字稿'}
            </label>
            <input
              id="list-search"
              type="search"
              autoComplete="off"
              placeholder={
                mode === 'shows' ? '搜索已订阅的播客、主播或分类' : '搜索单集、节目或逐字稿'
              }
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
            {searching ? <span className="search-note">搜索中…</span> : null}
            {query ? (
              <button
                type="button"
                className="icon"
                aria-label="清除搜索"
                onClick={() => setQuery('')}
              >
                <Icon name="close" />
              </button>
            ) : null}
          </div>
        )}
      </header>
      <div
        className={`chip-bar${mode !== 'downloads' && !scopeFeed ? ' mobile-only' : ''}`}
        aria-label="筛选"
      >
        {mode === 'downloads' ? (
          <>
            <button className="chip mobile-actions" type="button" onClick={() => props.onMode('inbox')}>
              收件箱 ↗
            </button>
            <button className="chip mobile-actions" type="button" onClick={() => props.onMode('shows')}>
              我的订阅 ↗
            </button>
            <button
              className="chip mobile-actions"
              type="button"
              onClick={() => props.onOpenDiscover()}
            >
              订阅播客 ↗
            </button>
          </>
        ) : (
          <>
            {scopeFeed ? (
              <button className="chip" type="button" aria-pressed="true" onClick={props.onClearScope}>
                {scopeFeed.title} ×
              </button>
            ) : null}
            <button
              className="chip mobile-actions"
              type="button"
              onClick={() => props.onMode(mode === 'inbox' ? 'shows' : 'inbox')}
            >
              {mode === 'inbox' ? '我的订阅 ↗' : '收件箱 ↗'}
            </button>
            {mode === 'inbox' && filter !== 'in_progress' ? (
              <button
                className="chip mobile-actions"
                type="button"
                onClick={() => props.onFilter('in_progress')}
              >
                继续收听 ↗
              </button>
            ) : null}
            <button
              className="chip mobile-actions"
              type="button"
              onClick={() => props.onMode('downloads')}
            >
              下载{downloads.length > 0 ? ` ${downloads.length}` : ''} ↗
            </button>
            <button
              className="chip mobile-actions"
              type="button"
              onClick={() => props.onOpenDiscover()}
            >
              订阅播客 ↗
            </button>
          </>
        )}
      </div>
      {selecting ? (
        <div className="selection-bar" role="region" aria-label="批量操作">
          <div className="selection-head">
            <span className="selection-count">已选 {pickCount} 项</span>
            <span className="selection-hint">勾选行首复选框</span>
            <button type="button" className="selection-cancel" onClick={endSelection}>
              <Icon name="close" />
              取消
            </button>
          </div>
          <div className="selection-actions">
            <button type="button" onClick={toggleAll}>
              {allSelected ? '清空选择' : '全选'}
            </button>
            {mode === 'shows' ? (
              <>
                <button
                  type="button"
                  disabled={pickCount === 0}
                  onClick={() => {
                    const ids = pickedFeedIDs
                    endSelection()
                    props.onBulkTags(ids)
                  }}
                >
                  分类
                </button>
                <button
                  type="button"
                  className="danger-text"
                  disabled={pickCount === 0}
                  onClick={confirmBulkUnsubscribe}
                >
                  取消关注
                </button>
              </>
            ) : mode === 'downloads' ? (
              <button
                type="button"
                className="danger-text"
                disabled={pickCount === 0}
                onClick={() => confirmDeleteDownloads(pickedDownloadIDs)}
              >
                删除下载
              </button>
            ) : (
              <>
                <button
                  type="button"
                  disabled={pickCount === 0 || props.downloading}
                  onClick={() => {
                    props.onDownload(selectedEpisodes)
                    endSelection()
                  }}
                >
                  {props.downloading ? '下载中' : '下载'}
                </button>
                <button
                  type="button"
                  disabled={pickCount === 0}
                  onClick={() => runEpisodeAction(pickedEpisodeIDs, 'read')}
                >
                  标记已读
                </button>
                <button
                  type="button"
                  disabled={pickCount === 0}
                  onClick={() =>
                    runEpisodeAction(pickedEpisodeIDs, pickedAllArchived ? 'unarchive' : 'archive')
                  }
                >
                  {pickedAllArchived ? '移回收件箱' : '移出收件箱'}
                </button>
              </>
            )}
          </div>
        </div>
      ) : null}
      <div className="list-label">
        {term
          ? '搜索结果'
          : scopeFeed
            ? '节目单集'
            : mode === 'shows'
              ? '已订阅节目'
              : mode === 'downloads'
                ? '已下载单集'
                : filter === 'in_progress'
                  ? '继续收听'
                  : filter === 'all'
                    ? '最近更新'
                    : '筛选结果'}
      </div>
      <div>
        {mode === 'shows'
          ? filteredFeeds.map((feed) => (
              <div
                className="list-row"
                key={feed.id}
                onContextMenu={selecting ? undefined : (event) => openContext(event, { kind: 'feed', feed })}
              >
                {selecting ? (
                  <input
                    type="checkbox"
                    className="row-check"
                    checked={pickedFeeds.has(feed.id)}
                    onChange={() => toggleFeed(feed.id)}
                    aria-label={`选择节目 ${feed.title}`}
                  />
                ) : null}
                <button
                  type="button"
                  className="episode-row"
                  data-feed={feed.id}
                  aria-current={episodes.some(
                    (episode) => episode.feed_id === feed.id && episode.id === selectedId,
                  )}
                  onPointerDown={
                    selecting ? undefined : (event) => startPress(event, { kind: 'feed', feed })
                  }
                  onPointerMove={movePress}
                  onPointerUp={cancelPress}
                  onPointerCancel={cancelPress}
                  onPointerLeave={cancelPress}
                  onKeyDown={(event) => rowKey(event, { kind: 'feed', feed })}
                  onClick={(event) =>
                    rowClick(event, () => (selecting ? toggleFeed(feed.id) : props.onShow(feed.id)))
                  }
                >
                  <Cover imageURL={feed.image_url} title={feed.title} />
                  <span>
                    <strong>{feed.title}</strong>
                    <span className="row-meta">
                      {feed.categories.map((category) => category.name).join(' / ') || '未分类'}
                      <b>{feed.episode_count} 集</b>
                    </span>
                    {feed.last_error ? <span className="row-warning">上次更新失败</span> : null}
                  </span>
                </button>
              </div>
            ))
          : mode === 'downloads'
            ? downloads.map((item) => (
                <div
                  className="list-row"
                  key={item.episode_id}
                  onContextMenu={
                    selecting
                      ? undefined
                      : (event) => openContext(event, { kind: 'download', download: item })
                  }
                >
                  {selecting ? (
                    <input
                      type="checkbox"
                      className="row-check"
                      checked={pickedDownloads.has(item.episode_id)}
                      onChange={() => toggleDownload(item.episode_id)}
                      aria-label={`选择下载 ${item.title}`}
                    />
                  ) : null}
                  <button
                    type="button"
                    className="episode-row"
                    data-download={item.episode_id}
                    data-episode={item.episode_id}
                    data-feed={item.feed_id}
                    aria-current={selectedId === item.episode_id}
                    onPointerDown={
                      selecting
                        ? undefined
                        : (event) => startPress(event, { kind: 'download', download: item })
                    }
                    onPointerMove={movePress}
                    onPointerUp={cancelPress}
                    onPointerCancel={cancelPress}
                    onPointerLeave={cancelPress}
                    onKeyDown={(event) => rowKey(event, { kind: 'download', download: item })}
                    onClick={(event) =>
                      rowClick(event, () =>
                        selecting ? toggleDownload(item.episode_id) : props.onSelect(item.episode_id),
                      )
                    }
                  >
                    <Cover imageURL={item.image_url || item.feed_image_url} title={item.feed_title} />
                    <span>
                      <span className="row-show">{item.feed_title} · 已下载</span>
                      <strong>{truncate(item.title, 120)}</strong>
                      <span className="row-meta">
                        {episodeDate(item.published_at)}
                        <span>·</span>
                        {durationLabel(item.duration_seconds)}
                        <b>{bytesLabel(item.size_bytes)}</b>
                      </span>
                    </span>
                  </button>
                </div>
              ))
            : filteredEpisodes.map((episode) => {
                return (
                  <div
                    className="list-row"
                    key={episode.id}
                    onContextMenu={
                      selecting
                        ? undefined
                        : (event) => openContext(event, { kind: 'episode', episode })
                    }
                  >
                    {selecting ? (
                      <input
                        type="checkbox"
                        className="row-check"
                        checked={pickedEpisodes.has(episode.id)}
                        onChange={() => toggleEpisode(episode.id)}
                        aria-label={`选择单集 ${episode.title}`}
                      />
                    ) : null}
                    <button
                      type="button"
                      className="episode-row"
                      data-episode={episode.id}
                      data-feed={episode.feed_id}
                      aria-current={selectedId === episode.id}
                      onPointerDown={
                        selecting
                          ? undefined
                          : (event) => startPress(event, { kind: 'episode', episode })
                      }
                      onPointerMove={movePress}
                      onPointerUp={cancelPress}
                      onPointerCancel={cancelPress}
                      onPointerLeave={cancelPress}
                      onKeyDown={(event) => rowKey(event, { kind: 'episode', episode })}
                      onClick={(event) =>
                        rowClick(event, () =>
                          selecting ? toggleEpisode(episode.id) : props.onSelect(episode.id),
                        )
                      }
                    >
                      <Cover
                        imageURL={episode.image_url || episode.feed_image_url}
                        title={episode.feed_title}
                      />
                      <span>
                        <span className="row-show">
                          {episode.feed_title}
                          {episode.inbox_state === 'new' ? ' · 未听' : ''}
                          {downloaded.has(episode.id) ? ' · 已下载' : ''}
                        </span>
                        <strong>{truncate(episode.title, 120)}</strong>
                        <span className="row-meta">
                          {episodeDate(episode.published_at)}
                          <span>·</span>
                          {durationLabel(episode.duration_seconds)}
                          {episode.has_transcript ? <b>有逐字稿</b> : <ProgressNote episode={episode} />}
                        </span>
                      </span>
                    </button>
                  </div>
                )
              })}
        {(mode === 'shows'
          ? filteredFeeds.length === 0
          : mode === 'downloads'
            ? downloads.length === 0
            : filteredEpisodes.length === 0) ? (
          <p className="empty">
            {term
              ? `没有匹配「${query.trim()}」的${mode === 'shows' ? '节目' : '单集、节目或逐字稿'}。`
              : mode === 'shows'
                ? '还没有订阅。到「订阅播客」搜索公开目录或粘贴 RSS 链接。'
                : mode === 'downloads'
                  ? '还没有下载。在收件箱右击单集，选择「下载」，文件会保存在本机。'
                  : scopeFeed
                    ? '这个节目还没有单集，或更新尚未完成。'
                    : filter === 'all'
                      ? '收件箱还是空的。订阅节目后，新单集会出现在这里。'
                      : '这个筛选下还没有单集。'}
          </p>
        ) : null}
      </div>
      {menu ? (
        <div
          ref={menuRef}
          className="context-menu"
          role="menu"
          aria-label="操作"
          style={{ left: menu.x, top: menu.y }}
          onContextMenu={(event) => event.preventDefault()}
        >
          {episodeMenu ? (
            <>
              <button
                type="button"
                role="menuitem"
                onClick={() =>
                  menuAction(() =>
                    props.onEpisodesState(
                      [episodeMenu.id],
                      episodeMenu.completed || episodeMenu.inbox_state === 'completed'
                        ? 'unread'
                        : 'read',
                    ),
                  )
                }
              >
                {episodeMenu.completed || episodeMenu.inbox_state === 'completed'
                  ? '标为未读'
                  : '标记已读'}
              </button>
              <button
                type="button"
                role="menuitem"
                onClick={() =>
                  menuAction(() =>
                    props.onEpisodesState(
                      [episodeMenu.id],
                      episodeMenu.inbox_state === 'archived' ? 'unarchive' : 'archive',
                    ),
                  )
                }
              >
                {episodeMenu.inbox_state === 'archived' ? '移回收件箱' : '移出收件箱'}
              </button>
              <div className="menu-sep" role="separator" />
              <button
                type="button"
                role="menuitem"
                onClick={() => beginSelection({ kind: 'episode', episode: episodeMenu })}
              >
                多选
              </button>
              <button type="button" role="menuitem" onClick={() => menuAction(() => props.onShare(episodeMenu))}>
                分享
              </button>
              <button
                type="button"
                role="menuitem"
                onClick={() => menuAction(() => props.onDownload([episodeMenu]))}
              >
                下载
              </button>
            </>
          ) : null}
          {feedMenu ? (
            <>
              <button
                type="button"
                role="menuitem"
                onClick={() => menuAction(() => props.onOpenTags(feedMenu.id))}
              >
                分类
              </button>
              <button
                type="button"
                role="menuitem"
                onClick={() => beginSelection({ kind: 'feed', feed: feedMenu })}
              >
                多选
              </button>
              <button
                type="button"
                role="menuitem"
                className="danger-text"
                onClick={() => menuAction(() => confirmFeedUnsubscribe(feedMenu))}
              >
                取消关注
              </button>
            </>
          ) : null}
          {downloadMenu ? (
            <>
              <button
                type="button"
                role="menuitem"
                onClick={() => menuAction(() => props.onShare(downloadMenu))}
              >
                分享
              </button>
              <button
                type="button"
                role="menuitem"
                className="danger-text"
                onClick={() => menuAction(() => confirmDeleteDownloads([downloadMenu.episode_id]))}
              >
                删除下载
              </button>
              <button
                type="button"
                role="menuitem"
                onClick={() => beginSelection({ kind: 'download', download: downloadMenu })}
              >
                多选
              </button>
            </>
          ) : null}
        </div>
      ) : null}
    </section>
  )
}
