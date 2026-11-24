<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref } from 'vue'
import {
  Archive, CheckCheck, Download, Eraser, FileText, Folder as FolderIcon, FolderPlus, Inbox, Pencil, PenSquare, Send,
  ShieldAlert, Trash2, Upload,
} from 'lucide-vue-next'
import { api, type Folder } from '../api'
import { t } from '../i18n'
import {
  createFolder, deleteFolder, emptyFolder, folderParent, folderTitle, importMail, moveSelected, openCompose, openFolder,
  readFolder, renameFolder, state, visibleFolders,
} from '../store'

const icons: Record<string, unknown> = {
  inbox: Inbox, drafts: FileText, sent: Send, archive: Archive, junk: ShieldAlert, trash: Trash2,
}
const visible = computed(visibleFolders)
// Подпапки спецпапки (Архив/2025) показываются сразу под ней, а не среди обычных.
const underRole = (f: Folder) => state.folders.some((r) => r.role && r.delimiter && f.name.startsWith(r.name + r.delimiter))
const special = computed(() => {
  const roles = visible.value.filter((f) => f.role)
  const out: Folder[] = []
  for (const r of roles) {
    out.push(r)
    if (r.delimiter) out.push(...visible.value.filter((f) => !f.role && f.name.startsWith(r.name + r.delimiter)))
  }
  return out
})
const others = computed(() => visible.value.filter((f) => !f.role && !underRole(f)))
const dropTarget = ref('')

function onDrop(e: DragEvent, name: string) {
  dropTarget.value = ''
  if (e.dataTransfer?.getData('application/x-webmail-uids')) void moveSelected(name)
}

// --- контекстное меню папки -------------------------------------------------------
const menu = ref<{ f: Folder; x: number; y: number } | null>(null)
const menuEl = ref<HTMLElement>()
const hasChildren = (f: Folder) => state.folders.some((x) => x.name.startsWith(f.name + f.delimiter) && f.delimiter)
const isSystem = (f: Folder) => !!f.role || state.folders.some((x) => x.role && f.delimiter && x.name.startsWith(f.name + f.delimiter))

async function openMenu(e: MouseEvent, f: Folder) {
  menu.value = { f, x: e.clientX, y: e.clientY }
  await nextTick()
  const el = menuEl.value
  if (!el) return
  // Не выходить за край окна.
  const r = el.getBoundingClientRect()
  if (r.right > innerWidth) menu.value.x = Math.max(4, innerWidth - r.width - 4)
  if (r.bottom > innerHeight) menu.value.y = Math.max(4, innerHeight - r.height - 4)
  el.querySelector('button')?.focus()
}
function closeMenu() { menu.value = null }
function onDocDown(e: Event) { if (menu.value && !menuEl.value?.contains(e.target as Node)) closeMenu() }
document.addEventListener('mousedown', onDocDown)
onBeforeUnmount(() => document.removeEventListener('mousedown', onDocDown))

function act(fn: (f: Folder) => unknown) {
  const f = menu.value?.f
  closeMenu()
  if (f) void fn(f)
}

async function newFolder(parent: Folder | null) {
  const name = prompt(parent ? `Новая папка внутри «${folderTitle(parent)}»` : 'Новая папка')
  if (name?.trim()) await createFolder(parent?.name ?? '', name)
}
async function rename(f: Folder) {
  const name = prompt('Новое имя папки', f.display)
  if (name?.trim() && name.trim() !== f.display) await renameFolder(f, folderParent(f), name)
}
async function remove(f: Folder) {
  const sub = hasChildren(f) ? ' вместе с вложенными папками' : ''
  const n = f.total ? ` и ${f.total} писем в ней` : ''
  if (confirm(`Удалить папку «${f.display}»${sub}${n}? Это нельзя отменить.`)) await deleteFolder(f)
}
// Загрузка писем: меню закрывается, а папка запоминается до выбора файлов.
const importInput = ref<HTMLInputElement>()
let importTarget: Folder | null = null
function startImport(f: Folder) {
  importTarget = f
  importInput.value?.click()
}
function onImportFiles(e: Event) {
  const input = e.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  input.value = ''
  if (importTarget) void importMail(importTarget, files)
}

async function empty(f: Folder) {
  if (confirm(`Удалить навсегда все письма в папке «${folderTitle(f)}» (${f.total})?`)) await emptyFolder(f)
}
</script>

