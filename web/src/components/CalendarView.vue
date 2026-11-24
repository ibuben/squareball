<script setup lang="ts">
// Календарь: месяц, неделя и список. События запрашиваются у сервера за
// показанный диапазон; сервер ходит в CalDAV и разворачивает повторения.
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { AlertTriangle, ChevronLeft, ChevronRight, Lock, MapPin, Plus, RefreshCw, Repeat } from 'lucide-vue-next'
import { api, type CalendarInfo, type CalError } from '../api'
import {
  addDays, browserTZ, calColor, isoLocal, layoutDay, longEvent, onDay, sameDay, startOfDay, startOfMonth, startOfWeek,
  toOcc, type Occ,
} from '../calendar'
import { dateLocale, t, tn, type Key } from '../i18n'
import { reportError } from '../store'
import EventDialog from './EventDialog.vue'

type Mode = 'month' | 'week' | 'list'
const STORE = 'wm.cal'

function loadSaved(): { mode?: Mode; hidden?: string[] } {
  try { return JSON.parse(localStorage.getItem(STORE) || '{}') } catch { return {} }
}
const saved = loadSaved()
const narrow = window.matchMedia('(max-width: 760px)').matches
const mode = ref<Mode>(narrow ? 'list' : saved.mode ?? 'month')
const hidden = ref(new Set<string>(saved.hidden ?? []))
function persist() {
  try { localStorage.setItem(STORE, JSON.stringify({ mode: mode.value, hidden: [...hidden.value] })) } catch { /* ignore */ }
}

const cursor = ref(startOfDay(new Date()))
const today = ref(startOfDay(new Date()))
const enabled = ref(true)
const calendars = ref<CalendarInfo[]>([])
const calErrors = ref<Record<string, CalError>>({})
const events = ref<Occ[]>([])
const loading = ref(false)
const truncated = ref(false)

const colorOf = computed(() => {
  const m: Record<string, string> = {}
  calendars.value.forEach((c, i) => { m[c.id] = calColor(i) })
  return m
})

// Показанный диапазон [from, to).
const range = computed(() => {
  const c = cursor.value
  if (mode.value === 'month') {
    const from = startOfWeek(startOfMonth(c))
    return { from, to: addDays(from, 42) }
  }
  if (mode.value === 'week') {
    const from = startOfWeek(c)
    return { from, to: addDays(from, 7) }
  }
  return { from: c, to: addDays(c, 30) }
})

const days = computed(() => {
  const out: Date[] = []
  for (let d = range.value.from; d < range.value.to; d = addDays(d, 1)) out.push(d)
  return out
})

const cap = (s: string) => s.charAt(0).toLocaleUpperCase() + s.slice(1)
const title = computed(() => {
  const { from, to } = range.value
  if (mode.value === 'month') return cap(cursor.value.toLocaleDateString(dateLocale(), { month: 'long', year: 'numeric' }))
  const last = addDays(to, -1)
  const fmt = (d: Date, year: boolean) => d.toLocaleDateString(dateLocale(), { day: 'numeric', month: 'short', ...(year ? { year: 'numeric' } : {}) })
  return `${fmt(from, from.getFullYear() !== last.getFullYear())} — ${fmt(last, true)}`
})

let seq = 0
async function load() {
  const n = ++seq
  loading.value = true
  try {
    const r = await api.calendarEvents(isoLocal(range.value.from), isoLocal(range.value.to), browserTZ())
    if (n !== seq) return
    events.value = r.events.map(toOcc)
    truncated.value = r.truncated
    calErrors.value = { ...calErrors.value, ...r.errors }
  } catch (e) { reportError(e) } finally { if (n === seq) loading.value = false }
}

async function loadCalendars(refresh = false) {
  try {
    const r = await api.calendars(refresh)
    enabled.value = r.enabled
    calendars.value = r.calendars
    calErrors.value = r.errors
  } catch (e) { reportError(e) }
}

async function reload() {
  await loadCalendars(true)
  await load()
}

onMounted(async () => {
  await loadCalendars()
  if (enabled.value) await load()
  await nextTick()
  scrollToMorning()
})
watch([mode, () => cursor.value.getTime()], () => { persist(); void load() })
watch(mode, async () => { await nextTick(); scrollToMorning() })

const visible = computed(() => events.value.filter((o) => !hidden.value.has(o.cal)))
function toggle(id: string) {
  const s = new Set(hidden.value)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  hidden.value = s
  persist()
}

