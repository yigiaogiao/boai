import { useCallback, useEffect, useRef, useState } from 'react'
import type { SyntheticEvent } from 'react'
import * as api from './api'
import type {
  AITask,
  Category,
  DiscoverEpisode,
  Download,
  Episode,
  EpisodeAI,
  EpisodeDetail,
  Feed,
  ShowPreview,
  StatusResponse,
  Transcript,
} from './types'
import type { DialogName, Filter, Mode, ReaderTab, TranscriptState } from './ui'
import { IconDefs } from './components/Icons'
import { Sidebar } from './components/Sidebar'
import { Inbox } from './components/Inbox'
import { Discover } from './components/Discover'
import { Reader } from './components/Reader'
import { ShowDetail, type ShowTarget } from './components/ShowDetail'
import { Player } from './components/Player'
import { CategoryDialog, ExportDialog, SettingsDialog, TagsDialog } from './components/Dialogs'

const RATES = [1, 1.25, 1.5, 2]

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : '操作失败，请稍后重试'
}

async function copyText(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value)
    return
  }
  const area = document.createElement('textarea')
  area.value = value
  area.setAttribute('readonly', '')
  area.style.position = 'fixed'
  area.style.opacity = '0'
  document.body.append(area)
  area.select()
  const copied = document.execCommand('copy')
  area.remove()
  if (!copied) throw new Error('复制失败')
}

