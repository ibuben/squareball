<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref } from 'vue'
import { EditorContent, useEditor } from '@tiptap/vue-3'
import StarterKit from '@tiptap/starter-kit'
import { Placeholder } from '@tiptap/extensions'
import Image from '@tiptap/extension-image'
import {
  Bold, Check, ImagePlus, Italic, Link as LinkIcon, List, ListOrdered, Quote, Redo2, RemoveFormatting, SpellCheck, Strikethrough,
  Underline, Undo2,
} from 'lucide-vue-next'
import {
  addToDictionary, dictionaryName, ignoreWord, refreshSpell, Spell, spellDictionaries, spellEngine, spellOn, type WordAt,
} from '../spell'
import { api } from '../api'
import { reportError, state } from '../store'

// Редактор текста письма. Цитата исходного письма в него не попадает:
// схема TipTap выбросила бы таблицы и стили рассылок (см. ComposeWindow).
const props = defineProps<{ content: string; placeholder?: string }>()
const emit = defineEmits<{ change: []; files: [files: File[]]; submit: [] }>()

// Картинки (PNG, JPEG, GIF, WebP) встают в текст, остальные файлы — во вложения.
const INLINE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp']
const MAX_SIDE = 2000 // крупные фото уменьшаем: в письме они всё равно показываются в ширину окна

function readDataURL(f: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => resolve(r.result as string)
    r.onerror = () => reject(r.error)
    r.readAsDataURL(f)
  })
}

async function toDataURL(f: File): Promise<string> {
  const url = await readDataURL(f)
  if (f.type === 'image/gif') return url // анимацию не трогаем
  const img = new window.Image()
  img.src = url
  try { await img.decode() } catch { return url }
  const k = Math.min(1, MAX_SIDE / Math.max(img.naturalWidth, img.naturalHeight))
  if (k >= 1) return url
  const c = document.createElement('canvas')
  c.width = Math.round(img.naturalWidth * k)
  c.height = Math.round(img.naturalHeight * k)
  c.getContext('2d')!.drawImage(img, 0, 0, c.width, c.height)
  return f.type === 'image/png' ? c.toDataURL('image/png') : c.toDataURL('image/jpeg', 0.88)
}

async function insertImages(files: File[], pos?: number) {
  const e = editor.value
  if (!e) return
  for (const f of files) {
    const src = await toDataURL(f)
    const chain = e.chain().focus()
    if (pos !== undefined) chain.insertContentAt(pos, { type: 'image', attrs: { src, alt: f.name } })
    else chain.setImage({ src, alt: f.name })
    chain.run()
  }
}

function takeFiles(list?: FileList | null, pos?: number): boolean {
  const files = Array.from(list ?? [])
  if (!files.length) return false
  const images = files.filter((f) => INLINE_TYPES.includes(f.type))
  const others = files.filter((f) => !INLINE_TYPES.includes(f.type))
  if (others.length) emit('files', others)
  if (images.length) void insertImages(images, pos)
  return true
}

const picker = ref<HTMLInputElement>()
function pickImages(e: Event) {
  const input = e.target as HTMLInputElement
  takeFiles(input.files)
  input.value = ''
}

const editor = useEditor({
  content: props.content || '<p></p>',
  extensions: [
    StarterKit.configure({
      heading: { levels: [2, 3] },
      codeBlock: false,
      link: { openOnClick: false, autolink: true, defaultProtocol: 'https', protocols: ['mailto'] },
    }),
    Placeholder.configure({ placeholder: props.placeholder ?? '' }),
    Image.configure({ allowBase64: true, HTMLAttributes: { style: 'max-width:100%;height:auto' } }),
    Spell.configure({ onMenu: openSpellMenu }),
  ],
  editorProps: {
    // Браузерная проверка — только при engine=browser, иначе подчёркивания задвоятся.
    attributes: () => ({ class: 'rich', spellcheck: spellEngine() === 'browser' && spellOn() ? 'true' : 'false' }),
    // Файлы, брошенные или вставленные в текст, становятся вложениями.
    handleDrop: (view, event) => {
      const ev = event as DragEvent
      const pos = view.posAtCoords({ left: ev.clientX, top: ev.clientY })?.pos
      return takeFiles(ev.dataTransfer?.files, pos)
    },
    handlePaste: (_view, event) => takeFiles(event.clipboardData?.files),
    handleKeyDown: (_view, event) => {
      if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) { emit('submit'); return true }
      return false
    },
  },
  onUpdate: () => emit('change'),
})

onBeforeUnmount(() => {
  editor.value?.destroy()
  document.removeEventListener('mousedown', onDocDown)
})

