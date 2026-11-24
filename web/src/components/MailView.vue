<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  CalendarDays, LogOut, Mail, Menu, Monitor, Moon, PanelRight, PenSquare, Settings, Sun, Users,
} from 'lucide-vue-next'
import { api } from '../api'
import { t } from '../i18n'
import FolderPane from './FolderPane.vue'
// Настройки (с редактором подписи) — отдельный чанк, грузится при первом открытии.
const SettingsView = defineAsyncComponent(() => import('./SettingsView.vue'))
const ContactsView = defineAsyncComponent(() => import('./ContactsView.vue'))
import MessageList from './MessageList.vue'
import ReadingPane from './ReadingPane.vue'
import ContactPane from './ContactPane.vue'
import {
  applyTheme, deleteSelected, loadFolders, loadIdentities, loadList, loadPrefs, openCompose, openFolder, openMessage, setMe, state,
} from '../store'

const reading = ref<InstanceType<typeof ReadingPane>>()
const menu = ref(false)
const drawer = ref(false) // панель папок на узких экранах

// Ширины панелей можно менять перетаскиванием; сохраняются в браузере.
const widths = ref<{ folders: number; list: number }>(
  (() => { try { return JSON.parse(localStorage.getItem('wm.widths') || '') } catch { return null } })()
  ?? { folders: 220, list: 380 },
)
function startResize(which: 'folders' | 'list', e: PointerEvent) {
  const start = e.clientX
  const initial = widths.value[which]
  const move = (ev: PointerEvent) => {
    const min = which === 'folders' ? 160 : 280
    widths.value = { ...widths.value, [which]: Math.max(min, Math.min(640, initial + ev.clientX - start)) }
  }
  const up = () => {
    window.removeEventListener('pointermove', move)
    window.removeEventListener('pointerup', up)
    try { localStorage.setItem('wm.widths', JSON.stringify(widths.value)) } catch { /* ignore */ }
  }
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', up)
}

const columns = computed(() =>
  `${widths.value.folders}px 1px ${widths.value.list}px 1px minmax(0,1fr)${state.showContact ? ' 260px' : ''}`)
function go(view: 'mail' | 'settings' | 'contacts') { state.view = view; menu.value = false; drawer.value = false }

function toggleContact() {
  state.showContact = !state.showContact
  try { localStorage.setItem('wm.contact', state.showContact ? '1' : '0') } catch { /* ignore */ }
}
function setTheme(th: 'light' | 'dark' | 'system') { state.theme = th; applyTheme(); menu.value = false }

async function logout() {
  try { await api.logout() } finally { setMe(null) }
}

function step(dir: 1 | -1) {
  const i = state.messages.findIndex((m) => m.uid === state.currentUID)
  const next = state.messages[i < 0 ? 0 : i + dir]
  if (next) void openMessage(next.uid)
}

function onKey(e: KeyboardEvent) {
  const el = e.target as HTMLElement
  if (el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName) || e.ctrlKey || e.metaKey || e.altKey) return
  const map: Record<string, () => void> = {
    c: () => openCompose(),
    r: () => reading.value?.reply(false),
    a: () => reading.value?.reply(true),
    f: () => reading.value?.forward(),
    j: () => step(1), k: () => step(-1),
    Delete: () => void deleteSelected(), '#': () => void deleteSelected(),
    '/': () => (document.querySelector('[data-search]') as HTMLInputElement | null)?.focus(),
  }
  const fn = map[e.key]
  if (fn) { e.preventDefault(); fn() }
}

// Обновление: «Входящие» приходят push-событиями (IMAP IDLE → SSE), остальные папки —
// опросом. Пока поток жив, опрос редкий; если поток недоступен — раз в 30 секунд.
let timer = 0
let lastPoll = 0
let events: EventSource | null = null
const live = ref(false)

async function refresh(force = false) {
  if (!force && document.hidden) return
  lastPoll = Date.now()
  await loadFolders()
  if (!state.search && state.messages.length <= 50) await loadList()
}

