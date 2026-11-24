<script setup lang="ts">
import { onBeforeUnmount } from 'vue'
import { EditorContent, useEditor } from '@tiptap/vue-3'
import StarterKit from '@tiptap/starter-kit'
import { Placeholder } from '@tiptap/extensions'
import {
  Bold, Italic, Link as LinkIcon, List, ListOrdered, Quote, Redo2, RemoveFormatting, Strikethrough, Underline, Undo2,
} from 'lucide-vue-next'

// Редактор текста письма. Цитата исходного письма в него не попадает:
// схема TipTap выбросила бы таблицы и стили рассылок (см. ComposeWindow).
const props = defineProps<{ content: string; placeholder?: string }>()
const emit = defineEmits<{ change: []; files: [files: File[]]; submit: [] }>()

function takeFiles(list?: FileList | null): boolean {
  const files = Array.from(list ?? [])
  if (!files.length) return false
  emit('files', files)
  return true
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
  ],
  editorProps: {
    attributes: { class: 'rich', spellcheck: 'true' },
    // Файлы, брошенные или вставленные в текст, становятся вложениями.
    handleDrop: (_view, event) => takeFiles((event as DragEvent).dataTransfer?.files),
    handlePaste: (_view, event) => takeFiles(event.clipboardData?.files),
    handleKeyDown: (_view, event) => {
      if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) { emit('submit'); return true }
      return false
    },
  },
  onUpdate: () => emit('change'),
})

onBeforeUnmount(() => editor.value?.destroy())

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
})
</script>

<template>
  <div class="tools" v-if="editor">
    <button v-for="b in buttons()" :key="b.title" type="button" class="icon-btn"
            :class="{ active: b.active?.() }" :title="b.title" :aria-label="b.title"
            @mousedown.prevent @click="b.run">
      <component :is="b.icon" :size="15" />
    </button>
  </div>
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
.editor-host :deep(.rich p.is-editor-empty:first-child::before) {
  content: attr(data-placeholder); float: left; height: 0; pointer-events: none; color: var(--text-3);
}
</style>