function move(dir: -1 | 1) {
  const c = cursor.value
  if (mode.value === 'month') cursor.value = new Date(c.getFullYear(), c.getMonth() + dir, 1)
  else cursor.value = addDays(c, dir * (mode.value === 'week' ? 7 : 30))
}
function goToday() { today.value = startOfDay(new Date()); cursor.value = today.value }

// --- месяц ---
const weekdays = computed(() => days.value.slice(0, 7).map((d) => d.toLocaleDateString(dateLocale(), { weekday: 'short' })))
const MAX_IN_CELL = 3
function dayEvents(d: Date) {
  return visible.value.filter((o) => onDay(o, d)).sort((a, b) => Number(!longEvent(a)) - Number(!longEvent(b)) || a.s.getTime() - b.s.getTime())
}
function openWeek(d: Date) { cursor.value = d; mode.value = 'week' }

// --- неделя ---
const HOUR = 44
const hours = Array.from({ length: 24 }, (_, i) => i)
const grid = ref<HTMLElement>()
function scrollToMorning() { if (grid.value) grid.value.scrollTop = HOUR * 8 - 12 }
const hourLabel = (h: number) => new Date(2000, 0, 1, h).toLocaleTimeString(dateLocale(), { hour: '2-digit', minute: '2-digit' })
const allDayRow = (d: Date) => visible.value.filter((o) => longEvent(o) && onDay(o, d))
const placed = (d: Date) => layoutDay(visible.value.filter((o) => !longEvent(o) && onDay(o, d)), d, HOUR)
const nowTop = computed(() => (new Date().getHours() + new Date().getMinutes() / 60) * HOUR)
function slotClick(d: Date, e: MouseEvent) {
  const y = e.offsetY
  const h = Math.max(0, Math.min(23, Math.floor(y / HOUR)))
  const half = y % HOUR >= HOUR / 2 ? 30 : 0
  create(new Date(d.getFullYear(), d.getMonth(), d.getDate(), h, half))
}

// --- список ---
const agenda = computed(() =>
  days.value.map((d) => ({ d, list: dayEvents(d) })).filter((x) => x.list.length))

// --- подписи ---
const time = (d: Date) => d.toLocaleTimeString(dateLocale(), { hour: '2-digit', minute: '2-digit' })
function span(o: Occ): string {
  if (o.all_day) return t('cal.allDay')
  return `${time(o.s)}–${time(o.e)}`
}
const dayTitle = (d: Date) => d.toLocaleDateString(dateLocale(), { weekday: 'long', day: 'numeric', month: 'long' })
const errText = (code: CalError) => t((code === 'auth' ? 'cal.err.auth' : 'cal.err.unavailable') as Key)
const firstError = computed(() => Object.values(calErrors.value).find(Boolean) as CalError | undefined)

// --- окно события ---
const dialog = ref<{ occ?: Occ; start?: Date; allDay?: boolean } | null>(null)
// Заголовок группы календарей пресета: общие календари домена — по-своему.
const groupName = (c: CalendarInfo) => (c.preset === 'shared' ? t('cal.shared') : c.preset_name || c.preset)
const canCreate = computed(() => calendars.value.some((c) => !c.readonly))
function create(start?: Date, allDay = false) {
  if (!canCreate.value) return
  const s = start ?? (() => {
    const n = new Date()
    const base = sameDay(cursor.value, today.value) ? n : cursor.value
    return new Date(base.getFullYear(), base.getMonth(), base.getDate(), sameDay(base, n) ? n.getHours() + 1 : 9)
  })()
  dialog.value = { start: s, allDay }
}
function open(o: Occ) { dialog.value = { occ: o } }
function onSaved() { dialog.value = null; void load() }
</script>