function connectEvents() {
  events = new EventSource('/api/v1/events')
  events.addEventListener('ready', () => { live.value = true })
  events.addEventListener('mailbox', async () => {
    await loadFolders()
    const inbox = state.folders.find((f) => f.role === 'inbox')
    if (inbox && state.folder === inbox.name && !state.search && state.messages.length <= 50) await loadList()
  })
  // При обрыве EventSource переподключается сам; пока его нет — работает частый опрос.
  events.onerror = () => { live.value = false }
}

// Число непрочитанных во «Входящих» — в заголовке вкладки.
watch(() => state.folders.find((f) => f.role === 'inbox')?.unread ?? 0, (n) => {
  const base = `${state.me?.mailbox} — ${state.me?.brand.name}`
  document.title = n ? `(${n}) ${base}` : base
})

onMounted(async () => {
  window.addEventListener('keydown', onKey)
  await loadFolders()
  await openFolder(state.folders.find((f) => f.role === 'inbox')?.name ?? 'INBOX')
  void loadIdentities()
  void loadPrefs()
  connectEvents()
  lastPoll = Date.now()
  timer = window.setInterval(() => {
    const every = live.value ? 120_000 : 30_000
    if (Date.now() - lastPoll >= every) void refresh()
  }, 10_000)
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKey)
  clearInterval(timer)
  events?.close()
})
</script>

<template>
  <div class="shell" :class="{ reading: state.current, drawer }">
    <header class="top">
      <button class="icon-btn burger" :aria-label="t('nav.mail')" @click="drawer = !drawer"><Menu :size="19" /></button>
      <div class="brand">
        <img v-if="state.me?.brand.logo_url" :src="state.me.brand.logo_url" alt="" />
        <Mail v-else :size="18" />
        <span>{{ state.me?.brand.name }}</span>
      </div>
      <div class="spacer" />
      <button class="icon-btn contact-toggle" :class="{ active: state.showContact }" title="Contact" @click="toggleContact"><PanelRight :size="17" /></button>
      <div class="user">
        <button class="userbtn" @click="menu = !menu">
          <span class="ellipsis">{{ state.me?.mailbox }}</span>
        </button>
        <div v-if="menu" class="menu" @mouseleave="menu = false">
          <div class="faint mlabel">{{ t('menu.theme') }}</div>
          <button @click="setTheme('light')"><Sun :size="15" /> {{ t('theme.light') }}</button>
          <button @click="setTheme('dark')"><Moon :size="15" /> {{ t('theme.dark') }}</button>
          <button @click="setTheme('system')"><Monitor :size="15" /> {{ t('theme.system') }}</button>
          <hr />
          <button @click="go('contacts')"><Users :size="15" /> {{ t('nav.contacts') }}</button>
          <button @click="go('settings')"><Settings :size="15" /> {{ t('nav.settings') }}</button>
          <button @click="logout"><LogOut :size="15" /> {{ t('menu.logout') }}</button>
        </div>
      </div>
    </header>

    <div class="main">
    <nav class="rail">
      <button class="icon-btn" :class="{ active: state.view === 'mail' }" :title="t('nav.mail')" :aria-label="t('nav.mail')" @click="go('mail')"><Mail :size="20" /></button>
      <button class="icon-btn" :class="{ active: state.view === 'contacts' }" :title="t('nav.contacts')" :aria-label="t('nav.contacts')" @click="go('contacts')"><Users :size="20" /></button>
      <button class="icon-btn" :title="t('nav.calendar')" disabled><CalendarDays :size="20" /></button>
      <span class="spacer" />
      <button class="icon-btn" :class="{ active: state.view === 'settings' }" :title="t('nav.settings')" :aria-label="t('nav.settings')" @click="go('settings')"><Settings :size="20" /></button>
    </nav>
    <SettingsView v-if="state.view === 'settings'" />
    <ContactsView v-else-if="state.view === 'contacts'" />
    <div v-else class="panes" :style="{ gridTemplateColumns: columns }">
      <FolderPane class="p-folders" @click="drawer = false" />
      <div class="scrim" @click="drawer = false" />
      <div class="split" @pointerdown="startResize('folders', $event)" />
      <MessageList class="p-list" />
      <div class="split" @pointerdown="startResize('list', $event)" />
      <ReadingPane ref="reading" class="p-read" @back="state.current = null; state.currentUID = 0" />
      <ContactPane v-if="state.showContact" class="p-contact" />
    </div>
    </div>
    <button v-if="state.view === 'mail'" class="fab" :aria-label="t('compose.new')" @click="openCompose()"><PenSquare :size="22" /></button>
  </div>
