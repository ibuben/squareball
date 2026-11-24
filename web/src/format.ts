import type { Address } from './api'
import { locale, t } from './i18n'

const loc = locale === 'en' ? 'en-GB' : 'ru-RU'

export function escapeHTML(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!)
}

export function who(a?: Address): string {
  if (!a) return ''
  return a.name || a.email
}

export function whoList(list?: Address[]): string {
  return (list ?? []).map(who).join(', ')
}

export function fullAddr(a: Address): string {
  return a.name ? `${a.name} <${a.email}>` : a.email
}

export function initials(a?: Address): string {
  // Есть имя — первые буквы двух слов; только адрес — буквы из local part (ivan.petrov@ → IP).
  const src = a?.name?.replace(/["']/g, '').trim() || (a?.email ?? '').split('@')[0]
  const parts = src.split(/[\s._-]+/).filter(Boolean)
  return ((parts[0]?.[0] ?? '?') + (parts[1]?.[0] ?? '')).toUpperCase()
}

// Цвет аватара стабилен для адреса.
export function avatarHue(a?: Address): number {
  let h = 0
  for (const ch of a?.email ?? '') h = (h * 31 + ch.charCodeAt(0)) % 360
  return h
}

function startOfDay(d: Date) { return new Date(d.getFullYear(), d.getMonth(), d.getDate()) }

export function listDate(iso: string): string {
  const d = new Date(iso)
  const now = new Date()
  if (startOfDay(d).getTime() === startOfDay(now).getTime()) {
    return d.toLocaleTimeString(loc, { hour: '2-digit', minute: '2-digit' })
  }
  const days = (startOfDay(now).getTime() - startOfDay(d).getTime()) / 864e5
  if (days < 7) return d.toLocaleDateString(loc, { weekday: 'short' })
  if (d.getFullYear() === now.getFullYear()) return d.toLocaleDateString(loc, { day: 'numeric', month: 'short' })
  return d.toLocaleDateString(loc, { day: '2-digit', month: '2-digit', year: 'numeric' })
}

export function fullDate(iso: string): string {
  return new Date(iso).toLocaleString(loc, { dateStyle: 'medium', timeStyle: 'short' })
}

export function dateGroup(iso: string): string {
  const d = startOfDay(new Date(iso)).getTime()
  const today = startOfDay(new Date()).getTime()
  const days = (today - d) / 864e5
  if (days <= 0) return t('group.today')
  if (days === 1) return t('group.yesterday')
  if (days < 7) return t('group.week')
  const now = new Date()
  const dd = new Date(iso)
  if (dd.getMonth() === now.getMonth() && dd.getFullYear() === now.getFullYear()) return t('group.month')
  return t('group.older')
}

export function size(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

export function replySubject(s: string, prefix: 'Re' | 'Fwd'): string {
  const re = prefix === 'Re' ? /^(re|ответ|aw)\s*:/i : /^(fwd?|пересл)\s*:/i
  return re.test(s.trim()) ? s : `${prefix}: ${s}`
}

// Разбор строки адресов "Имя <a@b>, c@d".
export function parseAddresses(s: string): Address[] {
  const out: Address[] = []
  for (const part of s.split(/[,;]/)) {
    const p = part.trim()
    if (!p) continue
    const m = p.match(/^(.*)<([^>]+)>$/)
    if (m) out.push({ name: m[1].trim().replace(/^"|"$/g, ''), email: m[2].trim() })
    else out.push({ email: p })
  }
  return out
}

export const isEmail = (s: string) => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(s) || /^[^\s@]+@[^\s@]+$/.test(s)
