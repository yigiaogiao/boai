import { useState } from 'react'
import type {
  AIAnalysisPayload,
  AICitation,
  AIInsight,
  AIMapPayload,
  AIQuestion,
  AIStatus,
  AISummaryPayload,
  AITask,
  EpisodeAI,
  Transcript,
} from '../types'
import { clock, episodeDate } from '../format'
import { Icon } from './Icons'

export interface AIActions {
  onGenerate: (kind: 'summary' | 'analysis' | 'map') => void
  onTranslate: (targetLanguage: string) => void
  onTranscribe: () => void
  onAsk: (question: string) => void
  onRetry: (taskId: string) => void
  onOpenSettings: () => void
  onSeek: (seconds: number) => void
  onOpenTranscript: () => void
}

interface AIProps {
  ai: EpisodeAI | null
  status: AIStatus | undefined
  loading: boolean
  actions: AIActions
}

function latestTask(tasks: AITask[], kind: AITask['kind']): AITask | null {
  return tasks.find((task) => task.kind === kind) ?? null
}

function taskLabel(task: AITask): string {
  if (task.status === 'queued') return '排队中，等待处理…'
  if (task.status === 'processing') return task.sends === 'audio' ? '正在上传并转写音频…' : '正在请求文本模型生成…'
  if (task.status === 'completed') return '已完成'
  return task.error ? `失败：${task.error}` : '失败，可重试'
}

export function TaskNotice(props: { task: AITask | null; onRetry: (taskId: string) => void; quietComplete?: boolean }) {
  const task = props.task
  if (!task) return null
  if (task.status === 'completed' && props.quietComplete) return null
  const active = task.status === 'queued' || task.status === 'processing'
  return (
    <div className={`ai-task${task.status === 'failed' ? ' ai-error' : ''}`} role="status">
      <span>{taskLabel(task)}</span>
      {task.provider ? <small>{task.provider}</small> : null}
      {task.status === 'failed' ? (
        <button type="button" className="quiet" onClick={() => props.onRetry(task.id)}>
          重试
        </button>
      ) : null}
      {active ? <span className="ai-pulse" aria-hidden="true" /> : null}
    </div>
  )
}

function providerText(status: AIStatus | undefined, kind: 'text' | 'speech'): string {
  if (!status) return ''
  const config = status[kind]
  if (!config.configured) return ''
  return config.model ? `${config.model}（${config.base_url}）` : config.base_url
}

function Citations(props: { items: AICitation[]; onSeek: (seconds: number) => void }) {
  if (!props.items.length) return null
  return (
    <div className="ai-citations">
      {props.items.map((citation) => (
        <button
          key={`${citation.segment}-${citation.quote}`}
          type="button"
          className="quiet ai-citation"
          onClick={() => props.onSeek(citation.start)}
          title="跳到原声时间"
        >
          <time>{clock(citation.start)}</time>
          <span>“{citation.quote}”</span>
        </button>
      ))}
    </div>
  )
}

function InsightMeta(props: { insight: AIInsight }) {
  return (
    <p className="source-label ai-meta">
      AI 生成 · {props.insight.model || '文本模型'} · {episodeDate(props.insight.updated_at)}
      {props.insight.source === 'ai' ? ' · 请以原文为准' : ''}
    </p>
  )
}

function GenerateCard(props: {
  title: string
  description: string
  status: AIStatus | undefined
  task: AITask | null
  button: string
  onGenerate: () => void
  onOpenSettings: () => void
  onRetry: (taskId: string) => void
  regenerate?: boolean
}) {
  const configured = Boolean(props.status?.text.configured)
  const provider = providerText(props.status, 'text')
  return (
    <section className="text-panel ai-panel">
      <div className="section-top">
        <h3>{props.title}</h3>
        <span className="demo-tag">{configured ? '可生成' : '需要文本模型'}</span>
      </div>
      <p className="muted capability-copy">{props.description}</p>
      <TaskNotice task={props.task} onRetry={props.onRetry} quietComplete />
      {configured ? (
        <>
          <p className="ai-disclosure">
            将把本集文字发送给 {provider}。费用由服务商收取；不发送音频。
          </p>
          <div className="feature-options">
            <button type="button" className="primary" onClick={props.onGenerate} disabled={props.task?.status === 'queued' || props.task?.status === 'processing'}>
              <Icon name="spark" />
              {props.regenerate ? '重新生成' : props.button}
            </button>
          </div>
        </>
      ) : (
        <div className="feature-options">
          <button type="button" className="outline" onClick={props.onOpenSettings}>
            <Icon name="spark" />
            配置文本模型
          </button>
        </div>
      )}
    </section>
  )
}