// --- орфография: меню исправлений и выбор словаря --------------------------------
const spellMenu = ref<{ x: number; y: number; w: WordAt & { suggestions: string[] } } | null>(null)
const spellMenuEl = ref<HTMLElement>()
async function openSpellMenu(e: MouseEvent, w: WordAt & { suggestions: string[] }) {
  langMenu.value = false
  spellMenu.value = { x: e.clientX, y: e.clientY, w }
  await nextTick()
  const el = spellMenuEl.value
  if (!el || !spellMenu.value) return
  const r = el.getBoundingClientRect()
  if (r.right > innerWidth) spellMenu.value.x = Math.max(4, innerWidth - r.width - 4)
  if (r.bottom > innerHeight) spellMenu.value.y = Math.max(4, innerHeight - r.height - 4)
  el.querySelector('button')?.focus()
}
function closeMenus() { spellMenu.value = null; langMenu.value = false }
function onDocDown(e: Event) {
  const t = e.target as Node
  if (spellMenuEl.value?.contains(t) || langEl.value?.contains(t)) return
  closeMenus()
}
document.addEventListener('mousedown', onDocDown)

function replaceWord(s: string) {
  const m = spellMenu.value
  const e = editor.value
  closeMenus()
  if (!m || !e) return
  // Слово могло измениться, пока было открыто меню.
  if (e.state.doc.textBetween(m.w.from, m.w.to) !== m.w.word) return
  e.chain().focus().command(({ tr }) => { tr.insertText(s, m.w.from, m.w.to); return true }).run()
}
function spellAction(fn: (w: string) => unknown) {
  const m = spellMenu.value
  closeMenus()
  if (m) void fn(m.w.word)
  editor.value?.commands.focus()
}

const langMenu = ref(false)
const langEl = ref<HTMLElement>()
const engine = spellEngine()
async function setSpell(v: string) {
  closeMenus()
  const prev = state.prefs.spell
  state.prefs.spell = v
  refreshSpell()
  if (spellEngine() === 'browser') editor.value?.view.dispatch(editor.value.state.tr) // обновить атрибут spellcheck
  try { state.prefs = (await api.setPrefs({ spell: v })).prefs } catch (err) { state.prefs.spell = prev; refreshSpell(); reportError(err) }
}

function setLink() {
  const e = editor.value
  if (!e) return
  const prev = e.getAttributes('link').href as string | undefined
  const url = prompt('URL', prev ?? 'https://')
  if (url === null) return
  if (url === '') { e.chain().focus().extendMarkRange('link').unsetLink().run(); return }
  if (!/^(https?:|mailto:)/i.test(url)) return
  e.chain().focus().extendMarkRange('link').setLink({ href: url }).run()
}

type Btn = { icon: unknown; title: string; run: () => void; active?: () => boolean }
const buttons = (): Btn[] => {
  const e = editor.value!
  return [
    { icon: Bold, title: 'Жирный (Ctrl+B)', run: () => e.chain().focus().toggleBold().run(), active: () => e.isActive('bold') },
    { icon: Italic, title: 'Курсив (Ctrl+I)', run: () => e.chain().focus().toggleItalic().run(), active: () => e.isActive('italic') },
    { icon: Underline, title: 'Подчёркнутый (Ctrl+U)', run: () => e.chain().focus().toggleUnderline().run(), active: () => e.isActive('underline') },
    { icon: Strikethrough, title: 'Зачёркнутый', run: () => e.chain().focus().toggleStrike().run(), active: () => e.isActive('strike') },
    { icon: List, title: 'Список', run: () => e.chain().focus().toggleBulletList().run(), active: () => e.isActive('bulletList') },
    { icon: ListOrdered, title: 'Нумерованный список', run: () => e.chain().focus().toggleOrderedList().run(), active: () => e.isActive('orderedList') },
    { icon: Quote, title: 'Цитата', run: () => e.chain().focus().toggleBlockquote().run(), active: () => e.isActive('blockquote') },
    { icon: LinkIcon, title: 'Ссылка', run: setLink, active: () => e.isActive('link') },
    { icon: ImagePlus, title: 'Картинка в текст', run: () => picker.value?.click() },
    { icon: RemoveFormatting, title: 'Очистить форматирование', run: () => e.chain().focus().unsetAllMarks().clearNodes().run() },
    { icon: Undo2, title: 'Отменить (Ctrl+Z)', run: () => e.chain().focus().undo().run() },
    { icon: Redo2, title: 'Повторить (Ctrl+Shift+Z)', run: () => e.chain().focus().redo().run() },
  ]
}

defineExpose({
  getHTML: () => editor.value?.getHTML() ?? '',
  isEmpty: () => editor.value?.isEmpty ?? true,
  focusStart: () => editor.value?.commands.focus('start'),
  focusEnd: () => editor.value?.commands.focus('end'),
  insertHTML: (html: string) => editor.value?.chain().focus().insertContent(html).run(),
})
</script>

