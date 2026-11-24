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
  role?: string; selectable: boolean; total: number; unread: number; subscribed: boolean
}
export interface Summary {
  uid: number; from: Address[]; to: Address[]; subject: string; date: string; size: number
  seen: boolean; flagged: boolean; answered: boolean; forwarded: boolean; draft: boolean
  has_attachments: boolean; priority?: 1 | 5
}
export interface Page { total: number; offset: number; messages: Summary[] }
export interface Attachment {
  index: number; filename: string; content_type: string; size: number; inline: boolean; content_id?: string
}
export interface MsgRef { folder: string; uid: number }
export interface Message {
  uid: number; folder: string; message_id?: string; in_reply_to?: string; references?: string[]
  from: Address[]; to: Address[]; cc?: Address[]; bcc?: Address[]; reply_to?: Address[]
  subject: string; date: string; attachments: Attachment[]; flags: string[]; list_unsubscribe?: string
  priority?: 1 | 5; mdn_to?: string; dsn?: boolean
}
export interface MessageView { message: Message; body_html: string; body_text: string; blocked_images: number }

export interface Identity {
  id: number; name: string; email: string; reply_to: string; bcc: string; signature: string; is_default: boolean
}
export interface Condition { field: string; header?: string; op: string; value?: string }
export interface Action { type: string; value?: string }
export interface Rule { name: string; enabled: boolean; match: 'all' | 'any'; conditions: Condition[]; actions: Action[]; stop: boolean }
export interface Vacation { enabled: boolean; subject: string; body: string; days: number; addresses?: string[]; start?: string; end?: string }
export interface Forward { enabled: boolean; to: string; keep_copy: boolean }
export interface Ruleset { rules: Rule[]; vacation: Vacation; forward: Forward }
export interface SieveState {
  mode: 'rules' | 'raw'; script_name: string; tag: string; ruleset?: Ruleset; raw: string; reason?: string
  extensions: Record<string, boolean>
}

export interface Labeled { value: string; label?: string }
export interface Contact {
  id: number; name: string; first_name: string; last_name: string; org: string; title: string; notes: string
  emails: Labeled[]; phones: Labeled[]; groups: number[]; updated_at?: string
}
export interface Group { id: number; name: string; count: number }
export interface Collected { email: string; name: string; uses: number; last_used: string }
export interface Suggestion { kind: 'contact' | 'collected' | 'group'; name: string; email?: string; group_id?: number; members?: Collected[] }

export interface Template { id: number; name: string; html: string }
export interface Prefs { archive_structure: '' | 'year' | 'month'; mdn_mode: '' | 'always' | 'never' }

export interface PasswordPolicy {
  min_length: number; max_length: number; require_classes: number
  forbid_chars?: string; forbid_mailbox: boolean
}

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

const q_ = (params: Record<string, string | number | undefined>) =>
  new URLSearchParams(
    Object.entries(params).filter(([, v]) => v !== undefined && v !== '').map(([k, v]) => [k, String(v)]),
  ).toString()

