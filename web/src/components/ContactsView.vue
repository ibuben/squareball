<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  AlertTriangle, BookUser, Download, History, Library, Lock, Mail, Pencil, Phone, Plus, RefreshCw, Search, Server, Trash2, Upload,
  UserPlus, Users, X,
} from 'lucide-vue-next'
import { api, ApiError, type Book, type BookError, type Books, type Collected, type Contact, type Group, type Labeled } from '../api'
import Avatar from './Avatar.vue'
import { openCompose, state, toast } from '../store'
import { t, type Key } from '../i18n'

type Source = { kind: 'all' } | { kind: 'group'; id: number } | { kind: 'collected' } | { kind: 'book'; id: number }

const source = ref<Source>({ kind: 'all' })
const groups = ref<Group[]>([])
const contacts = ref<Contact[]>([])
const total = ref(0)
const collected = ref<Collected[]>([])
const search = ref('')
const selected = ref<Set<number>>(new Set())
const current = ref<Contact | null>(null)
const editing = ref<Contact | null>(null)
const error = ref('')
const fileInput = ref<HTMLInputElement>()

const groupId = computed(() => (source.value.kind === 'group' ? source.value.id : 0))
const bookId = computed(() => (source.value.kind === 'book' ? source.value.id : undefined))

// --- внешние адресные книги (CardDAV) ----------------------------------------------
const books = ref<Books | null>(null)
const syncing = ref(false)
const hasBooks = computed(() => !!books.value?.enabled && (books.value.books.length > 0 || books.value.accounts.length > 0))
const bookOf = (c: Contact) => books.value?.books.find((b) => b.id === c.book)
const readonly = (c: Contact) => !!bookOf(c)?.readonly
const writableBooks = computed(() => books.value?.books.filter((b) => !b.readonly) ?? [])
const errText = (e?: BookError) => (e ? t(`books.e.${e}` as Key) : '')
const accountName = (id: number) => books.value?.accounts.find((a) => a.id === id)?.name ?? ''

async function loadBooks() {
  if (!state.me?.features.contact_books) return
  try {
    books.value = await api.books()
    if (books.value.stale) await syncBooks(false)
  } catch (e) { report(e) }
}
// Счётчики книг после правок (без похода на серверы).
async function refreshBooks() {
  if (books.value?.enabled) try { books.value = await api.books() } catch { /* не критично */ }
}
async function syncBooks(force = true) {
  if (syncing.value) return
  syncing.value = true
  try {
    books.value = await api.syncBooks(force)
    await load()
  } catch (e) { report(e) } finally { syncing.value = false }
}

const accountForm = ref<{ name: string; url: string; username: string; password: string } | null>(null)
const accountError = ref('')
const accountBusy = ref(false)
const ACCOUNT_ERRORS = ['books.auth', 'books.forbidden', 'books.not_found', 'books.unavailable', 'books.url', 'books.limit']
async function addAccount() {
  const f = accountForm.value
  if (!f) return
  accountBusy.value = true
  accountError.value = ''
  try {
    books.value = await api.addBookAccount(f)
    accountForm.value = null
    toast(t('books.connected'))
    await load()
  } catch (e) {
    accountError.value = e instanceof ApiError ? (ACCOUNT_ERRORS.includes(e.code) ? t(e.code.replace('books.', 'books.err.') as Key) : t('books.connectFailed')) : String(e)
  } finally { accountBusy.value = false }
}
async function removeAccount(id: number) {
  if (!confirm(t('books.confirmRemove', { name: accountName(id) }))) return
  try {
    books.value = await api.deleteBookAccount(id)
    if (source.value.kind === 'book' && !books.value.books.some((b) => b.id === (source.value as { id: number }).id)) choose({ kind: 'all' })
    else await load()
  } catch (e) { report(e) }
}
const labels = computed(() => Object.fromEntries(['work', 'home', 'mobile', 'other'].map((k) => [k, t(`ct.l.${k}` as Key)])) as Record<string, string>)