<template>
  <div class="tools" v-if="editor">
    <button v-for="b in buttons()" :key="b.title" type="button" class="icon-btn"
            :class="{ active: b.active?.() }" :title="b.title" :aria-label="b.title"
            @mousedown.prevent @click="b.run">
      <component :is="b.icon" :size="15" />
    </button>
    <div v-if="engine !== 'off'" ref="langEl" class="lang">
      <button type="button" class="icon-btn" :class="{ off: !spellOn() }" title="Проверка орфографии"
              aria-label="Проверка орфографии" aria-haspopup="menu" :aria-expanded="langMenu" data-test="spell-lang"
              @mousedown.prevent @click="langMenu = !langMenu; spellMenu = null">
        <SpellCheck :size="15" />
      </button>
      <div v-if="langMenu" class="menu down" role="menu" data-test="spell-lang-menu" @keydown.esc="closeMenus">
        <template v-if="engine === 'hunspell'">
          <button v-if="spellDictionaries().length > 1" type="button" role="menuitemradio"
                  :aria-checked="state.prefs.spell === '' || (state.prefs.spell !== 'off' && !spellDictionaries().includes(state.prefs.spell))"
                  @click="setSpell('')">
            <Check :size="14" class="tick" />Все словари
          </button>
          <button v-for="d in spellDictionaries()" :key="d" type="button" role="menuitemradio" :data-dict="d"
                  :aria-checked="state.prefs.spell === d || (spellDictionaries().length === 1 && state.prefs.spell === '')"
                  @click="setSpell(d)">
            <Check :size="14" class="tick" />{{ dictionaryName(d) }}
          </button>
        </template>
        <button v-else type="button" role="menuitemradio" :aria-checked="state.prefs.spell !== 'off'" @click="setSpell('')">
          <Check :size="14" class="tick" />Проверять орфографию
        </button>
        <button type="button" role="menuitemradio" :aria-checked="state.prefs.spell === 'off'" @click="setSpell('off')">
          <Check :size="14" class="tick" />Не проверять
        </button>
      </div>
    </div>
  </div>
  <div v-if="spellMenu" ref="spellMenuEl" class="menu" role="menu" :style="{ left: `${spellMenu.x}px`, top: `${spellMenu.y}px` }"
       data-test="spell-menu" @keydown.esc="closeMenus(); editor?.commands.focus()">
    <button v-for="s in spellMenu.w.suggestions" :key="s" type="button" role="menuitem" class="sugg" @click="replaceWord(s)">{{ s }}</button>
    <div v-if="!spellMenu.w.suggestions.length" class="none">Нет вариантов</div>
    <hr />
    <button type="button" role="menuitem" data-test="spell-add" @click="spellAction(addToDictionary)">Добавить в словарь</button>
    <button type="button" role="menuitem" @click="spellAction(ignoreWord)">Пропустить</button>
  </div>
  <input ref="picker" type="file" accept="image/png,image/jpeg,image/gif,image/webp" multiple hidden
         data-test="inline-image" @change="pickImages" />
  <EditorContent :editor="editor" class="editor-host scroll" />
</template>

<style scoped>
.tools { display: flex; flex-wrap: wrap; gap: 2px; padding: 4px 8px; border-bottom: 1px solid var(--border); }
.tools .icon-btn.active { background: var(--accent-soft); color: var(--accent); }
.editor-host { flex: 1; min-height: 120px; cursor: text; }
.editor-host :deep(.rich) { min-height: 100%; padding: 12px 14px; outline: none; font-size: 14px; line-height: 1.55; }
.editor-host :deep(.rich p) { margin: 0 0 8px; }
.editor-host :deep(.rich blockquote) { margin: 8px 0; padding-left: 12px; border-left: 3px solid var(--border); color: var(--text-2); }
.editor-host :deep(.rich a) { color: var(--accent); }
.editor-host :deep(.rich img) { max-width: 100%; height: auto; border-radius: 4px; }
.editor-host :deep(.rich img.ProseMirror-selectednode) { outline: 2px solid var(--accent); }
.editor-host :deep(.spell-error) {
  text-decoration: underline wavy var(--danger); text-decoration-skip-ink: none; text-underline-offset: 3px;
}
.lang { position: relative; margin-left: auto; }
.tools .icon-btn.off { color: var(--text-3); }
.menu { position: fixed; z-index: 60; min-width: 180px; padding: 4px; background: var(--pane);
  border: 1px solid var(--border); border-radius: 8px; box-shadow: var(--shadow); display: grid; }
.menu.down { position: absolute; right: 0; top: calc(100% + 4px); }
.menu button { display: flex; align-items: center; gap: 8px; height: 30px; padding: 0 10px; border: 0; border-radius: 6px;
  background: none; text-align: left; color: var(--text); white-space: nowrap; }
.menu button:hover, .menu button:focus-visible { background: var(--hover); outline: none; }
.menu .sugg { font-weight: 600; }
.menu .tick { visibility: hidden; color: var(--accent); }
.menu [aria-checked="true"] .tick { visibility: visible; }
.menu hr { border: 0; border-top: 1px solid var(--border); margin: 4px 0; }
.menu .none { padding: 6px 10px; color: var(--text-3); font-size: 13px; }
.editor-host :deep(.rich p.is-editor-empty:first-child::before) {
  content: attr(data-placeholder); float: left; height: 0; pointer-events: none; color: var(--text-3);
}
</style>
