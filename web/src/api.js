import { ref } from 'vue'

export const csrfToken = ref('')

export async function api(path, options = {}) {
  const headers = new Headers(options.headers || {})
  if (options.body && !(options.body instanceof Blob) && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  if (!['GET', 'HEAD'].includes((options.method || 'GET').toUpperCase()) && csrfToken.value) {
    headers.set('X-CSRF-Token', csrfToken.value)
  }
  const response = await fetch(`/api/v1${path}`, { credentials: 'same-origin', ...options, headers })
  if (response.status === 204) return null
  const data = await response.json().catch(() => ({}))
  if (!response.ok) {
    const error = new Error(data.error || `请求失败 (${response.status})`)
    error.status = response.status
    throw error
  }
  return data
}

export function formatBytes(value) {
  if (!value) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB']
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
  return `${(value / 1024 ** index).toFixed(index ? 1 : 0)} ${units[index]}`
}

export function formatTime(value) {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' }).format(new Date(value))
}
