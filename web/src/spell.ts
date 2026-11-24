// Проверка орфографии в редакторе письма.
//
// engine=browser — только атрибут spellcheck, всё делает браузер.
// engine=hunspell — слова уходят на сервер пачками, ответы кэшируются на всю
// сессию; ошибки подчёркиваются декорациями ProseMirror, правый клик по слову
// открывает варианты исправления.
import { Extension } from '@tiptap/core'
import type { Node as PMNode } from '@tiptap/pm/model'
import { Plugin, PluginKey, type EditorState } from '@tiptap/pm/state'
import { Decoration, DecorationSet, type EditorView } from '@tiptap/pm/view'
import { api } from './api'
import { reportError, state } from './store'
import { lang } from './i18n'

export type SpellEngine = 'browser' | 'hunspell' | 'off'

export function spellEngine(): SpellEngine {
  return state.me?.features.spellcheck?.engine ?? 'browser'
}
export function spellDictionaries(): string[] {
  return state.me?.features.spellcheck?.dictionaries ?? []
}
/** Включена ли проверка с учётом настройки ящика. */
export function spellOn(): boolean {
  return spellEngine() !== 'off' && state.prefs.spell !== 'off'
}
/** Словарь для запроса: '' — все сразу. */
export function spellDictionary(): string {
  const v = state.prefs.spell
  return v && v !== 'off' && spellDictionaries().includes(v) ? v : ''
}

const displayNames = new Map<string, Intl.DisplayNames | null>()
export function dictionaryName(d: string): string {
  const l = lang()
  if (!displayNames.has(l)) {
    try { displayNames.set(l, new Intl.DisplayNames([l], { type: 'language' })) } catch { displayNames.set(l, null) }
  }
  const names = displayNames.get(l)
  let n = ''
  try { n = names?.of(d.replace(/_/g, '-').split('-').slice(0, 2).join('-')) ?? '' } catch { /* нестандартное имя */ }
  return n ? n[0].toUpperCase() + n.slice(1) : d
}

// --- кэш ответов сервера ---------------------------------------------------------

const cache = new Map<string, string[] | null>() // dict \0 word → варианты (ошибка) или null (верно)
const queued = new Map<string, Set<string>>() // dict → слова на проверку
const inflight = new Set<string>()
const ignored = new Set<string>() // «Пропустить» — до конца сессии
let personal: Set<string> | null = null // личный словарь, в нижнем регистре
const listeners = new Set<() => void>()
let timer = 0
let failed = false

const key = (dict: string, w: string) => dict + '\u0000' + w

/** undefined — ещё не известно, null — верно, массив — ошибка с вариантами. */
export function lookup(dict: string, word: string): string[] | null | undefined {
  const lw = word.toLowerCase()
  if (ignored.has(lw) || personal?.has(lw)) return null
  return cache.get(key(dict, word))
}

function notify() { for (const f of listeners) f() }

function want(dict: string, words: Iterable<string>) {
  if (failed) return
  let set = queued.get(dict)
  for (const w of words) {
    const k = key(dict, w)
    if (cache.has(k) || inflight.has(k)) continue
    if (!set) queued.set(dict, (set = new Set()))
    set.add(w)
  }
  if (set?.size && !timer) timer = window.setTimeout(flush, 350)
}

async function flush() {
  timer = 0
  if (!personal) await loadPersonal()
  for (const [dict, set] of queued) {
    queued.delete(dict)
    const words = [...set]
    for (let i = 0; i < words.length; i += 500) {
      const part = words.slice(i, i + 500)
      part.forEach((w) => inflight.add(key(dict, w)))
      try {
        const r = await api.spell(dict, part)
        for (const w of part) cache.set(key(dict, w), r.misspelled[w] ?? null)
      } catch (e) {
        // Сервер не может проверить — молча выключаемся до перезагрузки страницы,
        // чтобы не сыпать ошибками на каждое слово.
        failed = true
        console.warn('spellcheck', e)
      } finally {
        part.forEach((w) => inflight.delete(key(dict, w)))
      }
    }
  }
  notify()
}

async function loadPersonal() {
  try { personal = new Set((await api.spellWords()).words.map((w) => w.toLowerCase())) } catch { personal = new Set() }
}

export function ignoreWord(word: string) {
  ignored.add(word.toLowerCase())
  notify()
}

export async function addToDictionary(word: string) {
  try {
    const r = await api.addSpellWord(word)
    personal = new Set(r.words.map((w) => w.toLowerCase()))
    notify()
  } catch (e) { reportError(e) }
}

/** Личный словарь изменён в настройках. */
export function setPersonal(words: string[]) {
  personal = new Set(words.map((w) => w.toLowerCase()))
  notify()
}

// --- слова в документе -----------------------------------------------------------

export interface WordAt { from: number; to: number; word: string }

