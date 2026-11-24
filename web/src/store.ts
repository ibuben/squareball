import { reactive } from 'vue'
import {
  api, ApiError, setCsrf, type Address, type Attachment, type Folder, type Identity, type Me, type MessageView, type MsgRef,
  type Prefs, type Summary, type Template,
} from './api'
import { t } from './i18n'

export interface ComposeInit {
  to?: Address[]; cc?: Address[]; bcc?: Address[]; subject?: string
  html?: string   // текст пользователя — в редактор
  quote?: string  // цитата исходного письма — отдельно, без изменений
  inReplyTo?: string; references?: string[]
  source?: { folder: string; uid: number; mode: 'reply' | 'forward' }
  // Продолжение черновика: ссылка на него и его вложения.
  draftRef?: MsgRef
  existing?: Attachment[]
  identityId?: number
  priority?: 0 | 1 | 5; readReceipt?: boolean; dsn?: boolean
}

interface Toast { id: number; text: string; action?: { label: string; run: () => void } }

export const state = reactive({
  me: null as Me | null,
  booting: true,
  folders: [] as Folder[],
  folder: 'INBOX',
  messages: [] as Summary[],
  total: 0,
  loadingList: false,
  filter: '' as '' | 'unread' | 'flagged',
  search: '',
  selected: new Set<number>(),
  current: null as MessageView | null,
  currentUID: 0,
  loadingMessage: false,
  composes: [] as { id: number; init: ComposeInit }[],
  toasts: [] as Toast[],
  theme: (localStorage.getItem('wm.theme') as 'light' | 'dark' | 'system') || 'system',
  showContact: localStorage.getItem('wm.contact') !== '0',
  view: 'mail' as 'mail' | 'settings' | 'contacts',
  identities: [] as Identity[],
  identityLevel: 'single' as 'single' | 'domain' | 'any',
  prefs: { archive_structure: '', mdn_mode: '' } as Prefs,
  templates: [] as Template[],
})

export async function loadIdentities() {
  try {
    const r = await api.identities()
    state.identities = r.identities
    state.identityLevel = r.level
  } catch (e) { reportError(e) }
}

export async function loadTemplates() {
  try { state.templates = (await api.templates()).templates } catch (e) { reportError(e) }
}

export async function loadPrefs() {
  try { state.prefs = (await api.prefs()).prefs } catch (e) { reportError(e) }
}

export async function savePrefs(p: Partial<Prefs>) {
  try {
    state.prefs = (await api.setPrefs(p)).prefs
    toast('Сохранено')
  } catch (e) { reportError(e) }
}

let toastSeq = 0
// На экране — не больше трёх последних уведомлений.
export function toast(text: string, action?: Toast['action']) {
  const id = ++toastSeq
  state.toasts = [...state.toasts, { id, text, action }].slice(-3)
  setTimeout(() => { state.toasts = state.toasts.filter((x) => x.id !== id) }, action ? 6000 : 4000)
}

