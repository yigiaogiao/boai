import { useEffect, useState } from 'react'
import type { ShowPreview } from '../types'
import { durationLabel, episodeDate, stripHTML } from '../format'
import { Cover } from './Cover'
import { Icon } from './Icons'

export interface ShowTarget {
  feedUrl: string
  title: string
  artist?: string
  artwork?: string
  source?: string
}

interface ShowDetailProps {
  target: ShowTarget
  preview: ShowPreview | null
  loading: boolean
  error: string
  subscribing: boolean
  onBack: () => void
  onSubscribe: () => void
  onUnsubscribe: () => void
  onOpenEpisode: (episodeId: number) => void
  onRetry: () => void
}

export function ShowDetail(props: ShowDetailProps) {
  const [expanded, setExpanded] = useState(false)
  const feed = props.preview?.feed
  const subscribed = Boolean(props.preview?.subscribed)
  const title = feed?.title || props.target.title
  const artwork = feed?.image_url || props.target.artwork || ''
  const description = stripHTML(feed?.description || '')
  const longDescription = description.length > 180
  const episodes = props.preview?.episodes ?? []
  const artist = props.target.artist || ''
  const totalCount = feed?.episode_count ?? 0
  const listSummary =
    totalCount > episodes.length
      ? `显示最近 ${episodes.length} 集 · 共 ${totalCount} 集`
      : `${episodes.length} 集 · 按发布时间`

  useEffect(() => {
    setExpanded(false)
  }, [props.target.feedUrl])

  return (
    <main className="show-page" aria-label="播客详情">
      <div className="show-page-inner">
        <div className="show-page-top">
          <button className="quiet back" type="button" onClick={props.onBack}>
            <Icon name="back" />
            返回
          </button>
        </div>

        <header className="show-hero">
          <Cover imageURL={artwork} title={title} large />
          <div className="show-hero-copy">
            <h1>{title}</h1>
            {artist ? <p className="show-artist">{artist}</p> : null}
            {description ? (
              <>
                <p className={`show-description${longDescription && !expanded ? ' clamped' : ''}`}>
                  {description}
                </p>
                {longDescription ? (
                  <button
                    type="button"
                    className="quiet show-description-toggle"
                    aria-expanded={expanded}
                    onClick={() => setExpanded((value) => !value)}
                  >
                    {expanded ? '收起介绍' : '展开介绍'}
                  </button>
                ) : null}
              </>
            ) : (
              <p className="show-description muted">
                {props.loading ? '正在读取节目介绍…' : '节目源没有提供介绍。'}
              </p>
            )}
            <div className="show-meta">
              <span>{feed ? `${feed.episode_count} 集` : '节目详情'}</span>
              {feed?.site_url ? (
                <>
                  <span>·</span>
                  <a href={feed.site_url} target="_blank" rel="noreferrer noopener">
                    节目主页
                  </a>
                </>
              ) : null}
              {!props.loading && feed ? (
                <>
                  <span>·</span>
                  <span>{subscribed ? '已加入我的订阅' : '尚未订阅'}</span>
                </>
              ) : null}
            </div>
            <div className="show-actions">
              {subscribed ? (
                <>
                  <span className="subscribed-mark">
                    <Icon name="check" />
                    已订阅
                  </span>
                  <button
                    type="button"
                    className="quiet danger-text"
                    disabled={props.subscribing}
                    onClick={props.onUnsubscribe}
                  >
                    取消关注
                  </button>
                </>
              ) : (
                <>
                  <button
                    type="button"
                    className="primary"
                    disabled={props.subscribing || props.loading}
                    onClick={props.onSubscribe}
                  >
                    {props.subscribing ? '订阅中…' : '订阅节目'}
                  </button>
                  <span className="show-action-note">订阅后新单集会进入收件箱</span>
                </>
              )}
            </div>
          </div>
        </header>

        {props.error ? (
          <div className="show-error" role="status">
            <span>{props.error}</span>
            <button type="button" className="outline" onClick={props.onRetry}>
              重试
            </button>
          </div>
        ) : null}

        <section className="show-episodes" aria-labelledby="show-episodes-title">
          <div className="show-episodes-head">
            <h2 id="show-episodes-title">节目列表</h2>
            <span>
              {props.loading
                ? '正在读取'
                : props.preview
                  ? listSummary
                  : ''}
            </span>
          </div>
          {props.loading ? <p className="muted show-loading">正在读取节目单集…</p> : null}
          {!props.loading && !props.error && episodes.length === 0 ? (
            <p className="muted show-loading">这个节目还没有可播放的单集。</p>
          ) : null}
          <div className="show-episode-list">
            {episodes.map((episode) => {
              const progress = episode.progress_seconds
              const resume = progress > 0 && !episode.completed
              return (
                <button
                  key={episode.id}
                  type="button"
                  className="show-episode-row"
                  onClick={() => props.onOpenEpisode(episode.id)}
                >
                  <span className="show-episode-play" aria-hidden="true">
                    <Icon name="play" />
                  </span>
                  <span className="show-episode-copy">
                    <strong>{episode.title}</strong>
                    <span>
                      {episodeDate(episode.published_at)}
                      <span>·</span>
                      {durationLabel(episode.duration_seconds)}
                      {resume ? (
                        <>
                          <span>·</span>
                          <b>继续收听</b>
                        </>
                      ) : episode.completed ? (
                        <>
                          <span>·</span>
                          <b>已听完</b>
                        </>
                      ) : null}
                    </span>
                  </span>
                  <Icon name="chevron" />
                </button>
              )
            })}
          </div>
        </section>
      </div>
    </main>
  )
}
