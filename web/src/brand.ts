// Оформление из ui.brand: название, логотипы, favicon и цвет акцента.
// Из accent считаются оттенки под текущую тему (фон выделения, текст на кнопке),
// css_vars из конфига применяются поверх. Последний бренд хранится в localStorage,
// чтобы при следующем открытии страница сразу была в нужных цветах.
import type { Brand } from './api'

type RGB = [number, number, number]

const parse = (hex: string): RGB | null => {
  const m = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(hex)
  if (!m) return null
  const h = m[1].length === 3 ? [...m[1]].map((c) => c + c).join('') : m[1]
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16)) as RGB
}
const hex = (c: RGB) => '#' + c.map((x) => Math.round(x).toString(16).padStart(2, '0')).join('')
// mix(a, b, w) — доля w цвета a, остальное — b.
const mix = (a: RGB, b: RGB, w: number): RGB => [0, 1, 2].map((i) => a[i] * w + b[i] * (1 - w)) as RGB
const luminance = (c: RGB) => {
  const [r, g, b] = c.map((x) => { const v = x / 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4 })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
// Белый или тёмный текст на фоне акцента — что контрастнее.
const contrastText = (c: RGB) => (1.05 / (luminance(c) + 0.05) >= (luminance(c) + 0.05) / 0.05 ? '#ffffff' : '#111418')

// Фоны панелей из styles.css: от них считаются мягкие оттенки акцента.
const PANE = { light: parse('#ffffff')!, dark: parse('#1c2027')! }

export function accentVars(b: Brand, dark: boolean): Record<string, string> {
  const base = parse(b.accent ?? '')
  if (!base) return {}
  // Без accent_dark — светлее основного, чтобы читался на тёмном фоне.
  const a = (dark && parse(b.accent_dark ?? '')) || (dark ? mix(base, [255, 255, 255], 0.75) : base)
  const pane = dark ? PANE.dark : PANE.light
  return {
    '--accent': hex(a),
    '--accent-contrast': contrastText(a),
    '--accent-soft': hex(mix(a, pane, dark ? 0.16 : 0.12)),
    '--selected': hex(mix(a, pane, dark ? 0.24 : 0.16)),
  }
}

let current: Brand | null = null
let applied: string[] = []

const isDark = () => document.documentElement.dataset.theme === 'dark'

export const brandLogo = (b: Brand | null | undefined, dark: boolean) =>
  (dark && b?.logo_dark_url) || b?.logo_url || ''

// Компактная метка в шапке: favicon, иначе тот же логотип, что на входе.
export const brandIcon = (b: Brand | null | undefined, dark: boolean) =>
  b?.favicon_url || brandLogo(b, dark)

function setFavicon(href: string) {
  let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!href) { link?.remove(); return }
  if (!link) {
    link = document.createElement('link')
    link.rel = 'icon'
    document.head.appendChild(link)
  }
  if (link.getAttribute('href') !== href) link.href = href
}

// applyBrand(b) запоминает бренд и применяет его; applyBrand() — повторно под новую тему.
export function applyBrand(b?: Brand) {
  if (b) {
    current = b
    try { localStorage.setItem('wm.brand', JSON.stringify(b)) } catch { /* приватный режим */ }
  }
  if (!current) return
  const style = document.documentElement.style
  for (const k of applied) style.removeProperty(k)
  const vars = { ...accentVars(current, isDark()), ...current.css_vars }
  applied = []
  for (const [k, v] of Object.entries(vars)) {
    if (/^--[a-z0-9-]+$/.test(k)) { style.setProperty(k, v); applied.push(k) }
  }
  setFavicon(current.favicon_url ?? '')
}

// Бренд прошлого визита — до ответа сервера.
export function restoreBrand(): Brand | null {
  try {
    const b = JSON.parse(localStorage.getItem('wm.brand') ?? 'null') as Brand | null
    if (b && typeof b.name === 'string') { applyBrand(b); return b }
  } catch { /* битое значение — ждём сервер */ }
  return null
}