export function applyTheme() {
  const dark = state.theme === 'dark' ||
    (state.theme === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  try { localStorage.setItem('wm.theme', state.theme) } catch { /* приватный режим */ }
}

export function applyBrand(vars?: Record<string, string>) {
  for (const [k, v] of Object.entries(vars ?? {})) {
    if (/^--[a-z0-9-]+$/.test(k)) document.documentElement.style.setProperty(k, v)
  }
}

export function setMe(me: Me | null) {
  state.me = me
  setCsrf(me?.csrf ?? '')
  if (me) {
    applyBrand(me.brand.css_vars)
    document.title = `${me.mailbox} — ${me.brand.name}`
  }
}

export function folderTitle(f: Folder | undefined): string {
  if (!f) return ''
  const key = f.role ? (`folder.${f.role}` as const) : null
  return key ? t(key as Parameters<typeof t>[0]) : f.display
}

export const currentFolder = () => state.folders.find((f) => f.name === state.folder)
export const roleFolder = (role: string) => state.folders.find((f) => f.role === role)

export function reportError(e: unknown) {
  if (e instanceof ApiError && e.status === 401) return
  console.error(e)
  toast(e instanceof ApiError && e.detail ? e.detail : t('toast.error'))
}

export async function loadFolders() {
  try {
    const { folders } = await api.folders()
    state.folders = folders
  } catch (e) { reportError(e) }
}

// --- управление папками -------------------------------------------------------

const folderErrors: Record<string, string> = {
  'folder.name': 'Недопустимое имя папки: без «/», «*», «%» и пробелов по краям',
  'folder.exists': 'Папка с таким именем уже есть',
  'folder.protected': 'Системные папки нельзя переименовать или удалить',
  'mail.not_found': 'Папка не найдена — список обновлён',
}

function folderError(e: unknown) {
  if (e instanceof ApiError && folderErrors[e.code]) {
    toast(folderErrors[e.code])
    if (e.code === 'mail.not_found') void loadFolders()
  } else reportError(e)
}

// Родитель папки по имени ('' — верхний уровень).
export function folderParent(f: Folder): string {
  if (!f.delimiter || f.depth === 0) return ''
  return f.name.slice(0, f.name.length - f.display.length - f.delimiter.length)
}

// Папки, видимые в панели: подписанные, спецпапки и родители подписанных.
export function visibleFolders(): Folder[] {
  const shown = new Set<string>()
  for (const f of state.folders) {
    if (!f.subscribed && !f.role) continue
    shown.add(f.name)
    let p = folderParent(f)
    while (p) {
      shown.add(p)
      const pf = state.folders.find((x) => x.name === p)
      p = pf ? folderParent(pf) : ''
    }
  }
  return state.folders.filter((f) => shown.has(f.name))
}

// Если открытая папка исчезла (переименована, удалена) — переходим в new или во Входящие.
function afterFolders(folders: Folder[], renamedFrom = '', renamedTo = '') {
  state.folders = folders
  const cur = state.folder
  if (renamedFrom && (cur === renamedFrom || cur.startsWith(renamedFrom + (folders[0]?.delimiter || '/')))) {
    state.folder = renamedTo + cur.slice(renamedFrom.length)
  } else if (!folders.some((f) => f.name === cur)) {
    void openFolder('INBOX')
  }
}

export async function createFolder(parent: string, name: string): Promise<boolean> {
  try {
    const r = await api.createFolder(parent, name.trim())
    afterFolders(r.folders)
    toast(`Папка «${name.trim()}» создана`)
    return true
  } catch (e) { folderError(e); return false }
}

export async function renameFolder(f: Folder, parent: string, name: string): Promise<boolean> {
  try {
    const r = await api.renameFolder(f.name, parent, name.trim())
    afterFolders(r.folders, f.name, r.name)
    return true
  } catch (e) { folderError(e); return false }
}

export async function deleteFolder(f: Folder): Promise<boolean> {
  try {
    afterFolders((await api.deleteFolder(f.name)).folders)
    toast(`Папка «${f.display}» удалена`)
    return true
  } catch (e) { folderError(e); return false }
}

export async function subscribeFolder(f: Folder, on: boolean) {
  try { afterFolders((await api.subscribeFolder(f.name, on)).folders) } catch (e) { folderError(e) }
}

export async function emptyFolder(f: Folder) {
  try {
    afterFolders((await api.emptyFolder(f.name)).folders)
    if (state.folder === f.name) void openFolder(f.name)
    toast(`Папка «${folderTitle(f)}» очищена`)
  } catch (e) { folderError(e) }
}

export async function readFolder(f: Folder) {
  try {
    afterFolders((await api.readFolder(f.name)).folders)
    if (state.folder === f.name) for (const m of state.messages) m.seen = true
  } catch (e) { folderError(e) }
}

export async function loadList(append = false) {
  state.loadingList = true
  try {
    const offset = append ? state.messages.length : 0
    const page = await api.messages(state.folder, offset, 50, state.search, state.filter)
    state.messages = append ? [...state.messages, ...page.messages] : page.messages
    state.total = page.total
  } catch (e) { reportError(e) } finally { state.loadingList = false }
}

export async function openFolder(name: string) {
  state.folder = name
  state.messages = [] // не показывать список прошлой папки, пока грузится новая
  state.total = 0
  state.selected = new Set()
  state.current = null
  state.currentUID = 0
  state.search = ''
  await loadList()
}

export async function openMessage(uid: number, images = false) {
  state.currentUID = uid
  state.selected = new Set([uid])
  state.loadingMessage = true
  try {
    const view = await api.message(state.folder, uid, images)
    if (state.currentUID !== uid) return
    state.current = view
    const s = state.messages.find((m) => m.uid === uid)
    if (s && !s.seen) {
      s.seen = true
      const f = currentFolder()
      if (f && f.unread > 0) f.unread--
    }
  } catch (e) { reportError(e) } finally { state.loadingMessage = false }
}

function selectedUIDs(): number[] {
  return state.selected.size ? [...state.selected] : state.currentUID ? [state.currentUID] : []
}

function dropFromList(uids: number[]) {
  const idx = state.messages.findIndex((m) => uids.includes(m.uid))
  state.messages = state.messages.filter((m) => !uids.includes(m.uid))
  state.total -= uids.length
  state.selected = new Set()
  if (uids.includes(state.currentUID)) {
    const next = state.messages[Math.min(idx, state.messages.length - 1)]
    state.current = null
    state.currentUID = 0
    if (next) void openMessage(next.uid)
  }
}

export async function deleteSelected() {
  const uids = selectedUIDs()
  if (!uids.length) return
  const folder = state.folder
  const snapshot = [...state.messages]
  dropFromList(uids)
  try {
    const r = await api.remove(folder, uids)
    const trash = roleFolder('trash')
    if (r.permanent || !trash) toast(t('toast.deletedForever'))
    else toast(t('toast.deleted'))
    void loadFolders()
  } catch (e) { state.messages = snapshot; reportError(e) }
}

export async function moveSelected(dest: string) {
  const uids = selectedUIDs()
  if (!uids.length || dest === state.folder) return
  const folder = state.folder
  dropFromList(uids)
  try {
    await api.move(folder, uids, dest)
    toast(t('toast.moved'))
    void loadFolders()
  } catch (e) { reportError(e); void loadList() }
}

// archiveSelected переносит письма в «Архив»; структуру (по годам, месяцам) выбирает сервер.
export async function archiveSelected() {
  const uids = selectedUIDs()
  if (!uids.length || !roleFolder('archive')) return
  const folder = state.folder
  dropFromList(uids)
  try {
    await api.archive(folder, uids)
    toast('Перемещено в архив')
    void loadFolders()
  } catch (e) { reportError(e); void loadList() }
}

export async function setFlag(uids: number[], flag: '\\Seen' | '\\Flagged', add: boolean) {
  for (const m of state.messages) {
    if (!uids.includes(m.uid)) continue
    if (flag === '\\Seen') m.seen = add
    else m.flagged = add
  }
  try {
    await api.flags(state.folder, uids, [flag], add)
    if (flag === '\\Seen') void loadFolders()
  } catch (e) { reportError(e); void loadList() }
}

let composeSeq = 0
export function openCompose(init: ComposeInit = {}) {
  // Один черновик — одно окно.
  if (init.draftRef && state.composes.some((c) => c.init.draftRef?.uid === init.draftRef!.uid)) return
  state.composes.push({ id: ++composeSeq, init })
}

// Открывает черновик из папки «Черновики» в окне редактирования.
export async function openDraft(uid: number) {
  try {
    const v = await api.message(state.folder, uid, true, true)
    const m = v.message
    const { text, quote } = splitQuote(v.body_html)
    const from = m.from[0]
    const ident = state.identities.find((i) => i.email.toLowerCase() === from?.email.toLowerCase() && i.name === (from?.name ?? ''))
      ?? state.identities.find((i) => i.email.toLowerCase() === from?.email.toLowerCase())
    openCompose({
      identityId: ident?.id,
      to: m.to, cc: m.cc ?? [], bcc: m.bcc ?? [], subject: m.subject, html: text, quote,
      inReplyTo: m.in_reply_to, references: m.references,
      draftRef: { folder: m.folder, uid: m.uid },
      existing: m.attachments.filter((a) => !a.inline || !a.content_id),
      priority: m.priority ?? 0, readReceipt: !!m.mdn_to, dsn: !!m.dsn,
    })
  } catch (e) { reportError(e) }
}

// Делит тело черновика на текст пользователя и цитату (<div data-wm-quote>).
// Подпись (<div data-wm-signature>) отбрасывается: её заново подставляет отправитель.
export function splitQuote(html: string): { text: string; quote: string } {
  const doc = new DOMParser().parseFromString(`<body>${html}</body>`, 'text/html')
  doc.body.querySelector('div[data-wm-signature]')?.remove()
  const q = doc.body.querySelector('div[data-wm-quote]')
  let quote = ''
  if (q) {
    quote = q.innerHTML
    q.remove()
  }
  return { text: doc.body.innerHTML, quote }
}

// Компоненты окон сообщают, есть ли у них несохранённые изменения.
export const unsaved = new Set<number>()
export function closeCompose(id: number) {
  state.composes = state.composes.filter((c) => c.id !== id)
}

export async function boot() {
  try {
    setMe(await api.me())
  } catch (e) {
    if (!(e instanceof ApiError && e.status === 401)) console.error(e)
  } finally {
    state.booting = false
  }
}
