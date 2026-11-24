// Строки интерфейса. Словари — в locales/: en.ts — эталонный (все ключи), остальные
// типизированы по нему, так что непереведённый ключ — ошибка сборки.
// Язык: выбор пользователя (localStorage) → ui.default_language с сервера → английский.
// Текущий язык — реактивный: компоненты, вызывающие t() в шаблоне, перерисуются при смене.
import { ref } from 'vue'
import en from './locales/en'
import ru from './locales/ru'

export type Key = keyof typeof en

// Название языка — на самом языке, чтобы его нашёл тот, кто не читает текущий.
export const LANGS = { en: 'English', ru: 'Русский' } as const
export type Lang = keyof typeof LANGS

const dicts: Record<Lang, Record<Key, string>> = { en, ru }
const STORE = 'wm.lang'
export const isLang = (v: unknown): v is Lang => typeof v === 'string' && v in LANGS

function stored(): Lang | null {
  try {
    const v = localStorage.getItem(STORE)
    return isLang(v) ? v : null
  } catch { return null }
}

const current = ref<Lang>(stored() ?? 'en')
document.documentElement.lang = current.value

export const lang = () => current.value

// Явный выбор пользователя — запоминается в браузере.
export function setLang(l: Lang) {
  current.value = l
  document.documentElement.lang = l
  try { localStorage.setItem(STORE, l) } catch { /* приватный режим */ }
}

// Язык установки (ui.default_language) — только если пользователь ещё не выбирал сам.
export function setDefaultLang(l: unknown) {
  if (stored() || !isLang(l)) return
  current.value = l
  document.documentElement.lang = l
}

// Языки, которые можно выбрать: из ui.languages те, для которых есть словарь.
export function availableLangs(allowed?: string[]): Lang[] {
  const all = Object.keys(LANGS) as Lang[]
  const list = allowed?.length ? all.filter((l) => allowed.includes(l)) : all
  return list.length ? list : all
}

// t('key', { n: 3 }) — подстановка {n} в строку.
export function t(key: Key, params?: Record<string, string | number>): string {
  const s = dicts[current.value][key] ?? en[key]
  return params ? s.replace(/\{(\w+)\}/g, (m, p: string) => (p in params ? String(params[p]) : m)) : s
}

// Локаль для дат и чисел.
export const dateLocale = () => (current.value === 'ru' ? 'ru-RU' : 'en-GB')

// Множественное число: tn('inv.people', 5) → ключ inv.people.one/few/many/other по правилам языка, {n} — число.
export function tn(base: string, n: number, params?: Record<string, string | number>): string {
  const exact = `${base}.${new Intl.PluralRules(current.value).select(n)}`
  const key = (exact in en ? exact : `${base}.other`) as Key
  return t(key, { n, ...params })
}
