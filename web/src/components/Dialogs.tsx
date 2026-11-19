import { useEffect, useId, useRef, useState } from 'react'
import type { Category, Episode, Feed, StatusResponse, Transcript } from '../types'
import * as api from '../api'
import { clock, download, episodeDate, safeFilename, srtTimestamp, stripHTML } from '../format'
import { Icon } from './Icons'

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : '操作失败，请稍后重试'
}

function Modal(props: {
  title: string
  wide?: boolean
  onClose: () => void
  children: React.ReactNode
}) {
  const titleId = useId()
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const dialog = ref.current
    if (dialog && !dialog.open) dialog.showModal()
    return () => {
      if (dialog?.open) dialog.close()
    }
  }, [])
  return (
    <dialog
      ref={ref}
      className={props.wide ? 'wider-dialog' : undefined}
      aria-labelledby={titleId}
      onCancel={(event) => {
        event.preventDefault()
        props.onClose()
      }}
      onClick={(event) => {
        if (event.target === event.currentTarget) props.onClose()
      }}
    >
      <header>
        <h2 id={titleId}>{props.title}</h2>
        <button type="button" className="icon" onClick={props.onClose} aria-label="关闭">
          <Icon name="close" />
        </button>
      </header>
      {props.children}
    </dialog>
  )
}

interface CategoryDialogProps {
  categories: Category[]
  onClose: () => void
  onChanged: () => void
  onDeleted: (categoryId: number) => void
  notify: (message: string) => void
}

const RESERVED_NAMES = ['全部', '未听', '继续听']