export function SummaryAI(props: AIProps & { insight: AIInsight | null }) {
  const { actions, status } = props
  if (!props.insight) {
    return (
      <GenerateCard
        title="AI 导读"
        description="生成总览、章节、核心观点和可核对摘录；先展示节目源简介，不要求先生成。"
        status={status}
        task={latestTask(props.ai?.tasks ?? [], 'summary')}
        button="生成导读"
        onGenerate={() => actions.onGenerate('summary')}
        onOpenSettings={actions.onOpenSettings}
        onRetry={actions.onRetry}
      />
    )
  }
  const payload = props.insight.payload as AISummaryPayload
  return (
    <section className="text-panel ai-panel">
      <div className="section-top">
        <h3>AI 导读</h3>
        <span className="demo-tag ai-ready">已生成</span>
      </div>
      <p className="summary-text">{payload.overview}</p>
      <InsightMeta insight={props.insight} />
      {payload.chapters?.length ? (
        <div className="ai-chapters">
          {payload.chapters.map((chapter) => (
            <button
              key={`${chapter.start}-${chapter.label}`}
              type="button"
              className="quiet"
              onClick={() => actions.onSeek(chapter.start)}
            >
              <time>{clock(chapter.start)}</time>
              <span>{chapter.label}</span>
            </button>
          ))}
        </div>
      ) : null}
      {payload.points?.length ? (
        <div className="ai-points">
          <h4>核心观点</h4>
          {payload.points.map((point, index) => (
            <div key={`${point.text}-${index}`} className="ai-point">
              <p>{point.text}</p>
              <Citations items={point.citations ?? []} onSeek={actions.onSeek} />
            </div>
          ))}
        </div>
      ) : null}
      {payload.quotes?.length ? (
        <div className="ai-points">
          <h4>可核对摘录</h4>
          <Citations items={payload.quotes} onSeek={actions.onSeek} />
        </div>
      ) : null}
      <TaskNotice task={latestTask(props.ai?.tasks ?? [], 'summary')} onRetry={actions.onRetry} quietComplete />
      {status?.text.configured ? (
        <button type="button" className="quiet guide-back" onClick={() => actions.onGenerate('summary')}>
          重新生成
        </button>
      ) : null}
    </section>
  )
}

export function AnalysisAI(props: AIProps & { insight: AIInsight | null }) {
  if (!props.insight) {
    return (
      <GenerateCard
        title="精读"
        description="围绕本集主题展开论点与依据；每个可核对引用都能回到逐字稿时间点。"
        status={props.status}
        task={latestTask(props.ai?.tasks ?? [], 'analysis')}
        button="生成精读"
        onGenerate={() => props.actions.onGenerate('analysis')}
        onOpenSettings={props.actions.onOpenSettings}
        onRetry={props.actions.onRetry}
      />
    )
  }
  const payload = props.insight.payload as AIAnalysisPayload
  return (
    <section className="text-panel ai-panel">
      <div className="section-top">
        <h3>{payload.title || '精读'}</h3>
        <span className="demo-tag ai-ready">已生成</span>
      </div>
      {payload.summary ? <p className="summary-text">{payload.summary}</p> : null}
      <InsightMeta insight={props.insight} />
      {payload.sections?.map((section, index) => (
        <section key={`${section.heading}-${index}`} className="ai-section">
          <h4>{section.heading}</h4>
          <p>{section.body}</p>
          <Citations items={section.citations ?? []} onSeek={props.actions.onSeek} />
        </section>
      ))}
      {payload.takeaways?.length ? (
        <div className="ai-points">
          <h4>可带走的结论</h4>
          {payload.takeaways.map((item, index) => (
            <div key={`${item.text}-${index}`} className="ai-point">
              <p>{item.text}</p>
              <Citations items={item.citations ?? []} onSeek={props.actions.onSeek} />
            </div>
          ))}
        </div>
      ) : null}
      <TaskNotice task={latestTask(props.ai?.tasks ?? [], 'analysis')} onRetry={props.actions.onRetry} quietComplete />
      {props.status?.text.configured ? (
        <button type="button" className="quiet guide-back" onClick={() => props.actions.onGenerate('analysis')}>
          重新生成
        </button>
      ) : null}
    </section>
  )
}

