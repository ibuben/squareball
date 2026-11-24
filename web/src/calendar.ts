// Даты для календаря: всё в поясе браузера. Сервер получает диапазон в ISO
// со смещением и имя пояса, а возвращает вхождения уже развёрнутыми.
import type { CalEvent } from './api'
import { lang } from './i18n'

export const browserTZ = () => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'

export const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate())
export const addDays = (d: Date, n: number) => new Date(d.getFullYear(), d.getMonth(), d.getDate() + n, d.getHours(), d.getMinutes())
export const sameDay = (a: Date, b: Date) =>
  a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()

// Первый день недели: понедельник, кроме английского интерфейса с американской локалью.
export function weekStart(): number {
  if (lang() !== 'en') return 1
  return navigator.language === 'en-US' ? 0 : 1
}

export function startOfWeek(d: Date): Date {
  const s = startOfDay(d)
  const shift = (s.getDay() - weekStart() + 7) % 7
  return addDays(s, -shift)
}

export const startOfMonth = (d: Date) => new Date(d.getFullYear(), d.getMonth(), 1)

const pad = (n: number) => String(n).padStart(2, '0')

// ISO-строка с местным смещением: 2026-12-01T09:00:00+05:00.
export function isoLocal(d: Date): string {
  const off = -d.getTimezoneOffset()
  const sign = off >= 0 ? '+' : '-'
  const a = Math.abs(off)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:00${sign}${pad(Math.floor(a / 60))}:${pad(a % 60)}`
}

// Значения полей <input type="date"> и <input type="time">.
export const dateValue = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
export const timeValue = (d: Date) => `${pad(d.getHours())}:${pad(d.getMinutes())}`
export function fromInputs(date: string, time = '00:00'): Date {
  const [y, m, d] = date.split('-').map(Number)
  const [hh, mm] = time.split(':').map(Number)
  return new Date(y, m - 1, d, hh || 0, mm || 0)
}

// Вхождение с разобранными датами.
export interface Occ extends CalEvent { s: Date; e: Date; key: string }

export function toOcc(ev: CalEvent): Occ {
  const s = new Date(ev.start)
  let e = new Date(ev.end)
  if (e < s) e = s
  return { ...ev, s, e, key: `${ev.cal}|${ev.href}|${ev.recurrence_id ?? ''}` }
}

// Пересекает ли событие день d (для событий без длительности — начинается в этот день).
export function onDay(o: Occ, d: Date): boolean {
  const ds = startOfDay(d)
  const de = addDays(ds, 1)
  if (o.e.getTime() === o.s.getTime()) return o.s >= ds && o.s < de
  return o.s < de && o.e > ds
}

// Многодневное событие со временем показываем вместе с событиями на весь день.
export const longEvent = (o: Occ) => o.all_day || o.e.getTime() - o.s.getTime() >= 24 * 3600_000

// Раскладка событий дня в колонки: пересекающиеся события делят ширину.
export interface Placed { o: Occ; top: number; height: number; col: number; cols: number }

export function layoutDay(list: Occ[], day: Date, hourPx: number): Placed[] {
  const ds = startOfDay(day).getTime()
  const de = addDays(startOfDay(day), 1).getTime()
  const items = list
    .map((o) => {
      const s = Math.max(o.s.getTime(), ds)
      const e = Math.min(Math.max(o.e.getTime(), o.s.getTime() + 30 * 60_000), de)
      return { o, s, e }
    })
    .sort((a, b) => a.s - b.s || b.e - a.e)
  const out: Placed[] = []
  let cluster: { p: Placed; e: number }[] = []
  let clusterEnd = 0
  const flush = () => {
    const cols = Math.max(1, ...cluster.map((c) => c.p.col + 1))
    for (const c of cluster) c.p.cols = cols
    cluster = []
  }
  for (const it of items) {
    if (cluster.length && it.s >= clusterEnd) flush()
    const used = new Set(cluster.filter((c) => c.e > it.s).map((c) => c.p.col))
    let col = 0
    while (used.has(col)) col++
    const p: Placed = {
      o: it.o, col, cols: 1,
      top: ((it.s - ds) / 3600_000) * hourPx,
      height: Math.max(((it.e - it.s) / 3600_000) * hourPx, hourPx / 2) - 2,
    }
    cluster.push({ p, e: it.e })
    clusterEnd = Math.max(clusterEnd, it.e)
    out.push(p)
  }
  if (cluster.length) flush()
  return out
}

// Цвет календаря по порядку; набор подобран читаемым в светлой и тёмной темах.
const PALETTE = ['#3b82f6', '#16a34a', '#d97706', '#db2777', '#7c3aed', '#0891b2', '#dc2626', '#4d7c0f']
export const calColor = (i: number) => PALETTE[((i % PALETTE.length) + PALETTE.length) % PALETTE.length]

// Повторение: варианты формы и правила RRULE.
export const REPEATS = {
  none: '',
  daily: 'FREQ=DAILY',
  weekdays: 'FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR',
  weekly: 'FREQ=WEEKLY',
  monthly: 'FREQ=MONTHLY',
  yearly: 'FREQ=YEARLY',
} as const
export type Repeat = keyof typeof REPEATS | 'custom'

export function repeatOf(rrule = ''): Repeat {
  if (!rrule) return 'none'
  for (const [k, v] of Object.entries(REPEATS)) if (v && v === rrule) return k as Repeat
  return 'custom'
}