export default function App() {
  const [status, setStatus] = useState<StatusResponse | null>(null)
  const [feeds, setFeeds] = useState<Feed[]>([])
  const [categories, setCategories] = useState<Category[]>([])
  const [episodes, setEpisodes] = useState<Episode[]>([])
  const [downloads, setDownloads] = useState<Download[]>([])
  const [downloading, setDownloading] = useState(false)
  const [mode, setMode] = useState<Mode>('inbox')
  const [filter, setFilter] = useState<Filter>('all')
  const [feedScope, setFeedScope] = useState<number | null>(null)
  const [showView, setShowView] = useState<ShowTarget | null>(null)
  const [showPreview, setShowPreview] = useState<ShowPreview | null>(null)
  const [showLoading, setShowLoading] = useState(false)
  const [showError, setShowError] = useState('')
  const [showSubscribing, setShowSubscribing] = useState(false)
  const [episodeVersion, setEpisodeVersion] = useState(0)
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [detail, setDetail] = useState<EpisodeDetail | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [transcript, setTranscript] = useState<Transcript | null>(null)
  const [transcriptState, setTranscriptState] = useState<TranscriptState>('idle')
  const [transcriptError, setTranscriptError] = useState('')
  const [ai, setAI] = useState<EpisodeAI | null>(null)
  const [aiLoading, setAILoading] = useState(false)
  const [aiTrigger, setAITrigger] = useState(0)
  const aiWasActive = useRef(false)
  const [tab, setTab] = useState<ReaderTab>('summary')
  const [dialog, setDialog] = useState<DialogName>(null)
  const [tagFeedIds, setTagFeedIds] = useState<number[]>([])
  const [discoverFocus, setDiscoverFocus] = useState(0)
  const [toast, setToast] = useState('')
  const [refreshing, setRefreshing] = useState(false)
  const [bootError, setBootError] = useState('')
  const [booting, setBooting] = useState(true)
  const [pane, setPane] = useState<'list' | 'detail'>('list')
  const [restoreReaderId, setRestoreReaderId] = useState<number | null>(null)

  const [nowPlaying, setNowPlaying] = useState<Episode | null>(null)
  const [playing, setPlaying] = useState(false)
  const [position, setPosition] = useState(0)
  const [duration, setDuration] = useState(0)
  const [rateIndex, setRateIndex] = useState(0)

  const audioRef = useRef<HTMLAudioElement>(null)
  const nowPlayingRef = useRef<Episode | null>(null)
  const pendingTime = useRef<number | null>(null)
  const startOverride = useRef<number | null>(null)
  const autoPlayRequested = useRef(false)
  const pendingTab = useRef<ReaderTab | null>(null)
  const showViewRef = useRef<ShowTarget | null>(null)
  const showReturnRef = useRef<number | null>(null)
  const showRequest = useRef(0)
  const toastTimer = useRef<number>(0)
  const paneRef = useRef(pane)
  paneRef.current = pane
  const dialogOpenRef = useRef(false)
  const dialogClosingRef = useRef(false)

  const notify = useCallback((message: string) => {
    setToast(message)
    window.clearTimeout(toastTimer.current)
    toastTimer.current = window.setTimeout(() => setToast(''), 3200)
  }, [])

  useEffect(() => () => window.clearTimeout(toastTimer.current), [])

  // Mobile and WebView back gestures should close a dialog or return from the
  // episode detail to the list before leaving the app.
  function openDialog(name: NonNullable<DialogName>) {
    setDialog(name)
    dialogOpenRef.current = true
    window.history.pushState({ boai: 'dialog' }, '')
  }

  function closeDialog() {
    if (!dialogOpenRef.current) {
      setDialog(null)
      return
    }
    dialogOpenRef.current = false
    dialogClosingRef.current = true
    setDialog(null)
    window.history.back()
  }

  useEffect(() => {
    function onPopState() {
      if (dialogClosingRef.current) {
        dialogClosingRef.current = false
        return
      }
      if (dialogOpenRef.current) {
        dialogOpenRef.current = false
        setDialog(null)
        return
      }
      if (paneRef.current === 'detail') {
        paneRef.current = 'list'
        setPane('list')
        return
      }
      if (showViewRef.current) {
        showViewRef.current = null
        showRequest.current += 1
        setShowView(null)
        setShowPreview(null)
        setShowLoading(false)
        setShowError('')
        const returnId = showReturnRef.current
        showReturnRef.current = null
        if (returnId != null) setRestoreReaderId(returnId)
      }
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  // Returning from a show that was opened on top of the reader restores the
  // episode the listener was reading, instead of leaving an empty reader.
  useEffect(() => {
    if (restoreReaderId == null) return
    setRestoreReaderId(null)
    selectEpisode(restoreReaderId)
  }, [restoreReaderId])

  const loadLibrary = useCallback(async () => {
    const [nextStatus, nextFeeds, nextCategories, nextDownloads] = await Promise.all([
      api.getStatus(),
      api.getFeeds(),
      api.getCategories(),
      api.getDownloads(),
    ])
    setStatus(nextStatus)
    setFeeds(nextFeeds)
    setCategories(nextCategories)
    setDownloads(nextDownloads)
  }, [])

  useEffect(() => {
    let cancelled = false
    setBooting(true)
    loadLibrary()
      .catch((error) => {
        if (!cancelled) setBootError(messageOf(error))
      })
      .finally(() => {
        if (!cancelled) setBooting(false)
      })
    return () => {
      cancelled = true
    }
  }, [loadLibrary])

  useEffect(() => {
    nowPlayingRef.current = nowPlaying
  }, [nowPlaying])

  useEffect(() => {
    if (mode === 'downloads') return
    let cancelled = false
    const filterArg: api.EpisodeListFilter =
      filter === 'all' || filter === 'new' || filter === 'in_progress'
        ? { kind: filter }
        : { kind: 'category', categoryId: Number(filter.slice(4)) }
    api
      .getEpisodes(filterArg, feedScope ?? undefined)
      .then((list) => {
        if (!cancelled) setEpisodes(list)
      })
      .catch((error) => {
        if (!cancelled) {
          setEpisodes([])
          notify(messageOf(error))
        }
      })
    return () => {
      cancelled = true
    }
  }, [filter, feedScope, episodeVersion, notify, mode])

  useEffect(() => {
    setSelectedId((current) => {
      if (episodes.length === 0) return null
      return current != null && episodes.some((episode) => episode.id === current)
        ? current
        : episodes[0].id
    })
  }, [episodes])

  useEffect(() => {
    if (selectedId == null) {
      setDetail(null)
      setDetailLoading(false)
      return
    }
    let cancelled = false
    setDetail((current) => (current && current.episode.id === selectedId ? current : null))
    setDetailLoading(true)
    api
      .getEpisode(selectedId)
      .then((loaded) => {
        if (cancelled) return
        setDetail(loaded)
        setTab(pendingTab.current ?? 'summary')
        pendingTab.current = null
      })
      .catch((error) => {
        if (!cancelled) {
          setDetail(null)
          notify(messageOf(error))
        }
      })
      .finally(() => {
        if (!cancelled) setDetailLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [selectedId, notify])

  useEffect(() => {
    const id = detail?.episode.id
    if (!id) {
      setTranscript(null)
      setTranscriptState('idle')
      setTranscriptError('')
      return
    }
    let cancelled = false
    setTranscript(null)
    setTranscriptState('loading')
    setTranscriptError('')
    api
      .getTranscript(id)
      .then((value) => {
        if (cancelled) return
        if (value) {
          setTranscript(value)
          setTranscriptState('ready')
        } else {
          setTranscriptState('missing')
        }
      })
      .catch((error) => {
        if (!cancelled) {
          setTranscriptError(messageOf(error))
          setTranscriptState('error')
        }
      })
    return () => {
      cancelled = true
    }
  }, [detail?.episode.id])

  useEffect(() => {
    aiWasActive.current = false
    setAI(null)
    setAILoading(Boolean(detail?.episode.id))
  }, [detail?.episode.id])

  useEffect(() => {
    const id = detail?.episode.id
    if (!id) return
    let cancelled = false
    let timer = 0
    const load = (initial: boolean) => {
      if (initial) setAILoading(true)
      api
        .getEpisodeAI(id)
        .then((next) => {
          if (cancelled) return
          setAI(next)
          const active = next.tasks.some(
            (task) => task.status === 'queued' || task.status === 'processing',
          )
          if (aiWasActive.current && !active) {
            api
              .getTranscript(id)
              .then((value) => {
                if (cancelled) return
                setTranscript(value)
                setTranscriptState(value ? 'ready' : 'missing')
              })
              .catch(() => undefined)
            api
              .getEpisode(id)
              .then((loaded) => {
                if (!cancelled) {
                  setDetail((current) => (current && current.episode.id === id ? loaded : current))
                }
              })
              .catch(() => undefined)
          }
          aiWasActive.current = active
          if (active) timer = window.setTimeout(() => load(false), 1200)
        })
        .catch(() => {
          if (!cancelled && initial) setAI(null)
        })
        .finally(() => {
          if (!cancelled && initial) setAILoading(false)
        })
    }
    load(true)
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [detail?.episode.id, aiTrigger])

  useEffect(() => {
    const audio = audioRef.current
    const episode = nowPlaying
    if (!audio || !episode) return
    audio.dataset.episodeId = String(episode.id)
    audio.src = api.audioURL(episode.id)
    audio.playbackRate = RATES[rateIndex]
    const override = startOverride.current
    startOverride.current = null
    const start =
      override ?? (episode.progress_seconds > 0 && !episode.completed ? episode.progress_seconds : 0)
    pendingTime.current = start
    setPosition(start)
    setDuration(episode.duration_seconds || 0)
    setPlaying(false)
    audio.load()
    // Progress saves refresh nowPlaying objects with a new identity. Reloading
    // the <audio> element on those updates paused playback every 8 seconds, so
    // only a different episode is allowed to reset the source.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nowPlaying?.id])

  useEffect(() => {
    if (audioRef.current) audioRef.current.playbackRate = RATES[rateIndex]
  }, [rateIndex])

  useEffect(() => {
    if (!playing) return
    const timer = window.setInterval(() => {
      const audio = audioRef.current
      const episode = nowPlayingRef.current
      if (!audio || !episode) return
      void persistProgress(
        episode,
        audio.currentTime,
        Number.isFinite(audio.duration) ? audio.duration : episode.duration_seconds,
      )
    }, 8000)
    return () => window.clearInterval(timer)
  }, [playing])

  useEffect(() => {
    function save() {
      const audio = audioRef.current
      const episode = nowPlayingRef.current
      if (!audio || !episode || audio.currentTime <= 0) return
      void api
        .saveProgress(
          episode.id,
          audio.currentTime,
          Number.isFinite(audio.duration) ? audio.duration : episode.duration_seconds,
          true,
        )
        .catch(() => undefined)
    }
    function onVisibility() {
      if (document.visibilityState === 'hidden') save()
    }
    window.addEventListener('beforeunload', save)
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      window.removeEventListener('beforeunload', save)
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [])

  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        if (document.querySelector('dialog[open]')) return
        event.preventDefault()
        setMode('discover')
        setFilter('all')
        setFeedScope(null)
        setPane('list')
        setDiscoverFocus((value) => value + 1)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  function patchEpisode(id: number, update: (episode: Episode) => Episode) {
    setEpisodes((list) => list.map((episode) => (episode.id === id ? update(episode) : episode)))
    setDetail((current) =>
      current && current.episode.id === id ? { ...current, episode: update(current.episode) } : current,
    )
    setNowPlaying((current) => (current && current.id === id ? update(current) : current))
  }

  async function persistProgress(
    episode: Episode,
    nextPosition: number,
    nextDuration: number,
    keepalive = false,
  ) {
    if (!Number.isFinite(nextPosition) || nextPosition < 0) return
    try {
      const progress = await api.saveProgress(
        episode.id,
        nextPosition,
        nextDuration,
        keepalive,
      )
      const wasInProgress = episode.progress_seconds > 0 && !episode.completed
      const nowInProgress = progress.position_seconds > 0 && !progress.completed
      if (wasInProgress !== nowInProgress) {
        setStatus((current) =>
          current
            ? {
                ...current,
                library: {
                  ...current.library,
                  in_progress: Math.max(0, current.library.in_progress + (nowInProgress ? 1 : -1)),
                },
              }
            : current,
        )
      }
      patchEpisode(episode.id, (current) => ({
        ...current,
        progress_seconds: progress.position_seconds,
        progress_duration: progress.duration_seconds,
        completed: progress.completed,
        inbox_state: progress.completed
          ? 'completed'
          : progress.position_seconds > 0
            ? 'started'
            : current.inbox_state,
        progress_updated_at: progress.updated_at,
      }))
    } catch {
      // Background progress saves stay quiet; the next event retries.
    }
  }

  function selectEpisode(episodeId: number) {
    pendingTab.current = null
    setSelectedId(episodeId)
    if (dialogOpenRef.current) {
      // The search dialog is handing off to the reader. Replace its history
      // entry instead of closing it first: closeDialog() would queue a
      // history.back() and race the pushState below, which can leave the
      // WebView without a detail entry to return from.
      dialogOpenRef.current = false
      setDialog(null)
      if (paneRef.current !== 'detail') {
        window.history.replaceState({ boai: 'detail' }, '')
      }
    } else if (paneRef.current !== 'detail' && window.matchMedia('(max-width: 620px)').matches) {
      window.history.pushState({ boai: 'detail' }, '')
    }
    paneRef.current = 'detail'
    setPane('detail')
  }

  function closeDetail() {
    if (window.history.state?.boai === 'detail') {
      window.history.back()
      return
    }
    paneRef.current = 'list'
    setPane('list')
  }

  function clearShow() {
    showViewRef.current = null
    showReturnRef.current = null
    showRequest.current += 1
    setShowView(null)
    setShowPreview(null)
    setShowLoading(false)
    setShowError('')
    setShowSubscribing(false)
    if (window.history.state?.boai === 'show') {
      window.history.replaceState({}, '')
    }
  }

  function closeShow() {
    const returnId = showReturnRef.current
    if (window.history.state?.boai === 'show') {
      window.history.back()
      return
    }
    clearShow()
    if (returnId != null) setRestoreReaderId(returnId)
  }

  function findEpisode(episodes: Episode[], item: DiscoverEpisode): Episode | null {
    const wanted = item.title.trim().toLocaleLowerCase()
    const exact = episodes.find((episode) => episode.title.trim().toLocaleLowerCase() === wanted)
    if (exact) return exact
    const partial = episodes.find((episode) => {
      const title = episode.title.trim().toLocaleLowerCase()
      return title.includes(wanted) || wanted.includes(title)
    })
    if (partial) return partial
    return null
  }

  async function openShowTarget(target: ShowTarget, match?: DiscoverEpisode, reusePreview = false) {
    const request = ++showRequest.current
    const wasDetail = paneRef.current === 'detail'
    showReturnRef.current = wasDetail ? selectedId : null
    showViewRef.current = target
    setShowView(target)
    setShowPreview(null)
    setShowLoading(true)
    setShowError('')
    setFeedScope(null)
    setSelectedId(null)
    setDetail(null)
    paneRef.current = 'list'
    setPane('list')
    if (wasDetail && window.history.state?.boai === 'detail') {
      window.history.replaceState({ boai: 'show' }, '')
    } else if (!wasDetail && window.matchMedia('(max-width: 620px)').matches) {
      window.history.pushState({ boai: 'show' }, '')
    }
    try {
      const cached =
        reusePreview && showPreview && showPreview.feed?.url === target.feedUrl ? showPreview : null
      const local = feeds.find((feed) => feed.url === target.feedUrl)
      const preview: ShowPreview = cached
        ? cached
        : local
          ? {
              feed: local,
              episodes: await api.getEpisodes({ kind: 'all' }, local.id, 120),
              subscribed: true,
            }
          : await api.previewShow(target.feedUrl)
      if (request !== showRequest.current) return
      setShowPreview(preview)
      setShowLoading(false)
      if (match) {
        const episode = findEpisode(preview.episodes, match)
        if (episode) {
          selectEpisode(episode.id)
        } else {
          notify('已打开节目详情，但没有在节目源中找到这一集')
        }
      }
    } catch (error) {
      if (request !== showRequest.current) return
      setShowError(messageOf(error))
      setShowLoading(false)
    }
  }

  function openShow(feedId: number) {
    const feed = feeds.find((item) => item.id === feedId)
    if (!feed) return
    void openShowTarget({
      feedUrl: feed.url,
      title: feed.title,
      artwork: feed.image_url,
      source: '我的订阅',
    })
  }

  function openDiscoverShow(target: ShowTarget) {
    void openShowTarget(target)
  }

  // Opening the show behind the current episode works for subscribed feeds
  // and for previews opened from Discover, where the feed is hidden from the
  // subscription list and may not have a local row in `feeds`.
  function openEpisodeShow(episode: Episode) {
    if (showViewRef.current && showViewRef.current.feedUrl === episode.feed_url) {
      void openShowTarget(showViewRef.current, undefined, true)
      return
    }
    const local = feeds.find((feed) => feed.id === episode.feed_id)
    if (local) {
      openShow(local.id)
      return
    }
    if (!episode.feed_url) {
      notify('暂时无法打开节目详情。')
      return
    }
    void openShowTarget({
      feedUrl: episode.feed_url,
      title: episode.feed_title,
      artwork: episode.feed_image_url,
      source: '单集详情',
    })
  }

  function openDiscoverEpisode(item: DiscoverEpisode) {
    void openShowTarget(
      {
        feedUrl: item.feed_url,
        title: item.show_title,
        artwork: item.artwork_url,
        source: item.source === 'itunes_us' ? 'Apple Podcasts 美国区榜单' : 'Apple Podcasts 热门榜',
      },
      item,
    )
  }

  async function subscribeCurrentShow() {
    const target = showViewRef.current
    if (!target || showSubscribing) return
    setShowSubscribing(true)
    setShowError('')
    try {
      const result = await api.subscribe(target.feedUrl, target.feedUrl, [])
      const episodes = await api.getEpisodes({ kind: 'all' }, result.feed.id, 120)
      await reloadAll()
      setShowPreview({ feed: result.feed, episodes, subscribed: true })
      notify(`已订阅「${result.feed.title}」${result.new_episodes > 0 ? ` · ${result.new_episodes} 集` : ''}`)
    } catch (error) {
      setShowError(messageOf(error))
    } finally {
      setShowSubscribing(false)
    }
  }

  async function unsubscribeCurrentShow() {
    const feed = showPreview?.feed
    const target = showViewRef.current
    if (!feed || !target || showSubscribing) return
    if (!window.confirm(`取消关注「${feed.title}」？\n\n本地下载和收听进度会删除；节目页会保留，你可以重新订阅。`)) {
      return
    }
    setShowSubscribing(true)
    setShowError('')
    const request = ++showRequest.current
    try {
      await api.removeFeeds([feed.id])
      const current = nowPlayingRef.current
      if (current && current.feed_id === feed.id) {
        nowPlayingRef.current = null
        audioRef.current?.pause()
        setNowPlaying(null)
        setPlaying(false)
        setPosition(0)
        setDuration(0)
      }
      setSelectedId(null)
      setDetail(null)
      paneRef.current = 'list'
      setPane('list')
      setShowPreview(null)
      setShowLoading(true)
      notify(`已取消关注「${feed.title}」，可以重新订阅`)
      await reloadAll()
      setEpisodeVersion((value) => value + 1)
      const preview = await api.previewShow(target.feedUrl)
      if (request !== showRequest.current) return
      setShowPreview(preview)
      setShowLoading(false)
    } catch (error) {
      if (request !== showRequest.current) return
      setShowError(messageOf(error))
      setShowLoading(false)
    } finally {
      setShowSubscribing(false)
    }
  }

  function changeMode(next: Mode) {
    clearShow()
    setMode(next)
    setFilter('all')
    setFeedScope(null)
    setPane('list')
  }

  function changeFilter(next: Filter) {
    clearShow()
    setMode('inbox')
    setFilter(next)
    setFeedScope(null)
    setPane('list')
  }

  function changeListFilter(next: Filter) {
    if (mode === 'shows') {
      clearShow()
      setFilter(next)
      setFeedScope(null)
      setPane('list')
      return
    }
    changeFilter(next)
  }

  async function reloadAll() {
    try {
      await loadLibrary()
      setEpisodeVersion((value) => value + 1)
    } catch (error) {
      notify(messageOf(error))
    }
  }

  async function handleRefresh() {
    if (refreshing) return
    setRefreshing(true)
    try {
      const result = await api.refreshAll()
      notify(
        result.failed > 0
          ? `更新完成：${result.new_episodes} 集新内容，${result.failed} 个节目失败`
          : `更新完成：${result.new_episodes} 集新内容`,
      )
      await loadLibrary()
      setEpisodeVersion((value) => value + 1)
      if (selectedId != null) {
        try {
          setDetail(await api.getEpisode(selectedId))
        } catch {
          // The current episode may have been removed by a feed change.
        }
      }
    } catch (error) {
      notify(messageOf(error))
    } finally {
      setRefreshing(false)
    }
  }

  async function handleEpisodesState(ids: number[], action: api.EpisodeInboxAction) {
    if (ids.length === 0) return
    try {
      const result = await api.setEpisodesInboxState(ids, action)
      const labels: Record<api.EpisodeInboxAction, string> = {
        read: '已标记为已读',
        unread: '已标为未读',
        archive: '已移出收件箱',
        unarchive: '已移回收件箱',
      }
      notify(`${labels[action]} · ${result.updated} 集`)
      await reloadAll()
      if (selectedId != null && ids.includes(selectedId)) {
        try {
          setDetail(await api.getEpisode(selectedId))
        } catch {
          // The episode may have left the current filter or the show may be gone.
        }
      }
    } catch (error) {
      notify(messageOf(error))
    }
  }

  async function handleRemoveFeeds(ids: number[]) {
    if (ids.length === 0) return
    try {
      const result = await api.removeFeeds(ids)
      const removed = new Set(ids)
      const current = nowPlayingRef.current
      if (current && removed.has(current.feed_id)) {
        nowPlayingRef.current = null
        audioRef.current?.pause()
        setNowPlaying(null)
        setPlaying(false)
        setPosition(0)
        setDuration(0)
      }
      if (detail && removed.has(detail.episode.feed_id)) {
        setSelectedId(null)
        setDetail(null)
        paneRef.current = 'list'
        setPane('list')
      }
      if (feedScope != null && removed.has(feedScope)) setFeedScope(null)
      if (showPreview && removed.has(showPreview.feed.id)) clearShow()
      notify(`已取消关注 ${result.removed} 个节目`)
      await reloadAll()
    } catch (error) {
      notify(messageOf(error))
    }
  }

  async function handleDownloadEpisodes(list: Episode[]) {
    if (downloading || list.length === 0) return
    const seen = new Set<number>()
    const pending = list.filter((episode) => {
      if (seen.has(episode.id)) return false
      seen.add(episode.id)
      return !downloads.some((item) => item.episode_id === episode.id)
    })
    if (pending.length === 0) {
      notify('这些单集已经下载过了')
      return
    }
    setDownloading(true)
    notify(`正在下载 ${pending.length} 集…`)
    let saved = 0
    let failed = 0
    let reason = ''
    try {
      for (const episode of pending) {
        try {
          await api.downloadEpisode(episode.id)
          saved += 1
        } catch (error) {
          failed += 1
          reason = messageOf(error)
        }
      }
      await reloadAll()
      if (failed > 0) {
        notify(`已下载 ${saved} 集，${failed} 集失败：${reason}`)
      } else {
        notify(`已下载 ${saved} 集，可在「下载」中查看`)
      }
    } finally {
      setDownloading(false)
    }
  }

  async function handleDeleteDownloads(episodeIDs: number[]) {
    if (episodeIDs.length === 0) return
    try {
      const result = await api.removeDownloads(episodeIDs)
      await reloadAll()
      notify(`已删除 ${result.removed} 个本地下载`)
    } catch (error) {
      notify(messageOf(error))
    }
  }

  async function handleShare(item: { title: string; link: string; feed_site_url: string }) {
    const link = item.link || item.feed_site_url
    if (!link) {
      notify('这一集没有可分享的链接')
      return
    }
    try {
      await copyText(link)
      notify(`已复制「${item.title}」的链接`)
    } catch {
      notify('复制失败，请手动复制链接')
    }
  }

  function handleDiscoverSubscribed(feed: Feed, newEpisodes: number) {
    notify(`已订阅「${feed.title}」${newEpisodes > 0 ? ` · ${newEpisodes} 集` : ''}`)
    void reloadAll()
  }

  function handleCategoryDeleted(categoryId: number) {
    if (filter === `cat:${categoryId}`) setFilter('all')
    setEpisodeVersion((value) => value + 1)
  }

  async function loadTranscript() {
    const episode = detail?.episode
    if (!episode) return
    setTranscriptState('loading')
    setTranscriptError('')
    try {
      const value = await api.fetchTranscript(episode.id)
      setTranscript(value)
      setTranscriptState('ready')
      patchEpisode(episode.id, (current) =>
        current.has_transcript ? current : { ...current, has_transcript: true },
      )
      notify('逐字稿已获取')
    } catch (error) {
      setTranscriptError(messageOf(error))
      setTranscriptState('error')
    }
  }

  function toggleSelectedPlayback() {
    const episode = detail?.episode
    if (!episode) return
    const audio = audioRef.current
    if (nowPlaying?.id === episode.id && audio) {
      if (audio.paused) {
        void audio.play().catch((error) => notify(`无法播放：${messageOf(error)}`))
      } else {
        audio.pause()
      }
      return
    }
    startOverride.current = null
    autoPlayRequested.current = true
    setNowPlaying(episode)
  }

  function togglePlayback() {
    const audio = audioRef.current
    if (!audio || !nowPlaying) return
    if (audio.paused) {
      void audio.play().catch((error) => notify(`无法播放：${messageOf(error)}`))
    } else {
      audio.pause()
    }
  }

  function seekTo(seconds: number, reveal?: ReaderTab) {
    const episode = detail?.episode
    if (!episode) return
    if (reveal) setTab(reveal)
    const audio = audioRef.current
    if (nowPlaying?.id !== episode.id) {
      startOverride.current = seconds
      autoPlayRequested.current = true
      setNowPlaying(episode)
      return
    }
    if (!audio) return
    if (audio.readyState >= 1) {
      const max = Number.isFinite(audio.duration) ? audio.duration : seconds
      audio.currentTime = Math.max(0, Math.min(seconds, max))
      setPosition(audio.currentTime)
    } else {
      pendingTime.current = seconds
    }
    if (audio.paused) {
      void audio.play().catch((error) => notify(`无法播放：${messageOf(error)}`))
    }
  }

  function seekPlayer(seconds: number) {
    const audio = audioRef.current
    if (!audio || !nowPlaying) return
    const max = Number.isFinite(audio.duration) ? audio.duration : seconds
    audio.currentTime = Math.max(0, Math.min(seconds, max))
    setPosition(audio.currentTime)
  }

  function skip(delta: number) {
    const audio = audioRef.current
    if (!audio || !nowPlaying) return
    const max = Number.isFinite(audio.duration)
      ? audio.duration
      : nowPlaying.duration_seconds || audio.currentTime + delta
    const next = Math.max(0, Math.min(audio.currentTime + delta, max))
    audio.currentTime = next
    setPosition(next)
  }

  function handleLoadedMetadata() {
    const audio = audioRef.current
    if (!audio) return
    const target = pendingTime.current
    pendingTime.current = null
    if (target != null) {
      const max = Number.isFinite(audio.duration) ? Math.max(0, audio.duration - 0.25) : target
      audio.currentTime = Math.max(0, Math.min(target, max))
      setPosition(audio.currentTime)
    }
    if (Number.isFinite(audio.duration) && audio.duration > 0) setDuration(audio.duration)
    audio.playbackRate = RATES[rateIndex]
  }

  function handleCanPlay() {
    const audio = audioRef.current
    if (!audio) return
    if (autoPlayRequested.current) {
      autoPlayRequested.current = false
      void audio.play().catch((error) => {
        setPlaying(false)
        notify(`无法播放：${messageOf(error)}`)
      })
    }
  }

  function mergeTask(task: AITask) {
    if (task.status === 'queued' || task.status === 'processing') aiWasActive.current = true
    setAI((current) => {
      const base = current ?? { insights: [], questions: [], tasks: [] }
      return { ...base, tasks: [task, ...base.tasks.filter((item) => item.id !== task.id)] }
    })
  }

  async function enqueueAI(run: () => Promise<AITask>, message: string) {
    try {
      const task = await run()
      mergeTask(task)
      setAITrigger((value) => value + 1)
      notify(message)
    } catch (error) {
      notify(messageOf(error))
    }
  }

  function generateInsight(kind: 'summary' | 'analysis' | 'map') {
    const id = detail?.episode.id
    if (!id) return
    const labels = { summary: '导读', analysis: '精读', map: '导图' }
    void enqueueAI(() => api.generateInsight(id, kind), `${labels[kind]}任务已排队`)
  }

  function translateTranscript(targetLanguage: string) {
    const id = detail?.episode.id
    if (!id) return
    void enqueueAI(() => api.translateTranscript(id, targetLanguage), '译文任务已排队')
  }

  function transcribeEpisode() {
    const id = detail?.episode.id
    if (!id) return
    void enqueueAI(() => api.transcribeEpisode(id), '云端转写任务已排队')
  }

  function askEpisode(question: string) {
    const id = detail?.episode.id
    if (!id) return
    void enqueueAI(() => api.askEpisode(id, question), '问题已排队')
  }

  async function retryAITask(taskId: string) {
    try {
      const task = await api.retryTask(taskId)
      mergeTask(task)
      setAITrigger((value) => value + 1)
      notify('已重试任务')
    } catch (error) {
      notify(messageOf(error))
    }
  }

  function handleTimeUpdate(event: SyntheticEvent<HTMLAudioElement>) {
    const audio = event.currentTarget
    setPosition(audio.currentTime)
    if (Number.isFinite(audio.duration) && audio.duration > 0) setDuration(audio.duration)
  }

  function handlePause(event: SyntheticEvent<HTMLAudioElement>) {
    setPlaying(false)
    const audio = event.currentTarget
    const episode = nowPlayingRef.current
    if (!episode || audio.dataset.episodeId !== String(episode.id)) return
    if (audio.currentTime <= 0) return
    void persistProgress(
      episode,
      audio.currentTime,
      Number.isFinite(audio.duration) ? audio.duration : episode.duration_seconds,
    )
  }

  function handleEnded(event: SyntheticEvent<HTMLAudioElement>) {
    setPlaying(false)
    const audio = event.currentTarget
    const episode = nowPlayingRef.current
    if (!episode) return
    const total = Number.isFinite(audio.duration)
      ? audio.duration
      : episode.duration_seconds || audio.currentTime
    setPosition(total)
    void persistProgress(episode, total, total)
  }

  async function handleAudioError() {
    autoPlayRequested.current = false
    setPlaying(false)
    const audio = audioRef.current
    const src = audio?.currentSrc || audio?.src || ''
    if (!src) {
      notify('无法播放：音频地址为空。')
      return
    }
    try {
      const response = await fetch(src, {
        headers: { Range: 'bytes=0-1' },
        cache: 'no-store',
      })
      if (!response.ok) {
        const payload = (await response.json().catch(() => null)) as { error?: string } | null
        notify(`无法播放：${payload?.error || `音频源返回 HTTP ${response.status}`}`)
        return
      }
      const type = (response.headers.get('content-type') || '').toLowerCase().split(';')[0]
      await response.body?.cancel()
      if (type && !type.startsWith('audio/') && !type.startsWith('video/') && type !== 'application/octet-stream') {
        notify(`无法播放：音频源返回了 ${type} 内容。`)
        return
      }
    } catch {
      // A failed probe falls through to the generic message below.
    }
    notify('音频加载失败，可能是链接失效或源站限制了访问。')
  }

  function openCurrentTranscript() {
    const episode = nowPlayingRef.current
    if (!episode) {
      notify('先在列表里选择一集播放。')
      return
    }
    if (detail?.episode.id !== episode.id) {
      pendingTab.current = 'transcript'
      setSelectedId(episode.id)
      setPane('detail')
    } else {
      setTab('transcript')
    }
  }

  const scopeFeed = feeds.find((feed) => feed.id === feedScope) ?? null
  const tagFeeds = feeds.filter((feed) => tagFeedIds.includes(feed.id))
  const newCount = feeds.reduce((sum, feed) => sum + feed.new_count, 0)
  const showOpen = showView != null
  const readerVisible = showOpen ? pane === 'detail' : mode !== 'discover'
  const showDetailVisible = showOpen && !readerVisible

  if (bootError) {
    return (
      <div className="boot-screen" role="alert">
        <h1>BOAI</h1>
        <p>无法载入资料库：{bootError}</p>
        <p className="muted">请确认后端已启动，然后重试。</p>
        <button type="button" className="primary" onClick={() => window.location.reload()}>
          重试
        </button>
      </div>
    )
  }

  if (booting && !status) {
    return (
      <div className="boot-screen" aria-live="polite">
        <h1>BOAI</h1>
        <p className="muted">正在载入本地资料库…</p>
      </div>
    )
  }

  return (
    <>
      <IconDefs />
      <div className="shell" data-pane={pane} data-mode={mode} data-view={showOpen ? 'show' : undefined}>
        <Sidebar
          mode={mode}
          filter={filter}
          feeds={feeds}
          categories={categories}
          newCount={newCount}
          inProgressCount={status?.library.in_progress ?? 0}
          downloadCount={downloads.length}
          refreshing={refreshing}
          onMode={changeMode}
          onFilter={changeFilter}
          onNewCategory={() => openDialog('category')}
          onOpenSettings={() => openDialog('settings')}
          onRefresh={() => void handleRefresh()}
        />
        {mode === 'discover' ? (
          showOpen ? null : (
            <Discover
              feeds={feeds}
              categories={categories}
              focusToken={discoverFocus}
              onMode={changeMode}
              onSubscribed={handleDiscoverSubscribed}
              onImported={() => void reloadAll()}
              onOpenShow={openDiscoverShow}
              onOpenEpisode={openDiscoverEpisode}
              notify={notify}
            />
          )
        ) : (
          <Inbox
            mode={mode}
            filter={filter}
            feeds={feeds}
            episodes={episodes}
            downloads={downloads}
            selectedId={selectedId}
            refreshing={refreshing}
            downloading={downloading}
            scopeFeed={scopeFeed}
            onMode={changeMode}
            onFilter={changeListFilter}
            onSelect={selectEpisode}
            onShow={openShow}
            onClearScope={() => setFeedScope(null)}
            onOpenDiscover={() => changeMode('discover')}
            onOpenTags={(feedId) => {
              setTagFeedIds([feedId])
              openDialog('tags')
            }}
            onBulkTags={(feedIds) => {
              if (feedIds.length === 0) return
              setTagFeedIds(feedIds)
              openDialog('tags')
            }}
            onOpenCategory={() => openDialog('category')}
            onEpisodesState={(ids, action) => void handleEpisodesState(ids, action)}
            onRemoveFeeds={(ids) => void handleRemoveFeeds(ids)}
            onDownload={(list) => void handleDownloadEpisodes(list)}
            onDeleteDownloads={(ids) => void handleDeleteDownloads(ids)}
            onShare={(item) => void handleShare(item)}
            onRefresh={() => void handleRefresh()}
          />
        )}
        {showDetailVisible && showView ? (
          <ShowDetail
            target={showView}
            preview={showPreview}
            loading={showLoading}
            error={showError}
            subscribing={showSubscribing}
            onBack={closeShow}
            onSubscribe={() => void subscribeCurrentShow()}
            onUnsubscribe={() => void unsubscribeCurrentShow()}
            onOpenEpisode={selectEpisode}
            onRetry={() => void openShowTarget(showView)}
          />
        ) : null}
        {readerVisible ? (
        <Reader
          key={detail?.episode.id ?? 'empty'}
          detail={detail}
          loading={detailLoading}
          transcript={transcript}
          transcriptState={transcriptState}
          transcriptError={transcriptError}
          tab={tab}
          playingId={nowPlaying?.id ?? null}
          playing={playing}
          position={position}
          onTab={setTab}
          onPlay={toggleSelectedPlayback}
          onSeek={seekTo}
          onOpenFeed={() => {
            if (detail) openEpisodeShow(detail.episode)
          }}
          onOpenExport={() => openDialog('export')}
          onOpenSettings={() => openDialog('settings')}
          onLoadTranscript={() => void loadTranscript()}
          ai={ai}
          aiStatus={status?.ai}
          aiLoading={aiLoading}
          onGenerateInsight={generateInsight}
          onTranslate={translateTranscript}
          onTranscribe={transcribeEpisode}
          onAsk={askEpisode}
          onRetryTask={(taskId) => void retryAITask(taskId)}
          onBack={closeDetail}
          showBack={showOpen}
        />
        ) : null}
        <Player
          episode={nowPlaying}
          playing={playing}
          position={position}
          duration={duration}
          rateIndex={rateIndex}
          onToggle={togglePlayback}
          onSeek={seekPlayer}
          onSkip={skip}
          onRate={() => setRateIndex((value) => (value + 1) % RATES.length)}
          onOpenTranscript={openCurrentTranscript}
        />
      </div>

      <audio
        ref={audioRef}
        hidden
        preload="metadata"
        onLoadedMetadata={handleLoadedMetadata}
        onCanPlay={handleCanPlay}
        onTimeUpdate={handleTimeUpdate}
        onPlay={() => setPlaying(true)}
        onPause={handlePause}
        onEnded={handleEnded}
        onError={handleAudioError}
      />

      {dialog === 'category' ? (
        <CategoryDialog
          categories={categories}
          onClose={() => closeDialog()}
          onChanged={() => void reloadAll()}
          onDeleted={handleCategoryDeleted}
          notify={notify}
        />
      ) : null}

      {dialog === 'tags' && tagFeeds.length > 0 ? (
        <TagsDialog
          key={tagFeeds.map((feed) => feed.id).join('-')}
          feeds={tagFeeds}
          categories={categories}
          onClose={() => closeDialog()}
          onSaved={() => void reloadAll()}
          notify={notify}
        />
      ) : null}

      {dialog === 'settings' ? (
        <SettingsDialog
          status={status}
          onClose={() => closeDialog()}
          onChanged={() => void loadLibrary()}
          notify={notify}
        />
      ) : null}

      {dialog === 'export' && detail ? (
        <ExportDialog
          episode={detail.episode}
          transcript={transcript}
          onClose={() => closeDialog()}
          notify={notify}
        />
      ) : null}

      <div className="toast" role="status" aria-live="polite" hidden={!toast}>
        {toast}
      </div>
    </>
  )
}
