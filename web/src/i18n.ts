// Строки интерфейса. Словари — в locales/: en.ts — эталонный (все ключи), остальные
// типизированы по нему, так что непереведённый ключ — ошибка сборки.
// Кроме встроенных en и ru сервер может предложить языки из языковых пакетов
// (ui.locales_dir): их словарь загружается при выборе, непереведённое берётся из en.
// Язык: выбор пользователя (localStorage) → ui.default_language с сервера → английский.
// Текущий язык — реактивный: компоненты, вызывающие t() в шаблоне, перерисуются при смене.
import { reactive, ref } from 'vue'
import en from './locales/en'
import ru from './locales/ru'

export type Key = keyof typeof en
export type Lang = string

// Язык из конфига сервера: код, название на самом языке, локаль дат (у пакетов).
export interface LangInfo { code: string; name: string; pack?: boolean; date_locale?: string }

// Встроенные языки. Название — на самом языке, чтобы его нашёл тот, кто не читает текущий.
const BUILTIN: LangInfo[] = [{ code: 'en', name: 'English' }, { code: 'ru', name: 'Русский' }]

const dicts: Record<string, Partial<Record<Key, string>>> = reactive({ en, ru })
const STORE = 'wm.lang'
const CODE = /^[a-z]{2,3}(-[a-z0-9]{2,8})?$/
export const isLang = (v: unknown): v is Lang => typeof v === 'string' && CODE.test(v)

// Языки, которые предлагает сервер (заполняет setServerLangs из конфига).
const server = ref<LangInfo[]>([])
const info = (l: Lang) => server.value.find((x) => x.code === l) ?? BUILTIN.find((x) => x.code === l)

function stored(): Lang | null {
  try {
    const v = localStorage.getItem(STORE)
    return isLang(v) ? v : null
  } catch { return null }
}

const current = ref<Lang>('en')

// Словарь языкового пакета — с сервера, один раз.
const loading = new Map<Lang, Promise<boolean>>()
function ensureDict(l: Lang): Promise<boolean> {
  if (dicts[l]) return Promise.resolve(true)
  let p = loading.get(l)
  if (!p) {
    p = fetch(new URL(`api/v1/locales/${l}`, document.baseURI), { credentials: 'same-origin' })
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => { if (d && typeof d === 'object') { dicts[l] = d; return true } return false })
      .catch(() => false)
      .finally(() => loading.delete(l))
    loading.set(l, p)
  }
  return p
}

// Язык виден и серверу (cookie wm_lang): на нём он пишет уведомления о прочтении,
// ответы на приглашения и имена правил фильтров.
// Язык пакета включается, когда загружен словарь; до того остаётся прежний.
let want = ''
let pending: Promise<void> = Promise.resolve()
function apply(l: Lang): Promise<void> {
  pending = applyNow(l)
  return pending
}
async function applyNow(l: Lang) {
  want = l
  if (!dicts[l] && !(await ensureDict(l))) return
  if (want !== l) return // пока грузили, выбрали другой
  current.value = l
  document.documentElement.lang = l
  document.cookie = `wm_lang=${l}; path=${new URL(document.baseURI).pathname}; max-age=31536000; samesite=lax`
}
{
  const s = stored()
  // Пакетный язык из прошлого визита — словарь грузится сразу, не дожидаясь конфига.
  void apply(s ?? 'en')
}

export const lang = () => current.value

// Дождаться словаря выбранного языка — чтобы первая отрисовка была уже на нём.
export const langReady = () => pending.catch(() => undefined)

// Явный выбор пользователя — запоминается в браузере.
export function setLang(l: Lang) {
  void apply(l)
  try { localStorage.setItem(STORE, l) } catch { /* приватный режим */ }
}

// Языки установки из конфига сервера. Старый формат — просто коды.
export function setServerLangs(list: unknown) {
  if (!Array.isArray(list)) return
  server.value = list.flatMap((x): LangInfo[] => {
    if (typeof x === 'string') return BUILTIN.filter((b) => b.code === x)
    if (x && typeof x === 'object' && isLang((x as LangInfo).code)) return [x as LangInfo]
    return []
  })
  // Выбранного раньше языка больше нет (пакет удалили) — по умолчанию.
  if (server.value.length && !server.value.some((x) => x.code === current.value) && !server.value.some((x) => x.code === want)) {
    void apply(server.value[0].code)
  }
}

// Язык установки (ui.default_language) — только если пользователь ещё не выбрал сам
// или выбранного им языка больше нет.
export function setDefaultLang(l: unknown) {
  if (!isLang(l) || !availableLangs().includes(l)) return
  const s = stored()
  if (s && availableLangs().includes(s)) return
  void apply(l)
}

// Языки, которые можно выбрать: от сервера, иначе встроенные.
export function availableLangs(): Lang[] {
  return (server.value.length ? server.value : BUILTIN).map((x) => x.code)
}

// Название языка для меню.
export const langName = (l: Lang) => info(l)?.name ?? l

// t('key', { n: 3 }) — подстановка {n} в строку.
export function t(key: Key, params?: Record<string, string | number>): string {
  const s = dicts[current.value]?.[key] ?? en[key]
  return params ? s.replace(/\{(\w+)\}/g, (m, p: string) => (p in params ? String(params[p]) : m)) : s
}

// Локаль для дат и чисел.
export const dateLocale = () => {
  const l = current.value
  if (l === 'ru') return 'ru-RU'
  if (l === 'en') return 'en-GB'
  return info(l)?.date_locale || l
}

// Множественное число: tn('inv.people', 5) → ключ inv.people.one/few/many/other по правилам языка, {n} — число.
export function tn(base: string, n: number, params?: Record<string, string | number>): string {
  const exact = `${base}.${new Intl.PluralRules(current.value).select(n)}`
  const key = (exact in en || exact in (dicts[current.value] ?? {}) ? exact : `${base}.other`) as Key
  return t(key, { n, ...params })
}
