export function clock(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds || 0))
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const secs = total % 60
  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, '0')}:${String(secs).padStart(2, '0')}`
  }
  return `${String(minutes).padStart(2, '0')}:${String(secs).padStart(2, '0')}`
}

export function durationLabel(seconds: number): string {
  if (!seconds || seconds <= 0) return '时长未知'
  const minutes = Math.round(seconds / 60)
  if (minutes < 1) return '不到 1 分钟'
  if (minutes < 60) return `${minutes} 分钟`
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  return rest ? `${hours} 小时 ${rest} 分钟` : `${hours} 小时`
}

export function bytesLabel(bytes: number): string {
  if (!bytes || bytes <= 0) return '大小未知'
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB']
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  const rounded = value >= 100 ? Math.round(value) : Math.round(value * 10) / 10
  return `${rounded} ${units[unit]}`
}

export function episodeDate(unixSeconds: number): string {
  if (!unixSeconds) return '日期未知'
  const date = new Date(unixSeconds * 1000)
  const now = new Date()
  const sameYear = date.getFullYear() === now.getFullYear()
  const label = `${date.getMonth() + 1} 月 ${date.getDate()} 日`
  return sameYear ? label : `${date.getFullYear()} 年 ${label}`
}

export function stripHTML(value: string): string {
  if (!value) return ''
  const container = document.createElement('textarea')
  container.innerHTML = value
    .replace(/<br\s*\/?>/gi, '\n')
    .replace(/<\/(p|div|li|h[1-6])>/gi, '\n')
  return container.value.replace(/\n{3,}/g, '\n\n').trim()
}

export function truncate(value: string, max: number): string {
  if (value.length <= max) return value
  return `${value.slice(0, max).trimEnd()}…`
}

export function srtTimestamp(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds))
  const hours = String(Math.floor(total / 3600)).padStart(2, '0')
  const minutes = String(Math.floor((total % 3600) / 60)).padStart(2, '0')
  const secs = String(total % 60).padStart(2, '0')
  return `${hours}:${minutes}:${secs},000`
}

export function download(filename: string, content: string, type = 'text/plain;charset=utf-8'): void {
  const url = URL.createObjectURL(new Blob([content], { type }))
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.append(link)
  link.click()
  link.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}

export function safeFilename(value: string): string {
  return value.replace(/[\\/:*?"<>|\n\r]+/g, ' ').trim().slice(0, 120) || 'episode'
}

export function isSecureImage(url: string): string {
  if (!url) return ''
  if (typeof window !== 'undefined' && window.location.protocol === 'https:' && url.startsWith('http:')) {
    return ''
  }
  return url
}

const palette = ['#31594b', '#524d70', '#987754', '#9b5545', '#577783', '#4d6a5e', '#6b5a7a', '#6c6250']

export function fallbackColor(seed: string): string {
  let hash = 0
  for (let index = 0; index < seed.length; index += 1) {
    hash = (hash * 31 + seed.charCodeAt(index)) | 0
  }
  return palette[Math.abs(hash) % palette.length]
}