async function loadGroups() {
  try { groups.value = (await api.groups()).groups } catch (e) { report(e) }
}
async function load() {
  try {
    if (source.value.kind === 'collected') {
      collected.value = (await api.collected(search.value)).collected
    } else {
      const r = await api.contacts(search.value, groupId.value, 0, 200, bookId.value)
      contacts.value = r.contacts
      total.value = r.total
      if (current.value) current.value = r.contacts.find((c) => c.id === current.value!.id) ?? null
    }
  } catch (e) { report(e) }
}
const REMOTE_ERRORS = ['contacts.conflict', 'contacts.readonly', 'contacts.remote_auth', 'contacts.remote_unavailable', 'contacts.remote_not_found']
function errorText(e: unknown): string {
  if (e instanceof ApiError && REMOTE_ERRORS.includes(e.code)) return t(e.code.replace('contacts.', 'ct.err.') as Key)
  if (e instanceof ApiError && e.detail) return e.detail
  return t('toast.error')
}
function report(e: unknown) {
  console.error(e)
  toast(errorText(e))
  if (e instanceof ApiError && e.code === 'contacts.conflict') void syncBooks()
}

onMounted(async () => { await Promise.all([loadGroups(), load(), loadBooks()]) })

function choose(s: Source) {
  source.value = s
  selected.value = new Set()
  current.value = null
  editing.value = null
  void load()
}
let searchTimer = 0
function onSearch() { clearTimeout(searchTimer); searchTimer = window.setTimeout(load, 250) }

function display(c: Contact) {
  return c.name || `${c.first_name} ${c.last_name}`.trim() || c.emails[0]?.value || '—'
}

function blank(): Contact {
  return { id: 0, name: '', first_name: '', last_name: '', org: '', title: '', notes: '',
    emails: [{ value: '', label: 'work' }], phones: [], groups: groupId.value ? [groupId.value] : [],
    book: bookId.value && writableBooks.value.some((b) => b.id === bookId.value) ? bookId.value : 0 }
}
function startEdit(c?: Contact) {
  error.value = ''
  editing.value = JSON.parse(JSON.stringify(c ?? blank()))
}
function addRow(list: Labeled[], label: string) { list.push({ value: '', label }) }

async function save() {
  const c = editing.value!
  c.emails = c.emails.filter((e) => e.value.trim())
  c.phones = c.phones.filter((p) => p.value.trim())
  try {
    const saved = await api.saveContact(c)
    editing.value = null
    current.value = saved
    toast(t('ct.saved'))
    await Promise.all([load(), loadGroups(), refreshBooks()])
  } catch (e) {
    error.value = errorText(e)
    if (e instanceof ApiError && e.code === 'contacts.conflict') void syncBooks()
  }
}

async function removeSelected(ids: number[]) {
  if (!ids.length || !confirm(ids.length === 1 ? t('ct.confirmDeleteOne') : t('ct.confirmDeleteMany', { n: ids.length }))) return
  try {
    const r = await api.deleteContacts(ids)
    if (r.failed) toast(t('ct.deleteFailed', { n: r.failed }))
    selected.value = new Set()
    if (current.value && ids.includes(current.value.id)) current.value = null
    await Promise.all([load(), loadGroups(), refreshBooks()])
  } catch (e) { report(e) }
}

function toggle(id: number) {
  const s = new Set(selected.value)
  s.has(id) ? s.delete(id) : s.add(id)
  selected.value = s
}

function write(list: Contact[]) {
  const to = list.flatMap((c) => (c.emails[0] ? [{ name: display(c), email: c.emails[0].value }] : []))
  if (to.length) openCompose({ to })
}

async function newGroup() {
  const name = prompt(t('ct.groupName'))
  if (!name?.trim()) return
  try {
    const g = await api.saveGroup({ name: name.trim() })
    await loadGroups()
    choose({ kind: 'group', id: g.id })
  } catch (e) { report(e) }
}
async function renameGroup(g: Group) {
  const name = prompt(t('ct.newName'), g.name)
  if (!name?.trim() || name === g.name) return
  try { await api.saveGroup({ id: g.id, name: name.trim() }); await loadGroups() } catch (e) { report(e) }
}
async function deleteGroup(g: Group) {
  if (!confirm(t('ct.confirmDeleteGroup', { name: g.name }))) return
  try { await api.deleteGroup(g.id); await loadGroups(); choose({ kind: 'all' }) } catch (e) { report(e) }
}
// Добавить выбранные контакты в группу.
async function addToGroup(gid: number) {
  const list = contacts.value.filter((c) => selected.value.has(c.id) && !c.groups.includes(gid))
  try {
    for (const c of list) await api.saveContact({ ...c, groups: [...c.groups, gid] })
    toast(t('ct.addedToGroup', { n: list.length }))
    await Promise.all([load(), loadGroups()])
  } catch (e) { report(e) }
}

