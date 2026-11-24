// Клиент внутреннего API BFF. CSRF-токен приходит при входе и в /auth/me.

export interface Address { name?: string; email: string }
export interface Brand { name: string; logo_url?: string; css_vars?: Record<string, string> }
export interface Me {
  mailbox: string
  csrf: string
  brand: Brand
  features: { password_change: boolean; sieve: boolean }
  limits: { max_upload: number; max_recipients: number }
  remote_images: 'ask' | 'always' | 'never'
}
export interface Folder {
  name: string; display: string; delimiter: string; depth: number
  role?: string; selectable: boolean; total: number; unread: number
}
export interface Summary {
  uid: number; from: Address[]; to: Address[]; subject: string; date: string; size: number
  seen: boolean; flagged: boolean; answered: boolean; forwarded: boolean; draft: boolean
  has_attachments: boolean
}
export interface Page { total: number; offset: number; messages: Summary[] }
export interface Attachment {
  index: number; filename: string; content_type: string; size: number; inline: boolean; content_id?: string
}
export interface Message {
  uid: number; folder: string; message_id?: string; references?: string[]
  from: Address[]; to: Address[]; cc?: Address[]; reply_to?: Address[]
  subject: string; date: string; attachments: Attachment[]; flags: string[]; list_unsubscribe?: string
}
export interface MessageView { message: Message; body_html: string; body_text: string; blocked_images: number }

export class ApiError extends Error {
  constructor(public status: number, public code: string, public detail?: string) {
    super(detail || code)
  }
}

let csrf = ''
let onUnauthorized: () => void = () => {}

export function setCsrf(token: string) { csrf = token }
export function setUnauthorizedHandler(fn: () => void) { onUnauthorized = fn }

async function request<T>(method: string, url: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  let payload: BodyInit | undefined
  if (body instanceof FormData) {
    payload = body
  } else if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }
  if (method !== 'GET' && csrf) headers['X-CSRF-Token'] = csrf
  const res = await fetch(url, { method, headers, body: payload, credentials: 'same-origin' })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    const err = new ApiError(res.status, data.error || 'error', data.detail)
    if (res.status === 401 && !url.endsWith('/auth/login')) onUnauthorized()
    throw err
  }
  return data as T
}

const q = (params: Record<string, string | number | undefined>) =>
  new URLSearchParams(
    Object.entries(params).filter(([, v]) => v !== undefined && v !== '').map(([k, v]) => [k, String(v)]),
  ).toString()

export const api = {
  config: () => request<{ brand: Brand; theme: string; default_domain: string }>('GET', '/api/v1/config'),
  me: () => request<Me>('GET', '/api/v1/auth/me'),
  login: (username: string, password: string) => request<Me>('POST', '/api/v1/auth/login', { username, password }),
  logout: () => request('POST', '/api/v1/auth/logout'),
  folders: () => request<{ folders: Folder[] }>('GET', '/api/v1/folders'),
  messages: (folder: string, offset = 0, limit = 50, text = '', filter = '') =>
    request<Page>('GET', `/api/v1/messages?${q({ folder, offset, limit, q: text, filter })}`),
  message: (folder: string, uid: number, images = false) =>
    request<MessageView>('GET', `/api/v1/message?${q({ folder, uid, images: images ? 1 : undefined })}`),
  partURL: (folder: string, uid: number, index: number, download = false) =>
    `/api/v1/part?${q({ folder, uid, index, download: download ? 1 : undefined })}`,
  rawURL: (folder: string, uid: number) => `/api/v1/raw?${q({ folder, uid })}`,
  flags: (folder: string, uids: number[], flags: string[], add: boolean) =>
    request('POST', '/api/v1/messages/flags', { folder, uids, flags, add }),
  move: (folder: string, uids: number[], dest: string) =>
    request('POST', '/api/v1/messages/move', { folder, uids, dest }),
  remove: (folder: string, uids: number[]) =>
    request<{ ok: boolean; permanent: boolean }>('POST', '/api/v1/messages/delete', { folder, uids }),
  send: (form: FormData) => request<{ ok: boolean; warning?: string }>('POST', '/api/v1/send', form),
}