export function MapAI(props: AIProps & { insight: AIInsight | null }) {
  if (!props.insight) {
    return (
      <GenerateCard
        title="导图"
        description="把本集主题整理成可直接阅读的两级结构，不把普通章节冒充成 AI 导图。"
        status={props.status}
        task={latestTask(props.ai?.tasks ?? [], 'map')}
        button="生成导图"
        onGenerate={() => props.actions.onGenerate('map')}
        onOpenSettings={props.actions.onOpenSettings}
        onRetry={props.actions.onRetry}
      />
    )
  }
  const payload = props.insight.payload as AIMapPayload
  return (
    <section className="text-panel ai-panel">
      <div className="section-top">
        <h3>{payload.title || '导图'}</h3>
        <span className="demo-tag ai-ready">已生成</span>
      </div>
      <InsightMeta insight={props.insight} />
      <div className="ai-map">
        {payload.branches?.map((branch, index) => (
          <section key={`${branch.title}-${index}`} className="ai-map-branch">
            <h4>{branch.title}</h4>
            <ul>
              {branch.children?.map((child, childIndex) => (
                <li key={`${child.title}-${childIndex}`}>
                  <span>{child.title}</span>
                  <Citations items={child.citations ?? []} onSeek={props.actions.onSeek} />
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>
      <TaskNotice task={latestTask(props.ai?.tasks ?? [], 'map')} onRetry={props.actions.onRetry} quietComplete />
      {props.status?.text.configured ? (
        <button type="button" className="quiet guide-back" onClick={() => props.actions.onGenerate('map')}>
          重新生成
        </button>
      ) : null}
    </section>
  )
}

export function AskAI(props: AIProps) {
  const [question, setQuestion] = useState('')
  const textReady = Boolean(props.status?.text.configured)
  const active = (props.ai?.tasks ?? []).some(
    (task) => task.kind === 'ask' && (task.status === 'queued' || task.status === 'processing'),
  )
  const questions = props.ai?.questions ?? []
  return (
    <section className="text-panel ai-panel">
      <div className="section-top">
        <h3>问这集</h3>
        <span className="demo-tag">{textReady ? '限定本集' : '需要文本模型'}</span>
      </div>
      {!props.loading && !props.ai ? <p className="muted">正在读取本集问答记录…</p> : null}
      {textReady ? (
        <>
          <p className="ai-disclosure">
            只把本集逐字稿发送给 {providerText(props.status, 'text')}，不会检索其他节目。费用由服务商收取。
          </p>
          <form
            className="note-form"
            onSubmit={(event) => {
              event.preventDefault()
              const value = question.trim()
              if (!value) return
              props.actions.onAsk(value)
              setQuestion('')
            }}
          >
            <label className="sr-only" htmlFor="episode-question">
              单集问题
            </label>
            <input
              id="episode-question"
              value={question}
              onChange={(event) => setQuestion(event.target.value)}
              placeholder="问这集：例如，这集对倾听的定义是什么？"
              maxLength={1000}
              disabled={active}
            />
            <button className="primary" type="submit" disabled={!question.trim() || active}>
              提问
            </button>
          </form>
        </>
      ) : (
        <div className="feature-options">
          <button type="button" className="outline" onClick={props.actions.onOpenSettings}>
            <Icon name="spark" />
            配置文本模型
          </button>
        </div>
      )}
      <TaskNotice task={latestTask(props.ai?.tasks ?? [], 'ask')} onRetry={props.actions.onRetry} />
      {questions.length ? (
        <div className="ai-answers">
          {questions.map((item) => (
            <QuestionCard key={item.id} item={item} onSeek={props.actions.onSeek} />
          ))}
        </div>
      ) : (
        <p className="muted">还没有问答记录。回答必须能在本集逐字稿中核对；证据不足时会直接说明。</p>
      )}
    </section>
  )
}

function QuestionCard(props: { item: AIQuestion; onSeek: (seconds: number) => void }) {
  return (
    <article className={`ai-answer${props.item.grounded ? '' : ' ungrounded'}`}>
      <header>
        <strong>{props.item.question}</strong>
        <span className="demo-tag">{props.item.grounded ? '有依据' : '证据不足'}</span>
      </header>
      <p>{props.item.answer}</p>
      <Citations items={props.item.citations ?? []} onSeek={props.onSeek} />
      <small>
        {props.item.model} · {episodeDate(props.item.created_at)}
      </small>
    </article>
  )
}

export function TranslationAction(props: AIProps & { transcript: Transcript }) {
  const [target, setTarget] = useState('中文（简体）')
  const configured = Boolean(props.status?.text.configured)
  const task = latestTask(props.ai?.tasks ?? [], 'translation')
  const active = task?.status === 'queued' || task?.status === 'processing'
  return (
    <div className="ai-inline-action">
      <TaskNotice task={task} onRetry={props.actions.onRetry} />
      {configured ? (
        <>
          <label className="sr-only" htmlFor="translation-target">
            译文语言
          </label>
          <input
            id="translation-target"
            value={target}
            maxLength={40}
            onChange={(event) => setTarget(event.target.value)}
          />
          <button
            type="button"
            className="outline"
            disabled={active || !target.trim()}
            onClick={() => props.actions.onTranslate(target.trim())}
          >
            <Icon name="spark" />
            {props.transcript.segments.some((segment) => segment.translation) ? '重新生成译文' : '生成译文'}
          </button>
          <small className="muted">只发送原文文字，不重传音频；费用由服务商收取。</small>
        </>
      ) : (
        <button type="button" className="quiet" onClick={props.actions.onOpenSettings}>
          <Icon name="spark" />
          配置文本模型后可生成译文
        </button>
      )}
    </div>
  )
}

export function TranscriptionAction(props: AIProps & { sourcesAvailable: boolean }) {
  const configured = Boolean(props.status?.speech.configured)
  const task = latestTask(props.ai?.tasks ?? [], 'transcription')
  const active = task?.status === 'queued' || task?.status === 'processing'
  return (
    <div className="ai-transcribe">
      <p className="muted">
        节目源没有现成逐字稿。直接收听不受影响。
        {props.sourcesAvailable ? '也可以先尝试获取节目源提供的逐字稿。' : ''}
      </p>
      <TaskNotice task={task} onRetry={props.actions.onRetry} />
      {configured ? (
        <>
          <p className="ai-disclosure">
            将把本集音频发送给 {providerText(props.status, 'speech')}，费用按音频时长由服务商收取。
          </p>
          <div className="feature-options">
            <button
              type="button"
              className="outline"
              disabled={active}
              onClick={props.actions.onTranscribe}
            >
              <Icon name="cc" />
              云端转写本集
            </button>
          </div>
        </>
      ) : (
        <div className="feature-options">
          <button type="button" className="outline" onClick={props.actions.onOpenSettings}>
            <Icon name="spark" />
            配置语音转写
          </button>
        </div>
      )}
    </div>
  )
}
