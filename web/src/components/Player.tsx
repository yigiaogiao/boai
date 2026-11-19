import type { Episode } from '../types'
import { clock } from '../format'
import { Cover } from './Cover'
import { Icon } from './Icons'

const RATES = [1, 1.25, 1.5, 2]

interface PlayerProps {
  episode: Episode | null
  playing: boolean
  position: number
  duration: number
  rateIndex: number
  onToggle: () => void
  onSeek: (seconds: number) => void
  onSkip: (delta: number) => void
  onRate: () => void
  onOpenTranscript: () => void
}

export function Player(props: PlayerProps) {
  const { episode, playing, position, duration } = props
  const total = duration || episode?.duration_seconds || 0
  const rate = RATES[props.rateIndex] ?? 1
  return (
    <footer className="player" aria-label="播放器">
      <div className="playing-info">
        {episode ? (
          <Cover imageURL={episode.image_url || episode.feed_image_url} title={episode.feed_title} />
        ) : (
          <div className="cover" aria-hidden="true">
            <svg>
              <use href="#cover-art" />
            </svg>
          </div>
        )}
        <div>
          <strong>{episode ? episode.title : '选择一集开始收听'}</strong>
          <small>{episode ? `${episode.feed_title} · 单集播放` : '支持续播与后台保存进度'}</small>
        </div>
      </div>
      <div className="transport">
        <div className="transport-controls">
          <button
            type="button"
            className="icon"
            onClick={() => props.onSkip(-15)}
            aria-label="后退 15 秒"
            disabled={!episode}
          >
            <Icon name="rewind" />
          </button>
          <button
            type="button"
            className="round"
            onClick={props.onToggle}
            aria-label={playing ? '暂停' : '播放'}
            disabled={!episode}
          >
            <Icon name={playing ? 'pause' : 'play'} />
          </button>
          <button
            type="button"
            className="icon"
            onClick={() => props.onSkip(30)}
            aria-label="前进 30 秒"
            disabled={!episode}
          >
            <Icon name="forward" />
          </button>
        </div>
        <div className="timeline">
          <time>{clock(position)}</time>
          <input
            type="range"
            min={0}
            max={Math.max(1, Math.floor(total))}
            step={1}
            value={Math.min(position, Math.max(0, total))}
            aria-label="播放进度"
            aria-valuetext={`${clock(position)} / ${clock(total)}`}
            disabled={!episode}
            onChange={(event) => props.onSeek(Number(event.target.value))}
          />
          <time>{total ? clock(total) : '--:--'}</time>
        </div>
      </div>
      <div className="player-extra">
        <span>{episode?.audio_type ? episode.audio_type.replace(/^audio\//, '').toUpperCase() : '音频'}</span>
        <button
          type="button"
          className="icon"
          onClick={props.onOpenTranscript}
          aria-label="打开正在播放的字幕"
          disabled={!episode}
        >
          <Icon name="cc" />
        </button>
        <button
          type="button"
          className="quiet"
          onClick={props.onRate}
          aria-label="播放速度"
          disabled={!episode}
        >
          {rate}×
        </button>
      </div>
    </footer>
  )
}