</template>

<style scoped>
.shell { height: 100%; display: grid; grid-template-rows: 44px 1fr; }
.top { display: flex; align-items: center; gap: 8px; padding: 0 10px 0 16px; background: var(--pane); border-bottom: 1px solid var(--border); }
.brand { display: flex; align-items: center; gap: 8px; font-weight: 600; color: var(--accent); }
.brand img { height: 22px; }
.spacer { flex: 1; }
.user { position: relative; }
.userbtn { max-width: 240px; height: 30px; padding: 0 10px; border: 1px solid var(--border); border-radius: 15px; background: var(--pane-alt); }
.menu { position: absolute; right: 0; top: 36px; z-index: 20; min-width: 200px; padding: 6px; background: var(--pane); border: 1px solid var(--border); border-radius: 8px; box-shadow: var(--shadow); }
.menu button { width: 100%; display: flex; align-items: center; gap: 8px; padding: 7px 8px; border: 0; background: none; border-radius: 6px; text-align: left; }
.menu button:hover { background: var(--hover); }
.menu hr { border: 0; border-top: 1px solid var(--border); margin: 6px 0; }
.mlabel { padding: 4px 8px; font-size: 12px; }
.main { display: grid; grid-template-columns: 52px minmax(0, 1fr); min-height: 0; }
.main > * { min-height: 0; }
.panes { display: grid; min-height: 0; }
.panes > * { min-height: 0; }
.rail { display: flex; flex-direction: column; align-items: center; gap: 6px; padding: 10px 0; background: var(--pane); border-right: 1px solid var(--border); }
.rail .icon-btn { width: 38px; height: 38px; }
.rail .icon-btn:disabled { opacity: 0.4; cursor: default; }
.split { position: relative; cursor: col-resize; background: var(--border); z-index: 2; }
.split::after { content: ''; position: absolute; inset: 0 -4px; }
.split:hover { background: var(--accent); }

.burger, .fab, .scrim { display: none; }
.fab {
  position: fixed; right: 18px; bottom: 18px; z-index: 15; width: 54px; height: 54px; border-radius: 50%;
  border: 0; background: var(--accent); color: var(--accent-contrast); box-shadow: var(--shadow);
  align-items: center; justify-content: center;
}
@media (max-width: 1280px) { .p-contact, .contact-toggle { display: none; } .panes { grid-template-columns: 200px 1px 340px 1px minmax(0,1fr) !important; } }
@media (max-width: 960px) {
  .main { grid-template-columns: minmax(0, 1fr); }
  .panes { grid-template-columns: 0 340px 1px minmax(0,1fr) !important; }
  .rail { display: none; }
  .burger { display: inline-flex; }
  .p-folders { position: fixed; z-index: 25; left: 0; top: 44px; bottom: 0; width: 260px; transform: translateX(-100%); transition: transform .2s; box-shadow: var(--shadow); }
  .shell.drawer .p-folders { transform: none; }
  .shell.drawer .scrim { display: block; position: fixed; inset: 44px 0 0 0; z-index: 24; background: rgba(0,0,0,.3); }
}
@media (max-width: 760px) {
  .panes { grid-template-columns: 1fr !important; }
  .split { display: none; }
  .p-read { display: none; }
  .shell.reading .p-list { display: none; }
  .shell.reading .p-read { display: flex; }
  .fab { display: inline-flex; }
  .shell.reading .fab { display: none; }
}
</style>