const WORD = /[\p{L}\p{M}]+(?:['’ʼʻ‘-][\p{L}\p{M}]+)*/gu
// Адреса, ссылки, коды и числа не проверяем целиком.
const SKIP_TOKEN = /[@/\\_#\d=]|www\.|\p{L}\.\p{L}/u

function checkable(w: string): boolean {
  if ([...w].length < 2) return false
  // Аббревиатуры (НДС, IMAP) — пропускаем.
  return w !== w.toUpperCase() || w === w.toLowerCase()
}

/** Все проверяемые слова документа с позициями. */
export function wordsIn(doc: PMNode): WordAt[] {
  const out: WordAt[] = []
  doc.descendants((node, pos) => {
    if (!node.isTextblock) return true
    if (node.type.name === 'codeBlock') return false
    // Текст блока: у текста по символу на позицию, у прочих inline-узлов — один символ.
    let text = ''
    node.forEach((child) => { text += child.isText ? child.text! : '￼' })
    const base = pos + 1
    const tokens = /\S+/g
    let t: RegExpExecArray | null
    while ((t = tokens.exec(text))) {
      if (SKIP_TOKEN.test(t[0])) continue
      for (const m of t[0].matchAll(WORD)) {
        if (!checkable(m[0])) continue
        // JS считает символы вне BMP за два, как и ProseMirror, — позиции совпадают.
        const from = base + t.index + m.index!
        out.push({ from, to: from + m[0].length, word: m[0] })
      }
    }
    return false
  })
  return out
}

// --- плагин подсветки ------------------------------------------------------------

interface SpellState { deco: DecorationSet; typing: number | null }
export const spellKey = new PluginKey<SpellState>('spell')

function build(s: EditorState, typing: number | null): DecorationSet {
  if (spellEngine() !== 'hunspell' || !spellOn()) return DecorationSet.empty
  const dict = spellDictionary()
  const words = wordsIn(s.doc)
  const unknown: string[] = []
  const deco: Decoration[] = []
  for (const w of words) {
    // Слово, которое сейчас набирается, не подчёркиваем и не проверяем.
    if (typing !== null && typing >= w.from && typing <= w.to) continue
    const r = lookup(dict, w.word)
    if (r === undefined) unknown.push(w.word)
    else if (r) deco.push(Decoration.inline(w.from, w.to, { class: 'spell-error', 'data-test': 'spell-error' }))
  }
  if (unknown.length) want(dict, unknown)
  return DecorationSet.create(s.doc, deco)
}

/** Слово под позицией, если оно ошибочное. */
export function errorAt(view: EditorView, pos: number): (WordAt & { suggestions: string[] }) | null {
  const $p = view.state.doc.resolve(pos)
  const block = $p.parent
  if (!block.isTextblock) return null
  const start = $p.start()
  for (const w of wordsIn(view.state.doc)) {
    if (w.from < start || w.from > start + block.content.size) continue
    if (pos >= w.from && pos <= w.to) {
      const r = lookup(spellDictionary(), w.word)
      return r ? { ...w, suggestions: r } : null
    }
  }
  return null
}

export const Spell = Extension.create<{ onMenu: (e: MouseEvent, w: WordAt & { suggestions: string[] }) => void }>({
  name: 'spell',
  addOptions() { return { onMenu: () => {} } },
  addProseMirrorPlugins() {
    const onMenu = () => this.options.onMenu
    return [new Plugin<SpellState>({
      key: spellKey,
      state: {
        init: (_, s) => ({ deco: build(s, null), typing: null }),
        apply(tr, prev, _old, s) {
          let typing = prev.typing
          if (tr.docChanged) typing = s.selection.empty ? s.selection.head : null
          else if (tr.selectionSet) typing = null
          // Документ обычного письма невелик: пересчитываем целиком.
          if (tr.docChanged || tr.selectionSet || tr.getMeta(spellKey)) return { deco: build(s, typing), typing }
          return prev
        },
      },
      props: {
        decorations: (s) => spellKey.getState(s)?.deco,
        handleDOMEvents: {
          contextmenu: (view, e) => {
            if (spellEngine() !== 'hunspell' || !spellOn()) return false
            const at = view.posAtCoords({ left: e.clientX, top: e.clientY })
            const w = at && errorAt(view, at.pos)
            if (!w) return false
            e.preventDefault()
            onMenu()(e, w)
            return true
          },
        },
      },
      view(view) {
        // Ответ сервера или смена словаря — перерисовать подчёркивания.
        const refresh = () => { if (!view.isDestroyed) view.dispatch(view.state.tr.setMeta(spellKey, true)) }
        listeners.add(refresh)
        return { destroy: () => listeners.delete(refresh) }
      },
    })]
  },
})

/** Пересчитать подчёркивания во всех открытых редакторах (сменили словарь). */
export function refreshSpell() { notify() }