async function importFile(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  ;(e.target as HTMLInputElement).value = ''
  if (!f) return
  try {
    const r = await api.importContacts(f)
    toast(t('ct.imported', { n: r.added }) + (r.skipped ? t('ct.skipped', { n: r.skipped }) : ''))
    await Promise.all([load(), loadGroups()])
  } catch (err) {
    toast(err instanceof ApiError && err.code === 'contacts.import_format'
      ? t('ct.importFormat')
      : t('ct.importFailed'))
  }
}

async function saveCollected(c: Collected) {
  startEdit({ ...blank(), name: c.name, emails: [{ value: c.email, label: 'work' }], groups: [] })
}
async function forgetCollected(c: Collected) {
  try { await api.deleteCollected([c.email]); await load() } catch (e) { report(e) }
}
</script>

<template>
  <div class="contacts">
    <nav class="sources">
      <h1>{{ t('nav.contacts') }}</h1>
      <button class="src" :class="{ on: source.kind === 'all' }" @click="choose({ kind: 'all' })">
        <BookUser :size="16" /> {{ t('ct.all') }}
      </button>
      <button v-if="hasBooks" class="src" :class="{ on: source.kind === 'book' && source.id === 0 }" data-book="0"
              @click="choose({ kind: 'book', id: 0 })">
        <BookUser :size="16" /> {{ t('ct.personal') }}
      </button>
      <button class="src" :class="{ on: source.kind === 'collected' }" @click="choose({ kind: 'collected' })">
        <History :size="16" /> {{ t('ct.collected') }}
      </button>
      <template v-if="books?.enabled">
        <div class="sep row-sep">
          <span>{{ t('ct.books') }}</span>
          <button class="icon-btn mini vis" :class="{ spin: syncing }" :title="t('ct.syncTitle')" :aria-label="t('ct.syncLabel')"
                  data-test="books-sync" :disabled="syncing" @click="syncBooks()"><RefreshCw :size="13" /></button>
        </div>
        <template v-for="a in books.accounts" :key="a.id">
          <div class="acct" :title="a.url">
            <Server :size="13" /> <span class="ellipsis">{{ a.name }}</span>
            <AlertTriangle v-if="a.error" :size="13" class="warn" :aria-label="errText(a.error)" />
            <button v-if="!a.preset" class="icon-btn mini" :aria-label="t('ct.disconnectName', { name: a.name })" :title="t('ct.disconnect')"
                    @click="removeAccount(a.id)"><X :size="13" /></button>
          </div>
          <p v-if="a.error" class="acct-err">{{ errText(a.error) }}</p>
          <button v-for="b in books.books.filter((b) => b.account === a.id)" :key="b.id" class="src"
                  :class="{ on: source.kind === 'book' && source.id === b.id }" :data-book="b.id"
                  :title="b.error ? errText(b.error) : b.readonly ? t('ct.readonlyTitle') : ''" @click="choose({ kind: 'book', id: b.id })">
            <Library :size="16" /> <span class="ellipsis">{{ b.name }}</span>
            <Lock v-if="b.readonly" :size="12" class="faint" :aria-label="t('ct.readonlyLower')" />
            <AlertTriangle v-if="b.error" :size="13" class="warn" />
            <span class="faint">{{ b.count }}</span>
          </button>
        </template>
        <button v-if="books.allow_user_accounts" class="src add" data-test="books-add"
                @click="accountForm = { name: '', url: '', username: state.me?.mailbox ?? '', password: '' }; accountError = ''">
          <Plus :size="16" /> {{ t('ct.connectBook') }}
        </button>
      </template>
      <div class="sep">{{ t('ct.groups') }}</div>
      <div v-for="g in groups" :key="g.id" class="src grp" :class="{ on: source.kind === 'group' && source.id === g.id }"
           @click="choose({ kind: 'group', id: g.id })">
        <Users :size="16" /> <span class="ellipsis">{{ g.name }}</span> <span class="faint">{{ g.count }}</span>
        <button class="icon-btn mini" :aria-label="t('ct.renameGroup')" @click.stop="renameGroup(g)"><Pencil :size="13" /></button>
        <button class="icon-btn mini" :aria-label="t('ct.deleteGroup')" @click.stop="deleteGroup(g)"><Trash2 :size="13" /></button>
      </div>
      <button class="src add" @click="newGroup"><Plus :size="16" /> {{ t('ct.newGroup') }}</button>
    </nav>

    <section class="list">
      <div class="toolbar">
        <label class="search"><Search :size="15" class="faint" />
          <input v-model="search" :placeholder="t('ct.searchPh')" @input="onSearch" />
        </label>
      </div>
      <div v-if="source.kind !== 'collected'" class="toolbar">
        <button class="btn primary" @click="startEdit()"><UserPlus :size="15" /> {{ t('ct.new') }}</button>
        <!-- Импорт идёт в личную книгу: во внешней книге кнопку не показываем. -->
        <button v-if="!(bookId && bookId > 0)" class="icon-btn" :title="t('ct.importTitle')" :aria-label="t('ct.import')"
                @click="fileInput?.click()"><Upload :size="17" /></button>
        <a class="icon-btn" :title="t('ct.exportTitle')" :aria-label="t('ct.exportLabel')" :href="api.exportURL('vcf', groupId)"><Download :size="17" /></a>
        <a class="link small" :href="api.exportURL('csv', groupId)">CSV</a>
        <input ref="fileInput" type="file" accept=".vcf,.vcard,.csv,text/vcard,text/csv" hidden @change="importFile" />
      </div>
      <div v-if="selected.size" class="bulk">
        {{ t('ct.selected', { n: selected.size }) }}
        <button class="btn" @click="write(contacts.filter((c) => selected.has(c.id)))"><Mail :size="14" /> {{ t('ct.write') }}</button>
        <select v-if="groups.length" class="input sel" @change="addToGroup(Number(($event.target as HTMLSelectElement).value)); ($event.target as HTMLSelectElement).value = ''">
          <option value="">{{ t('ct.toGroup') }}</option>
          <option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}</option>
        </select>
        <button class="icon-btn" :aria-label="t('ct.deleteSelected')" @click="removeSelected([...selected])"><Trash2 :size="16" /></button>
        <button class="icon-btn" :aria-label="t('ct.clearSelection')" @click="selected = new Set()"><X :size="16" /></button>
      </div>

      <div class="scroll items">
        <template v-if="source.kind === 'collected'">
          <p class="faint hint">{{ t('ct.collectedHint') }}</p>
          <div v-for="c in collected" :key="c.email" class="item">
            <Avatar :addr="{ name: c.name, email: c.email }" :size="32" />
            <div class="main"><strong class="ellipsis">{{ c.name || c.email }}</strong><span class="faint ellipsis">{{ c.email }}</span></div>
            <button class="icon-btn" :title="t('ct.saveToContacts')" :aria-label="t('ct.saveToContacts')" @click="saveCollected(c)"><UserPlus :size="15" /></button>
            <button class="icon-btn" :title="t('ct.forget')" :aria-label="t('ct.forget')" @click="forgetCollected(c)"><Trash2 :size="15" /></button>
          </div>
          <p v-if="!collected.length" class="faint hint">{{ t('ct.emptyYet') }}</p>
        </template>
        <template v-else>
          <div v-for="c in contacts" :key="c.id" class="item" :class="{ on: current?.id === c.id }"
               @click="current = c; editing = null">
            <input type="checkbox" :checked="selected.has(c.id)" :aria-label="t('ct.select', { name: display(c) })" @click.stop="toggle(c.id)" />
            <Avatar :addr="{ name: display(c), email: c.emails[0]?.value ?? display(c) }" :size="32" />
            <div class="main"><strong class="ellipsis">{{ display(c) }}</strong><span class="faint ellipsis">{{ c.emails[0]?.value ?? c.org }}</span></div>
          </div>
          <p v-if="!contacts.length" class="faint hint">{{ t(search ? 'ct.notFound' : 'ct.none') }}</p>
          <p v-else class="faint hint">{{ t('ct.total', { n: total }) }}</p>
        </template>
      </div>
    </section>

    <section class="detail scroll">
      <form v-if="editing" class="card" @submit.prevent="save">
        <h2>{{ t(editing.id ? 'ct.edit' : 'ct.new') }}</h2>
        <label v-if="!editing.id && writableBooks.length">{{ t('ct.book') }}
          <select v-model="editing.book" class="input" data-test="contact-book">
            <option :value="0">{{ t('ct.personal') }}</option>
            <option v-for="b in writableBooks" :key="b.id" :value="b.id">{{ b.name }} · {{ accountName(b.account) }}</option>
          </select>
        </label>
        <div class="two">
          <label>{{ t('ct.firstName') }} <input v-model="editing.first_name" class="input" /></label>
          <label>{{ t('ct.lastName') }} <input v-model="editing.last_name" class="input" /></label>
        </div>
        <label>{{ t('ct.displayName') }} <input v-model="editing.name" class="input" :placeholder="t('ct.displayNamePh')" /></label>
        <div class="two">
          <label>{{ t('ct.org') }} <input v-model="editing.org" class="input" /></label>
          <label>{{ t('ct.title') }} <input v-model="editing.title" class="input" /></label>
        </div>
        <div class="group-label">{{ t('ct.emails') }}</div>
        <div v-for="(e, i) in editing.emails" :key="'e' + i" class="row">
          <input v-model="e.value" class="input" type="email" placeholder="name@example.com" />
          <select v-model="e.label" class="input sel"><option v-for="(l, k) in labels" :key="k" :value="k">{{ l }}</option></select>
          <button type="button" class="icon-btn" :aria-label="t('ct.removeEmail')" @click="editing.emails.splice(i, 1)"><X :size="15" /></button>
        </div>
        <button type="button" class="link" @click="addRow(editing.emails, 'work')">{{ t('ct.addEmail') }}</button>
        <div class="group-label">{{ t('ct.phones') }}</div>
        <div v-for="(p, i) in editing.phones" :key="'p' + i" class="row">
          <input v-model="p.value" class="input" type="tel" placeholder="+7 900 000-00-00" />
          <select v-model="p.label" class="input sel"><option v-for="(l, k) in labels" :key="k" :value="k">{{ l }}</option></select>
          <button type="button" class="icon-btn" :aria-label="t('ct.removePhone')" @click="editing.phones.splice(i, 1)"><X :size="15" /></button>
        </div>
        <button type="button" class="link" @click="addRow(editing.phones, 'mobile')">{{ t('ct.addPhone') }}</button>
        <template v-if="groups.length">
          <div class="group-label">{{ t('ct.groups') }}</div>
          <div class="checks">
            <label v-for="g in groups" :key="g.id" class="check">
              <input v-model="editing.groups" type="checkbox" :value="g.id" /> {{ g.name }}
            </label>
          </div>
        </template>
        <label>{{ t('ct.notes') }} <textarea v-model="editing.notes" class="input area" rows="3" /></label>
        <p v-if="error" class="error" role="alert">{{ error }}</p>
        <div class="actions">
          <button class="btn primary">{{ t('common.save') }}</button>
          <button type="button" class="btn" @click="editing = null">{{ t('common.cancel') }}</button>
        </div>
      </form>

      <div v-else-if="current" class="card view">
        <div class="head">
          <Avatar :addr="{ name: display(current), email: current.emails[0]?.value ?? display(current) }" :size="64" />
          <div>
            <h2>{{ display(current) }}</h2>
            <div class="muted">{{ [current.title, current.org].filter(Boolean).join(', ') }}</div>
          </div>
        </div>
        <div class="actions">
          <button v-if="current.emails.length" class="btn primary" @click="write([current])"><Mail :size="15" /> {{ t('ct.write') }}</button>
          <template v-if="!readonly(current)">
            <button class="btn" @click="startEdit(current)"><Pencil :size="15" /> {{ t('common.edit') }}</button>
            <button class="btn" @click="removeSelected([current.id])"><Trash2 :size="15" /> {{ t('common.delete') }}</button>
          </template>
        </div>
        <div v-if="bookOf(current)" class="line faint small">
          <Library :size="15" /> {{ bookOf(current)!.name }} · {{ accountName(bookOf(current)!.account) }}
          <template v-if="readonly(current)"> · {{ t('ct.readonlyLower') }}</template>
        </div>
        <div v-for="e in current.emails" :key="e.value" class="line">
          <Mail :size="15" class="faint" />
          <a href="#" @click.prevent="openCompose({ to: [{ name: display(current), email: e.value }] })">{{ e.value }}</a>
          <span class="faint small">{{ labels[e.label ?? ''] ?? '' }}</span>
        </div>
        <div v-for="p in current.phones" :key="p.value" class="line">
          <Phone :size="15" class="faint" /> <a :href="`tel:${p.value}`">{{ p.value }}</a>
          <span class="faint small">{{ labels[p.label ?? ''] ?? '' }}</span>
        </div>
        <div v-if="current.groups.length" class="line">
          <Users :size="15" class="faint" /> {{ groups.filter((g) => current!.groups.includes(g.id)).map((g) => g.name).join(', ') }}
        </div>
        <p v-if="current.notes" class="notes">{{ current.notes }}</p>
      </div>
      <p v-else class="faint empty">{{ t('ct.choose') }}</p>
    </section>

    <div v-if="accountForm" class="overlay" @mousedown.self="accountForm = null">
      <form class="card dialog" role="dialog" :aria-label="t('ct.connectTitle')" data-test="books-form" @submit.prevent="addAccount"
            @keydown.esc="accountForm = null">
        <h2>{{ t('ct.connectTitle') }}</h2>
        <p class="faint small">{{ t('ct.connectHint') }}</p>
        <label>{{ t('ct.url') }} <input v-model="accountForm.url" class="input" type="url" required placeholder="https://cloud.example.com/remote.php/dav" data-test="books-url" /></label>
        <div class="two">
          <label>{{ t('ct.login') }} <input v-model="accountForm.username" class="input" autocomplete="username" data-test="books-user" /></label>
          <label>{{ t('login.password') }} <input v-model="accountForm.password" class="input" type="password" autocomplete="new-password" data-test="books-password" /></label>
        </div>
        <label>{{ t('ct.name') }} <input v-model="accountForm.name" class="input" :placeholder="t('ct.namePh')" /></label>
        <p v-if="accountError" class="error" role="alert">{{ accountError }}</p>
        <div class="actions">
          <button class="btn primary" :disabled="accountBusy">{{ t(accountBusy ? 'ct.connecting' : 'ct.connect') }}</button>
          <button type="button" class="btn" @click="accountForm = null">{{ t('common.cancel') }}</button>
        </div>
      </form>
    </div>
  </div>