<template>
  <div class="cal" data-test="calendar">
    <aside class="side">
      <h1>{{ t('nav.calendar') }}</h1>
      <button class="btn primary new" :disabled="!canCreate" data-test="event-new" @click="create()"><Plus :size="15" /> {{ t('ev.new') }}</button>
      <div class="sep row-sep">
        <span>{{ t('cal.calendars') }}</span>
        <button class="icon-btn mini" :class="{ spin: loading }" :title="t('cal.refresh')" :aria-label="t('cal.refresh')" @click="reload">
          <RefreshCw :size="13" /></button>
      </div>
      <template v-for="(c, i) in calendars" :key="c.id">
        <div v-if="i > 0 && c.preset !== calendars[i - 1].preset" class="sep group" data-test="cal-group">{{ groupName(c) }}</div>
        <label class="calrow">
          <input type="checkbox" :checked="!hidden.has(c.id)" :style="{ accentColor: colorOf[c.id] }" @change="toggle(c.id)">
          <span class="dot" :style="{ background: colorOf[c.id] }" />
          <span class="ellipsis">{{ c.name }}</span>
          <Lock v-if="c.readonly" :size="12" class="faint" :title="t('cal.readonly')" />
          <AlertTriangle v-if="calErrors[c.id]" :size="13" class="warn" :title="errText(calErrors[c.id])" />
        </label>
      </template>
      <p v-if="!calendars.length && !firstError" class="faint small">{{ enabled ? t('cal.none') : t('cal.disabled') }}</p>
    </aside>

    <section class="body">
      <div class="toolbar">
        <button class="btn" @click="goToday">{{ t('cal.today') }}</button>
        <button class="icon-btn" :aria-label="t('cal.prev')" :title="t('cal.prev')" @click="move(-1)"><ChevronLeft :size="18" /></button>
        <button class="icon-btn" :aria-label="t('cal.next')" :title="t('cal.next')" @click="move(1)"><ChevronRight :size="18" /></button>
        <h2 class="ellipsis">{{ title }}</h2>
        <span class="spacer" />
        <div class="modes" role="tablist">
          <button v-for="m in (['month', 'week', 'list'] as Mode[])" :key="m" role="tab" class="btn" :class="{ on: mode === m }"
                  :aria-selected="mode === m" :data-test="`cal-${m}`" @click="mode = m">{{ t(`cal.${m}` as Key) }}</button>
        </div>
        <button class="icon-btn fab-new" :disabled="!canCreate" :aria-label="t('ev.new')" @click="create()"><Plus :size="18" /></button>
      </div>
      <p v-if="firstError" class="banner"><AlertTriangle :size="14" /> {{ errText(firstError) }}</p>
      <p v-if="truncated" class="banner">{{ t('cal.truncated') }}</p>

      <!-- Месяц -->
      <div v-if="mode === 'month'" class="month">
        <div v-for="w in weekdays" :key="w" class="wd">{{ w }}</div>
        <div v-for="d in days" :key="d.getTime()" class="cell" :class="{ other: d.getMonth() !== cursor.getMonth(), today: sameDay(d, today) }"
             @click.self="create(new Date(d.getFullYear(), d.getMonth(), d.getDate(), 9))">
          <button class="num" @click="openWeek(d)">{{ d.getDate() }}</button>
          <button v-for="o in dayEvents(d).slice(0, MAX_IN_CELL)" :key="o.key" class="chip" :class="{ long: longEvent(o), cancelled: o.status === 'CANCELLED' }"
                  :style="{ '--c': colorOf[o.cal] }" :title="`${span(o)} ${o.summary}`" @click="open(o)">
            <span v-if="!longEvent(o)" class="t">{{ time(o.s) }}</span>
            <span class="ellipsis">{{ o.summary || t('cal.untitled') }}</span>
          </button>
          <button v-if="dayEvents(d).length > MAX_IN_CELL" class="more" @click="openWeek(d)">
            {{ tn('cal.more', dayEvents(d).length - MAX_IN_CELL) }}</button>
        </div>
      </div>

      <!-- Неделя -->
      <div v-else-if="mode === 'week'" ref="grid" class="week scroll">
        <div class="whead">
          <div class="gutter" />
          <div v-for="d in days" :key="d.getTime()" class="dh" :class="{ today: sameDay(d, today) }">
            <span class="dw">{{ d.toLocaleDateString(dateLocale(), { weekday: 'short' }) }}</span>
            <span class="dn">{{ d.getDate() }}</span>
          </div>
          <div class="gutter allday-lbl faint">{{ t('cal.allDay') }}</div>
          <div v-for="d in days" :key="`a${d.getTime()}`" class="allday" @click.self="create(d, true)">
            <button v-for="o in allDayRow(d)" :key="o.key" class="chip long" :style="{ '--c': colorOf[o.cal] }" :title="o.summary" @click="open(o)">
              <span class="ellipsis">{{ o.summary || t('cal.untitled') }}</span></button>
          </div>
        </div>
        <div class="wgrid">
          <div class="hours">
            <div v-for="h in hours" :key="h" class="hl" :style="{ height: `${HOUR}px` }"><span>{{ h ? hourLabel(h) : '' }}</span></div>
          </div>
          <div v-for="d in days" :key="d.getTime()" class="col" :style="{ height: `${HOUR * 24}px` }" @click.self="slotClick(d, $event)">
            <div v-if="sameDay(d, today)" class="now" :style="{ top: `${nowTop}px` }" />
            <button v-for="p in placed(d)" :key="p.o.key" class="ev" :class="{ cancelled: p.o.status === 'CANCELLED' }"
                    :style="{ '--c': colorOf[p.o.cal], top: `${p.top}px`, height: `${p.height}px`,
                              left: `calc(${(100 / p.cols) * p.col}% + 2px)`, width: `calc(${100 / p.cols}% - 4px)` }"
                    :title="`${span(p.o)} ${p.o.summary}`" @click="open(p.o)">
              <span class="ellipsis s">{{ p.o.summary || t('cal.untitled') }}</span>
              <span class="ellipsis tm">{{ span(p.o) }}</span>
            </button>
          </div>
        </div>
      </div>

      <!-- Список -->
      <div v-else class="agenda scroll">
        <p v-if="!agenda.length && !loading" class="faint empty">{{ t('cal.empty') }}</p>
        <section v-for="g in agenda" :key="g.d.getTime()" class="aday">
          <h3 :class="{ today: sameDay(g.d, today) }">{{ dayTitle(g.d) }}</h3>
          <button v-for="o in g.list" :key="o.key" class="arow" :class="{ cancelled: o.status === 'CANCELLED' }" @click="open(o)">
            <span class="dot" :style="{ background: colorOf[o.cal] }" />
            <span class="at">{{ span(o) }}</span>
            <span class="what">
              <span class="ellipsis s">{{ o.summary || t('cal.untitled') }}</span>
              <span v-if="o.location" class="faint ellipsis loc"><MapPin :size="12" /> {{ o.location }}</span>
            </span>
            <Repeat v-if="o.recurring" :size="13" class="faint" :aria-label="t('inv.recurring')" />
          </button>
        </section>
      </div>
    </section>

    <EventDialog v-if="dialog" :calendars="calendars" :occ="dialog.occ" :start="dialog.start" :all-day="dialog.allDay"
                 @close="dialog = null" @saved="onSaved" />
  </div>