export function CategoryDialog(props: CategoryDialogProps) {
  const [name, setName] = useState('')
  const [selectedId, setSelectedId] = useState<number | null>(props.categories[0]?.id ?? null)
  const [renameName, setRenameName] = useState(props.categories[0]?.name ?? '')
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')

  function validate(value: string): boolean {
    if (!value) {
      setMessage('请输入分类名称。')
      return false
    }
    if (RESERVED_NAMES.includes(value)) {
      setMessage('这个名称是内置筛选，换一个吧。')
      return false
    }
    return true
  }

  async function create(event: React.FormEvent) {
    event.preventDefault()
    const value = name.trim()
    if (!validate(value)) return
    if (props.categories.some((category) => category.name === value)) {
      setMessage('已经有同名分类了。')
      return
    }
    setBusy(true)
    setMessage('')
    try {
      await api.createCategory(value)
      setName('')
      props.onChanged()
      props.notify('分类已创建')
    } catch (error) {
      setMessage(messageOf(error))
    } finally {
      setBusy(false)
    }
  }

  async function rename() {
    if (selectedId == null) return
    const value = renameName.trim()
    if (!validate(value)) return
    if (props.categories.some((category) => category.id !== selectedId && category.name === value)) {
      setMessage('已经有同名分类了。')
      return
    }
    setBusy(true)
    setMessage('')
    try {
      await api.renameCategory(selectedId, value)
      props.onChanged()
      props.notify('分类已改名')
    } catch (error) {
      setMessage(messageOf(error))
    } finally {
      setBusy(false)
    }
  }

  async function remove() {
    if (selectedId == null) return
    const category = props.categories.find((item) => item.id === selectedId)
    if (!category) return
    if (!window.confirm(`删除分类「${category.name}」？订阅不会被取消。`)) return
    setBusy(true)
    setMessage('')
    try {
      await api.deleteCategory(selectedId)
      props.onDeleted(selectedId)
      props.onChanged()
      props.notify('分类已删除，订阅保留')
    } catch (error) {
      setMessage(messageOf(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title="分类管理" onClose={props.onClose}>
      <form onSubmit={create}>
        <p>分类由你决定；同一个节目可以放入多个分类。节目分类在上方「分类」或订阅列表的「⋯」里设置。</p>
        <label className="sr-only" htmlFor="category-name">
          分类名称
        </label>
        <input
          id="category-name"
          placeholder="比如：通勤时听"
          maxLength={20}
          required
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
        {message ? (
          <p className="dialog-result" role="status">
            {message}
          </p>
        ) : null}
        <footer>
          <button type="button" className="outline" onClick={props.onClose}>
            取消
          </button>
          <button type="submit" className="primary" disabled={busy}>
            创建分类
          </button>
        </footer>
      </form>
      {props.categories.length > 0 ? (
        <details className="category-management">
          <summary>管理已有分类</summary>
          <p>改名或删除；删除分类不会取消订阅。</p>
          <label className="sr-only" htmlFor="category-manage">
            选择已有分类
          </label>
          <select
            id="category-manage"
            value={selectedId ?? ''}
            onChange={(event) => {
              const id = Number(event.target.value)
              setSelectedId(id)
              setRenameName(props.categories.find((category) => category.id === id)?.name ?? '')
            }}
          >
            {props.categories.map((category) => (
              <option key={category.id} value={category.id}>
                {category.name}
              </option>
            ))}
          </select>
          <div className="feature-options">
            <label className="sr-only" htmlFor="category-rename">
              新分类名称
            </label>
            <input
              id="category-rename"
              maxLength={20}
              placeholder="新名称"
              value={renameName}
              onChange={(event) => setRenameName(event.target.value)}
            />
            <button type="button" className="outline" onClick={rename} disabled={busy}>
              改名
            </button>
            <button type="button" className="outline" onClick={remove} disabled={busy}>
              删除
            </button>
          </div>
        </details>
      ) : null}
    </Modal>
  )
}

interface TagsDialogProps {
  feeds: Feed[]
  categories: Category[]
  onClose: () => void
  onSaved: () => void
  notify: (message: string) => void
}

function commonCategoryIDs(feeds: Feed[]): number[] {
  if (feeds.length === 0) return []
  const common = new Set(feeds[0].categories.map((category) => category.id))
  for (const feed of feeds.slice(1)) {
    const owned = new Set(feed.categories.map((category) => category.id))
    for (const id of [...common]) {
      if (!owned.has(id)) common.delete(id)
    }
  }
  return [...common]
}

export function TagsDialog(props: TagsDialogProps) {
  const [selected, setSelected] = useState<number[]>(commonCategoryIDs(props.feeds))
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const bulk = props.feeds.length > 1

  async function save(event: React.FormEvent) {
    event.preventDefault()
    setBusy(true)
    setMessage('')
    try {
      if (bulk) {
        await api.setFeedsCategories(
          props.feeds.map((feed) => feed.id),
          selected,
        )
      } else {
        await api.setFeedCategories(props.feeds[0].id, selected)
      }
      props.onSaved()
      props.notify(bulk ? `已更新 ${props.feeds.length} 个节目的分类` : '节目分类已更新')
      props.onClose()
    } catch (error) {
      setMessage(messageOf(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title="节目分类" onClose={props.onClose}>
      <form onSubmit={save}>
        <p>
          {bulk
            ? `已选 ${props.feeds.length} 个节目 · 保存后会替换这些节目的全部分类`
            : `${props.feeds[0]?.title ?? ''} · 分类应用于整个节目`}
        </p>
        {props.categories.length > 0 ? (
          <div className="checks">
            {props.categories.map((category) => (
              <label key={category.id}>
                <input
                  type="checkbox"
                  checked={selected.includes(category.id)}
                  onChange={(event) =>
                    setSelected((current) =>
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
        ) : (
          <p className="muted">还没有分类。先到「我的订阅 → 分类」里创建一个。</p>
        )}
        {message ? (
          <p className="dialog-result" role="status">
            {message}
          </p>
        ) : null}
        <footer>
          <button type="button" className="outline" onClick={props.onClose}>
            取消
          </button>
          <button type="submit" className="primary" disabled={busy || props.categories.length === 0}>
            保存分类
          </button>
        </footer>
      </form>
    </Modal>
  )
}

interface SettingsDialogProps {
  status: StatusResponse | null
  onClose: () => void
  onChanged: () => void
  notify: (message: string) => void
}

function AIConfigCard(props: {
  kind: 'text' | 'speech'
  title: string
  description: string
  config: import('../types').AIProviderStatus
  secretReady: boolean
  onChanged: () => void
  notify: (message: string) => void
}) {
  const id = useId()
  const [baseURL, setBaseURL] = useState(props.config.base_url)
  const [model, setModel] = useState(props.config.model)
  const [apiKey, setAPIKey] = useState('')
  const [busy, setBusy] = useState('')
  const [message, setMessage] = useState('')
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    setBaseURL(props.config.base_url)
    setModel(props.config.model)
  }, [props.config.base_url, props.config.model, props.config.updated_at])

  async function save(event: React.FormEvent) {
    event.preventDefault()
    setBusy('save')
    setMessage('')
    setFailed(false)
    try {
      await api.saveAISetting(props.kind, {
        provider: 'openai-compatible',
        base_url: baseURL.trim(),
        model: model.trim(),
        api_key: apiKey.trim(),
      })
      setAPIKey('')
      setMessage('已保存。Key 只保存在本机，不会回传到页面。')
      props.onChanged()
      props.notify(`${props.title}配置已保存`)
    } catch (error) {
      setFailed(true)
      setMessage(messageOf(error))
    } finally {
      setBusy('')
    }
  }

  async function test() {
    setBusy('test')
    setMessage('')
    setFailed(false)
    try {
      const result = await api.testAISetting(props.kind)
      setFailed(!result.ok)
      setMessage(`${result.message}${result.disclosure ? ` ${result.disclosure}` : ''}`)
    } catch (error) {
      setFailed(true)
      setMessage(messageOf(error))
    } finally {
      setBusy('')
    }
  }

  async function remove() {
    if (!window.confirm(`删除${props.title}的 API Key？已经生成的内容会保留。`)) return
    setBusy('delete')
    setMessage('')
    setFailed(false)
    try {
      await api.deleteAISetting(props.kind)
      setAPIKey('')
      setMessage('已删除 Key；历史导读、问答和逐字稿仍可查看。')
      props.onChanged()
      props.notify(`${props.title} Key 已删除`)
    } catch (error) {
      setFailed(true)
      setMessage(messageOf(error))
    } finally {
      setBusy('')
    }
  }

  return (
    <section className="ai-config-card">
      <div className="section-top">
        <h3>{props.title}</h3>
        <span className={`demo-tag${props.config.configured ? ' ai-ready' : ''}`}>
          {props.config.configured ? `已配置 · ${props.config.model}` : '未配置'}
        </span>
      </div>
      <p className="muted">{props.description}</p>
      <form className="ai-config-form" onSubmit={save}>
        <label htmlFor={`${id}-base`}>Base URL（OpenAI 兼容接口）</label>
        <input
          id={`${id}-base`}
          type="url"
          placeholder="https://api.example.com/v1"
          value={baseURL}
          onChange={(event) => setBaseURL(event.target.value)}
          autoComplete="off"
        />
        <label htmlFor={`${id}-model`}>模型</label>
        <input
          id={`${id}-model`}
          type="text"
          placeholder={props.kind === 'text' ? '例如 gpt-4.1-mini' : '例如 whisper-1'}
          value={model}
          onChange={(event) => setModel(event.target.value)}
          autoComplete="off"
        />
        <label htmlFor={`${id}-key`}>API Key</label>
        <input
          id={`${id}-key`}
          type="password"
          placeholder={props.config.key_set ? '已保存；留空保持不变' : '首次保存需要填写 Key'}
          value={apiKey}
          onChange={(event) => setAPIKey(event.target.value)}
          autoComplete="new-password"
        />
        <div className="ai-config-actions">
          <button className="primary" type="submit" disabled={Boolean(busy) || !props.secretReady}>
            {busy === 'save' ? '保存中…' : '保存'}
          </button>
          <button
            className="outline"
            type="button"
            onClick={() => void test()}
            disabled={Boolean(busy) || !props.config.configured}
          >
            {busy === 'test' ? '测试中…' : '测试连接'}
          </button>
          <button
            className="quiet danger-text"
            type="button"
            onClick={() => void remove()}
            disabled={Boolean(busy) || !props.config.configured}
          >
            {busy === 'delete' ? '删除中…' : '删除 Key'}
          </button>
        </div>
      </form>
      {message ? (
        <p className={`dialog-result${failed ? ' ai-error' : ''}`} role="status">
          {message}
        </p>
      ) : null}
    </section>
  )
}

export function SettingsDialog(props: SettingsDialogProps) {
  const ai = props.status?.ai
  const library = props.status?.library
  return (
    <Modal title="AI 服务" wide onClose={props.onClose}>
      <p>
        <span className="demo-tag">{ai?.configured ? '已配置' : '未配置 · 可选'}</span>
      </p>
      <p className="capability-copy">
        不用配置也能订阅、分类、播放、搜索和阅读节目提供的内容。只有主动生成新内容或转写时，才会把对应文字或音频
        发给所选服务商；费用由服务商收取，本应用不代收。
      </p>
      {ai && !ai.secret_ready ? (
        <p className="dialog-result ai-error" role="alert">
          服务端缺少 AI Key 加密密钥，暂时不能保存配置。请检查部署数据目录或 BOAI_SECRET。
        </p>
      ) : null}
      {ai ? (
        <div className="ai-config-grid">
          <AIConfigCard
            kind="text"
            title="文本模型"
            description="生成导读、精读、导图、译文和单集问答。发起任务时会发送本集文字，不会发送音频。"
            config={ai.text}
            secretReady={ai.secret_ready}
            onChanged={props.onChanged}
            notify={props.notify}
          />
          <AIConfigCard
            kind="speech"
            title="语音转写"
            description="只有本集没有可用来源逐字稿时才需要。发起转写会发送本集音频，费用按音频时长计算。"
            config={ai.speech}
            secretReady={ai.secret_ready}
            onChanged={props.onChanged}
            notify={props.notify}
          />
        </div>
      ) : (
        <p className="muted">正在读取 AI 配置…</p>
      )}
      <p className="muted ai-cost-note">
        {ai?.message || 'AI 功能是可选的。'} 测试连接和生成任务都可能产生服务商费用；测试只发送极短、与本集无关的内容。
      </p>
      {library ? (
        <p className="muted">
          本地资料库：{library.feeds} 个节目 · {library.episodes} 集
        </p>
      ) : null}
    </Modal>
  )
}

interface ExportDialogProps {
  episode: Episode
  transcript: Transcript | null
  onClose: () => void
  notify: (message: string) => void
}

export function ExportDialog(props: ExportDialogProps) {
  const episode = props.episode
  const description = stripHTML(episode.description ?? '')

  function exportSummary() {
    const lines = [
      `# ${episode.title}`,
      '',
      `- 节目：${episode.feed_title}`,
      `- 日期：${episodeDate(episode.published_at)}`,
      `- 时长：${clock(episode.duration_seconds)}`,
    ]
    if (episode.link) lines.push(`- 链接：${episode.link}`)
    lines.push('', '## 节目简介', '', description || '节目源没有提供简介。')
    if (episode.chapters.length > 0) {
      lines.push('', '## 章节', '')
      for (const chapter of episode.chapters) lines.push(`- ${clock(chapter.start)} ${chapter.label}`)
    }
    lines.push('', '> 导出内容来自节目 RSS；不含 AI 生成内容。', '')
    download(`${safeFilename(episode.title)}.md`, lines.join('\n'))
    props.notify('已导出摘要 Markdown')
  }

  function exportTranscript() {
    if (!props.transcript) return
    const lines = [
      `# ${episode.title}`,
      '',
      `- 节目：${episode.feed_title}`,
      `- 逐字稿来源：${props.transcript.source || '节目提供'}`,
      '',
    ]
    for (const segment of props.transcript.segments) {
      const time = props.transcript.timed ? `${clock(segment.start)} ` : ''
      lines.push(`- ${time}${segment.speaker ? `${segment.speaker}：` : ''}${segment.text}`)
      if (segment.translation) lines.push(`  - ${segment.translation}`)
    }
    download(`${safeFilename(episode.title)}-transcript.md`, lines.join('\n'))
    props.notify('已导出逐字稿 Markdown')
  }

  function exportSRT() {
    if (!props.transcript?.timed) return
    const segments = props.transcript.segments
    const blocks = segments.map((segment, index) => {
      const next = segments[index + 1]?.start
      const end = Math.min(next ?? segment.start + 10, episode.duration_seconds || next || segment.start + 10)
      return `${index + 1}\n${srtTimestamp(segment.start)} --> ${srtTimestamp(end)}\n${segment.speaker ? `${segment.speaker}：` : ''}${segment.text}\n`
    })
    download(`${safeFilename(episode.title)}.srt`, blocks.join('\n'))
    props.notify('已导出 SRT 字幕')
  }

  return (
    <Modal title="导出与连接" onClose={props.onClose}>
      <p>把节目带进你的笔记工具。</p>
      <div className="feature-options">
        <button type="button" className="outline" onClick={exportSummary}>
          摘要 Markdown
        </button>
        <button
          type="button"
          className="outline"
          onClick={exportTranscript}
          disabled={!props.transcript}
          title={props.transcript ? undefined : '这集还没有逐字稿'}
        >
          逐字稿 Markdown
        </button>
        <button
          type="button"
          className="outline"
          onClick={exportSRT}
          disabled={!props.transcript?.timed}
          title={props.transcript?.timed ? undefined : '这集没有带时间轴的逐字稿'}
        >
          字幕 SRT
        </button>
      </div>
      <p className="muted">
        Notion、Readwise、Obsidian、Logseq 尚未接入，这里不会显示为已连接；首版也不依赖它们。
      </p>
    </Modal>
  )
}