</template>

<style scoped>
.contacts { display: grid; grid-template-columns: 230px 340px minmax(0, 1fr); height: 100%; min-height: 0; background: var(--pane-alt); }
.sources { display: flex; flex-direction: column; gap: 2px; padding: 16px 10px; border-right: 1px solid var(--border); overflow: auto; }
h1 { font-size: 17px; margin: 0 8px 10px; }
.src { display: flex; align-items: center; gap: 10px; min-height: 34px; padding: 0 10px; border: 0; border-radius: 6px; background: none; text-align: left; color: var(--text); cursor: pointer; }
.src:hover { background: var(--hover); }
.src.on { background: var(--selected); font-weight: 600; }
.src .ellipsis { flex: 1; }
.src.add { color: var(--accent); }
.grp .mini { width: 24px; height: 24px; opacity: 0; }
/* Видны при наведении, у выбранной группы и при фокусе с клавиатуры; на сенсорных экранах — всегда. */
.grp:hover .mini, .grp.on .mini, .grp .mini:focus-visible { opacity: 1; }
@media (hover: none) { .grp .mini { opacity: 1; } }
.sep { margin: 12px 10px 4px; font-size: 12px; font-weight: 600; color: var(--text-2); }
.list { display: flex; flex-direction: column; min-height: 0; background: var(--pane); border-right: 1px solid var(--border); }
.toolbar { display: flex; align-items: center; gap: 6px; padding: 10px 12px 0; }
.search { flex: 1; display: flex; align-items: center; gap: 8px; height: 32px; padding: 0 10px; border-radius: 6px; background: var(--pane-alt); border: 1px solid var(--border); }
.search input { flex: 1; min-width: 0; border: 0; background: transparent; outline: none; }
.bulk { display: flex; align-items: center; gap: 8px; margin: 10px 12px 0; padding: 6px 8px; border-radius: 8px; background: var(--accent-soft); }
.bulk .sel { width: auto; height: 30px; }
.items { flex: 1; padding: 8px 0; }
.item { display: flex; align-items: center; gap: 10px; padding: 8px 14px; cursor: pointer; }
.item:hover { background: var(--hover); }
.item.on { background: var(--selected); }
.item .main { flex: 1; min-width: 0; display: grid; }
.hint { padding: 8px 14px; font-size: 12px; }
.link { border: 0; background: none; color: var(--accent); padding: 0; justify-self: start; text-decoration: none; }
.small { font-size: 12px; }
.detail { padding: 20px 28px; }
.card { display: grid; gap: 12px; max-width: 620px; padding: 18px; border: 1px solid var(--border); border-radius: 10px; background: var(--pane); }
.card h2 { margin: 0; font-size: 18px; }
.card label { display: grid; gap: 6px; color: var(--text-2); }
.card label.check { display: flex; align-items: center; gap: 6px; color: var(--text); }
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.row { display: flex; gap: 8px; align-items: center; }
.row .sel, .sel { width: 140px; flex: none; }
.group-label { color: var(--text-2); margin-top: 4px; }
.checks { display: flex; flex-wrap: wrap; gap: 14px; }
.area { height: auto; padding: 8px 10px; resize: vertical; }
.actions { display: flex; gap: 8px; flex-wrap: wrap; }
.error { color: var(--danger); margin: 0; }
.view .head { display: flex; gap: 16px; align-items: center; }
.line { display: flex; align-items: center; gap: 10px; }
.notes { white-space: pre-wrap; margin: 4px 0 0; color: var(--text-2); }
.empty { margin-top: 30vh; text-align: center; }
.row-sep { display: flex; align-items: center; justify-content: space-between; }
.mini.vis { opacity: 1; width: 24px; height: 24px; }
.spin :deep(svg) { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.acct { display: flex; align-items: center; gap: 6px; margin: 6px 10px 2px; font-size: 12px; color: var(--text-2); }
.acct .ellipsis { flex: 1; }
.acct .mini { width: 22px; height: 22px; opacity: 0; }
.acct:hover .mini, .acct .mini:focus-visible { opacity: 1; }
@media (hover: none) { .acct .mini { opacity: 1; } }
.acct-err { margin: 0 10px 4px; font-size: 12px; color: var(--danger); }
.warn { color: var(--danger); flex: none; }
.overlay { position: fixed; inset: 0; z-index: 70; display: grid; place-items: center; padding: 16px; background: rgb(0 0 0 / 35%); }
.dialog { width: min(560px, 100%); box-shadow: var(--shadow); }
@media (max-width: 1100px) { .contacts { grid-template-columns: 200px 300px minmax(0, 1fr); } }
@media (max-width: 760px) {
  .contacts { grid-template-columns: 1fr; grid-template-rows: auto auto 1fr; overflow: auto; }
  .sources { flex-direction: row; overflow-x: auto; border-right: 0; border-bottom: 1px solid var(--border); padding: 8px; }
  .sources h1, .sep { display: none; }
  .src { flex: none; }
  .list { border-right: 0; max-height: 50vh; }
  .detail { padding: 12px; }
  .two { grid-template-columns: 1fr; }
}
</style>