</template>

<style scoped>
.cal { display: grid; grid-template-columns: 220px minmax(0, 1fr); height: 100%; min-height: 0; background: var(--pane-alt); }
.side { display: flex; flex-direction: column; gap: 4px; padding: 16px 10px; border-right: 1px solid var(--border); overflow: auto; }
h1 { font-size: 17px; margin: 0 8px 10px; }
.new { justify-content: center; margin: 0 4px 8px; height: 34px; }
.sep { margin: 12px 10px 4px; font-size: 12px; font-weight: 600; color: var(--text-2); display: flex; align-items: center; justify-content: space-between; }
.mini { width: 24px; height: 24px; }
.spin :deep(svg) { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.sep.group { margin-top: 14px; }
.calrow { display: flex; align-items: center; gap: 8px; min-height: 30px; padding: 0 10px; border-radius: 6px; cursor: pointer; }
.calrow:hover { background: var(--hover); }
.calrow .ellipsis { flex: 1; }
.dot { flex: none; width: 9px; height: 9px; border-radius: 50%; }
.warn { color: var(--danger); flex: none; }
.small { font-size: 12px; margin: 4px 10px; }

.body { display: flex; flex-direction: column; min-height: 0; min-width: 0; background: var(--pane); }
.toolbar { display: flex; align-items: center; gap: 6px; padding: 10px 14px; border-bottom: 1px solid var(--border); }
.toolbar h2 { margin: 0 0 0 6px; font-size: 16px; font-weight: 600; min-width: 0; }
.spacer { flex: 1; }
.modes { display: inline-flex; }
.modes .btn { border-radius: 0; margin-left: -1px; }
.modes .btn:first-child { border-radius: 6px 0 0 6px; }
.modes .btn:last-child { border-radius: 0 6px 6px 0; }
.modes .btn.on { background: var(--selected); font-weight: 600; }
.fab-new { display: none; }
.banner { margin: 8px 14px 0; padding: 6px 10px; border-radius: 6px; background: var(--accent-soft); display: flex; align-items: center; gap: 6px; font-size: 12px; }

.chip {
  display: flex; align-items: center; gap: 4px; width: 100%; min-width: 0; height: 20px; padding: 0 5px; border: 0; border-radius: 4px;
  background: transparent; color: var(--text); font-size: 12px; text-align: left;
}
.chip:hover { background: var(--hover); }
.chip::before { content: ''; flex: none; width: 6px; height: 6px; border-radius: 50%; background: var(--c); }
.chip.long { background: color-mix(in srgb, var(--c) 18%, transparent); }
.chip.long::before { display: none; }
.chip .t { flex: none; color: var(--text-2); }
.cancelled .ellipsis, .cancelled .s { text-decoration: line-through; color: var(--text-3); }

.month { flex: 1; display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); grid-template-rows: auto repeat(6, minmax(0, 1fr)); min-height: 0; }
.wd { padding: 6px 8px; font-size: 12px; color: var(--text-2); border-bottom: 1px solid var(--border); text-transform: capitalize; }
.cell { display: flex; flex-direction: column; gap: 1px; padding: 2px 3px 4px; min-height: 0; overflow: hidden; border-right: 1px solid var(--border); border-bottom: 1px solid var(--border); cursor: default; }
.cell:nth-child(7n) { border-right: 0; }
.cell.other { background: var(--pane-alt); }
.cell.other .num { color: var(--text-3); }
.num { align-self: flex-start; min-width: 24px; height: 24px; padding: 0 6px; border: 0; border-radius: 12px; background: none; font-weight: 600; font-size: 12px; }
.num:hover { background: var(--hover); }
.cell.today .num { background: var(--accent); color: var(--accent-contrast); }
.more { border: 0; background: none; color: var(--text-2); font-size: 12px; text-align: left; padding: 0 5px; }
.more:hover { color: var(--accent); }