export const api = {
  config: () => request<{ brand: Brand; theme: string; default_domain: string }>('GET', '/api/v1/config'),
  me: () => request<Me>('GET', '/api/v1/auth/me'),
  login: (username: string, password: string) => request<Me>('POST', '/api/v1/auth/login', { username, password }),
  logout: () => request('POST', '/api/v1/auth/logout'),
  folders: () => request<{ folders: Folder[] }>('GET', '/api/v1/folders'),
  createFolder: (parent: string, name: string) =>
    request<{ folders: Folder[]; name: string }>('POST', '/api/v1/folders', { parent, name }),
  renameFolder: (name: string, parent: string, new_name: string) =>
    request<{ folders: Folder[]; name: string }>('POST', '/api/v1/folders/rename', { name, parent, new_name }),
  deleteFolder: (name: string) => request<{ folders: Folder[] }>('POST', '/api/v1/folders/delete', { name }),
  subscribeFolder: (name: string, subscribed: boolean) =>
    request<{ folders: Folder[] }>('POST', '/api/v1/folders/subscribe', { name, subscribed }),
  emptyFolder: (name: string) => request<{ folders: Folder[] }>('POST', '/api/v1/folders/empty', { name }),
  readFolder: (name: string) => request<{ folders: Folder[] }>('POST', '/api/v1/folders/read', { name }),
  messages: (folder: string, offset = 0, limit = 50, text = '', filter = '') =>
    request<Page>('GET', `/api/v1/messages?${q_({ folder, offset, limit, q: text, filter })}`),
  message: (folder: string, uid: number, images = false, peek = false) =>
    request<MessageView>('GET', `/api/v1/message?${q_({ folder, uid, images: images ? 1 : undefined, peek: peek ? 1 : undefined })}`),
  partURL: (folder: string, uid: number, index: number, download = false) =>
    `/api/v1/part?${q_({ folder, uid, index, download: download ? 1 : undefined })}`,
  rawURL: (folder: string, uid: number) => `/api/v1/raw?${q_({ folder, uid })}`,
  flags: (folder: string, uids: number[], flags: string[], add: boolean) =>
    request('POST', '/api/v1/messages/flags', { folder, uids, flags, add }),
  archive: (folder: string, uids: number[]) =>
    request<{ ok: boolean; moved: number }>('POST', '/api/v1/messages/archive', { folder, uids }),
  prefs: () => request<{ prefs: Prefs }>('GET', '/api/v1/prefs'),
  setPrefs: (p: Partial<Prefs>) => request<{ prefs: Prefs }>('PUT', '/api/v1/prefs', p),
  mdn: (folder: string, uid: number, send: boolean, auto = false) =>
    request<{ ok: boolean; sent: boolean }>('POST', '/api/v1/messages/mdn', { folder, uid, send, auto }),
  zipURL: (folder: string, uid: number, name: string) => `/api/v1/attachments.zip?${q_({ folder, uid, name })}`,
  move: (folder: string, uids: number[], dest: string) =>
    request('POST', '/api/v1/messages/move', { folder, uids, dest }),
  remove: (folder: string, uids: number[]) =>
    request<{ ok: boolean; permanent: boolean }>('POST', '/api/v1/messages/delete', { folder, uids }),
  send: (form: FormData) => request<{ ok: boolean; warning?: string }>('POST', '/api/v1/send', form),
  saveDraft: (form: FormData) => request<{ ok: boolean; draft_ref: MsgRef }>('POST', '/api/v1/drafts', form),
  discardDraft: (ref: MsgRef) => request('POST', '/api/v1/drafts/discard', ref),
  identities: () => request<{ identities: Identity[]; level: 'single' | 'domain' | 'any' }>('GET', '/api/v1/identities'),
  saveIdentity: (i: Partial<Identity>) => request<Identity>('POST', '/api/v1/identities', i),
  deleteIdentity: (id: number) => request('POST', '/api/v1/identities/delete', { id }),
  contacts: (q = '', group = 0, offset = 0, limit = 200) =>
    request<{ contacts: Contact[]; total: number }>('GET', `/api/v1/contacts?${q_({ q, group: group || undefined, offset, limit })}`),
  saveContact: (c: Partial<Contact>) => request<Contact>('POST', '/api/v1/contacts', c),
  deleteContacts: (ids: number[]) => request<{ deleted: number }>('POST', '/api/v1/contacts/delete', { ids }),
  suggest: (q: string) => request<{ suggestions: Suggestion[] }>('GET', `/api/v1/contacts/suggest?${q_({ q })}`),
  collected: (q = '') => request<{ collected: Collected[] }>('GET', `/api/v1/contacts/collected?${q_({ q })}`),
  deleteCollected: (emails: string[]) => request('POST', '/api/v1/contacts/collected/delete', { emails }),
  importContacts: (file: File) => {
    const f = new FormData()
    f.append('file', file)
    return request<{ added: number; skipped: number; limit_reached: boolean }>('POST', '/api/v1/contacts/import', f)
  },
  exportURL: (format: 'vcf' | 'csv', group = 0) => `/api/v1/contacts/export?${q_({ format, group: group || undefined })}`,
  groups: () => request<{ groups: Group[] }>('GET', '/api/v1/groups'),
  saveGroup: (g: Partial<Group>) => request<Group>('POST', '/api/v1/groups', g),
  deleteGroup: (id: number) => request('POST', '/api/v1/groups/delete', { id }),
  passwordPolicy: () => request<{ policy: PasswordPolicy }>('GET', '/api/v1/account/password'),
  changePassword: (current: string, next: string) => request('POST', '/api/v1/account/password', { current, new: next }),
  templates: () => request<{ templates: Template[] }>('GET', '/api/v1/templates'),
  saveTemplate: (tpl: Partial<Template>) => request<Template>('POST', '/api/v1/templates', tpl),
  deleteTemplate: (id: number) => request('POST', '/api/v1/templates/delete', { id }),
  sieve: () => request<SieveState>('GET', '/api/v1/sieve'),
  putSieve: (body: { mode: 'rules' | 'raw'; tag: string; ruleset?: Ruleset; raw?: string }) =>
    request<SieveState>('PUT', '/api/v1/sieve', body),
}