<template>
  <nav class="folders" :aria-label="t('nav.mail')">
    <div class="head">
      <button class="btn primary compose" @click="openCompose()">
        <PenSquare :size="16" /> {{ t('compose.new') }}
      </button>
    </div>
    <div class="scroll list">
      <template v-for="group in [special, others]" :key="group === special ? 's' : 'o'">
        <div v-if="group === others" class="sep">
          <span>Папки</span>
          <button class="add" title="Новая папка" aria-label="Новая папка" data-test="folder-new" @click="newFolder(null)">
            <FolderPlus :size="15" />
          </button>
        </div>
        <button v-for="f in group" :key="f.name" class="folder"
                :class="{ active: f.name === state.folder, drop: dropTarget === f.name, dim: !f.selectable }"
                :style="{ paddingLeft: `${12 + f.depth * 14}px` }"
                :aria-disabled="!f.selectable" :data-folder="f.name"
                @click="f.selectable && openFolder(f.name)"
                @contextmenu.prevent="openMenu($event, f)"
                @keydown.shift.f10.prevent="openMenu({ clientX: ($event.target as HTMLElement).getBoundingClientRect().left + 40, clientY: ($event.target as HTMLElement).getBoundingClientRect().bottom } as MouseEvent, f)"
                @dragover.prevent="f.selectable && (dropTarget = f.name)"
                @dragleave="dropTarget = ''"
                @drop.prevent="f.selectable && onDrop($event, f.name)">
          <component :is="(f.role && icons[f.role]) || FolderIcon" :size="16" class="ico" />
          <span class="name ellipsis">{{ folderTitle(f) }}</span>
          <span v-if="f.unread && f.role !== 'sent' && f.role !== 'trash'" class="count"
                :class="{ strong: f.role === 'inbox' }">{{ f.unread }}</span>
        </button>
      </template>
    </div>

    <input ref="importInput" type="file" accept=".mbox,.eml,.mbx,application/mbox,message/rfc822" multiple hidden
           data-test="folder-import-input" @change="onImportFiles" />
    <div v-if="menu" ref="menuEl" class="menu" role="menu" :style="{ left: `${menu.x}px`, top: `${menu.y}px` }"
         data-test="folder-menu" @keydown.esc="closeMenu">
      <button v-if="menu.f.delimiter" role="menuitem" @click="act(newFolder)"><FolderPlus :size="15" /> Новая подпапка</button>
      <button v-if="menu.f.selectable" role="menuitem" :disabled="!menu.f.unread" @click="act(readFolder)">
        <CheckCheck :size="15" /> Отметить все прочитанными
      </button>
      <button v-if="menu.f.role === 'trash' || menu.f.role === 'junk'" role="menuitem" :disabled="!menu.f.total" @click="act(empty)">
        <Eraser :size="15" /> Очистить папку
      </button>
      <a v-if="menu.f.selectable" role="menuitem" class="item" :href="api.exportMboxURL(menu.f.name)" download
         data-test="folder-export" @click="closeMenu"><Download :size="15" /> Скачать письма (mbox)</a>
      <button v-if="menu.f.selectable" role="menuitem" data-test="folder-import" @click="act(startImport)">
        <Upload :size="15" /> Загрузить письма (.mbox, .eml)
      </button>
      <template v-if="!isSystem(menu.f)">
        <button role="menuitem" @click="act(rename)"><Pencil :size="15" /> Переименовать</button>
        <button role="menuitem" class="danger" @click="act(remove)"><Trash2 :size="15" /> Удалить папку</button>
      </template>
    </div>
  </nav>
</template>

<style scoped>
.folders { display: flex; flex-direction: column; min-width: 0; background: var(--pane-alt); }
.head { padding: 12px; }
.compose { width: 100%; justify-content: center; height: 34px; }
.list { flex: 1; padding: 0 8px 12px; }
.sep { display: flex; align-items: center; justify-content: space-between; margin: 10px 6px 4px; padding-top: 8px;
  border-top: 1px solid var(--border); font-size: 12px; color: var(--text-3); }
.add { display: inline-flex; border: 0; background: none; color: var(--text-3); padding: 3px; border-radius: 5px; }
.add:hover { background: var(--hover); color: var(--text); }
.folder {
  width: 100%; display: flex; align-items: center; gap: 10px; height: 32px; padding-right: 10px;
  border: 0; border-radius: 6px; background: transparent; text-align: left; color: var(--text);
}
.folder:hover { background: var(--hover); }
.folder.active { background: var(--selected); font-weight: 600; }
.folder.drop { outline: 2px dashed var(--accent); }
.folder.dim { color: var(--text-3); cursor: default; }
.ico { flex: none; color: var(--text-2); }
.folder.active .ico { color: var(--accent); }
.name { flex: 1; }
.count { font-size: 12px; color: var(--text-2); }
.count.strong { color: var(--accent); font-weight: 700; }
.menu { position: fixed; z-index: 50; min-width: 220px; padding: 4px; background: var(--pane);
  border: 1px solid var(--border); border-radius: 8px; box-shadow: var(--shadow); display: grid; }
.menu button, .menu a.item { display: flex; text-decoration: none; align-items: center; gap: 8px; height: 32px; padding: 0 10px; border: 0; border-radius: 6px;
  background: none; text-align: left; color: var(--text); }
.menu button:hover:not(:disabled), .menu button:focus-visible, .menu a.item:hover, .menu a.item:focus-visible { background: var(--hover); outline: none; }
.menu button:disabled { color: var(--text-3); }
.menu .danger { color: var(--danger); }
</style>