.week { flex: 1; min-height: 0; }
.whead { position: sticky; top: 0; z-index: 3; display: grid; grid-template-columns: 64px repeat(7, minmax(0, 1fr)); border-bottom: 1px solid var(--border); background: var(--pane); }
.dh { display: flex; flex-direction: column; align-items: center; padding: 6px 0 4px; }
.dw { font-size: 12px; color: var(--text-2); text-transform: capitalize; }
.dn { font-size: 18px; font-weight: 600; width: 32px; height: 32px; display: grid; place-items: center; border-radius: 50%; }
.dh.today .dn { background: var(--accent); color: var(--accent-contrast); }
.allday-lbl { font-size: 11px; display: flex; align-items: center; justify-content: flex-end; padding-right: 6px; text-align: right; line-height: 1.1; }
.allday { display: flex; flex-direction: column; gap: 1px; padding: 2px; min-height: 24px; border-left: 1px solid var(--border); }
.wgrid { display: grid; grid-template-columns: 64px repeat(7, minmax(0, 1fr)); }
.hours .hl { position: relative; }
.hours .hl span { position: absolute; top: -8px; right: 6px; font-size: 11px; color: var(--text-3); }
.col {
  position: relative; border-left: 1px solid var(--border);
  background-image: repeating-linear-gradient(to bottom, var(--border) 0 1px, transparent 1px 44px);
}
.now { position: absolute; left: 0; right: 0; height: 2px; background: var(--danger); z-index: 2; pointer-events: none; }
.ev {
  position: absolute; display: flex; flex-direction: column; align-items: flex-start; gap: 0; padding: 2px 6px; overflow: hidden;
  border: 0; border-left: 3px solid var(--c); border-radius: 4px; background: color-mix(in srgb, var(--c) 18%, var(--pane));
  color: var(--text); font-size: 12px; text-align: left; line-height: 1.3; z-index: 1;
}
.ev:hover { background: color-mix(in srgb, var(--c) 28%, var(--pane)); }
.ev .s { font-weight: 600; max-width: 100%; }
.ev .tm { color: var(--text-2); font-size: 11px; max-width: 100%; }

.agenda { flex: 1; padding: 6px 0 20px; }
.empty { padding: 30px; text-align: center; }
.aday h3 { margin: 14px 16px 4px; font-size: 13px; color: var(--text-2); text-transform: capitalize; }
.aday h3.today { color: var(--accent); }
.arow { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 40px; padding: 4px 16px; border: 0; background: none; text-align: left; }
.arow:hover { background: var(--hover); }
.at { flex: none; width: 110px; color: var(--text-2); font-size: 12px; }
.what { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.what .s { font-weight: 600; }
.loc { display: inline-flex; align-items: center; gap: 3px; font-size: 12px; }

@media (max-width: 960px) {
  .cal { grid-template-columns: minmax(0, 1fr); }
  .side { display: none; }
  .fab-new { display: inline-flex; }
}
@media (max-width: 760px) {
  .toolbar { flex-wrap: wrap; }
  .toolbar h2 { order: -1; flex-basis: 100%; margin: 0 0 4px 2px; }
  .modes { order: 10; width: 100%; }
  .modes .btn { flex: 1; justify-content: center; }
  .at { width: 84px; }
}
</style>
