import type { CSSProperties } from 'react'
import type { Category, Feed } from '../types'
import type { Filter, Mode } from '../ui'
import { Icon } from './Icons'

interface SidebarProps {
  mode: Mode
  filter: Filter
  feeds: Feed[]
  categories: Category[]
  newCount: number
  inProgressCount: number
  downloadCount: number
  refreshing: boolean
  onMode: (mode: Mode) => void
  onFilter: (filter: Filter) => void
  onNewCategory: () => void
  onOpenSettings: () => void
  onRefresh: () => void
}

export function Sidebar(props: SidebarProps) {
  const { mode, filter, feeds, categories, newCount, inProgressCount, downloadCount, refreshing } = props
  return (
    <aside className="side" aria-label="播客导航">
      <div className="brand">
        <span className="brand-mark">
          <Icon name="wave" />
        </span>
        <div>
          <strong>BOAI</strong>
          <small>PODCAST LIBRARY</small>
        </div>
      </div>
      <div className="group-label">资料库</div>
      <nav className="nav-block" aria-label="资料库">
        <button
          type="button"
          className="nav-btn"
          aria-current={mode === 'inbox' && filter !== 'in_progress'}
          onClick={() => props.onMode('inbox')}
        >
          <Icon name="inbox" />
          收件箱
          <small>{newCount}</small>
        </button>
        <button
          type="button"
          className="nav-btn"
          aria-current={mode === 'inbox' && filter === 'in_progress'}
          onClick={() => props.onFilter('in_progress')}
        >
          <Icon name="play" />
          继续收听
          <small>{inProgressCount}</small>
        </button>
        <button
          type="button"
          className="nav-btn"
          aria-current={mode === 'shows'}
          onClick={() => props.onMode('shows')}
        >
          <Icon name="audio" />
          我的订阅
          <small>{feeds.length}</small>
        </button>
        <button
          type="button"
          className="nav-btn"
          aria-current={mode === 'discover'}
          onClick={() => props.onMode('discover')}
        >
          <Icon name="search" />
          订阅播客
        </button>
        <button
          type="button"
          className="nav-btn"
          aria-current={mode === 'downloads'}
          onClick={() => props.onMode('downloads')}
        >
          <Icon name="download" />
          下载
          <small>{downloadCount}</small>
        </button>
      </nav>
      <div className="group-label">
        我的分类
        <button type="button" className="icon" onClick={props.onNewCategory} aria-label="分类管理">
          <Icon name="plus" />
        </button>
      </div>
      <nav className="nav-block" aria-label="我的分类">
        {categories.map((category) => (
          <button
            key={category.id}
            type="button"
            className="nav-btn"
            aria-current={filter === `cat:${category.id}`}
            onClick={() => props.onFilter(`cat:${category.id}`)}
          >
            <span className="dot" style={{ '--dot': category.color || '#6b5ca5' } as CSSProperties} />
            {category.name}
            <small>{category.feed_count}</small>
          </button>
        ))}
        {categories.length === 0 ? <p className="side-empty">还没有分类；节目可以不分类。</p> : null}
      </nav>
      <div className="side-footer">
        <button type="button" className="quiet" onClick={props.onOpenSettings}>
          <Icon name="spark" />
          AI 服务 · 未配置
        </button>
        <button
          type="button"
          className="icon"
          onClick={props.onRefresh}
          aria-label="更新订阅"
          title="更新订阅"
          disabled={refreshing}
        >
          <Icon name="refresh" />
        </button>
      </div>
    </aside>
  )
}
