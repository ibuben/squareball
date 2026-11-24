<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Pencil, Trash2 } from 'lucide-vue-next'
import type { Folder } from '../../api'
import {
  createFolder, deleteFolder, folderParent, folderTitle, loadFolders, renameFolder, state, subscribeFolder,
} from '../../store'

onMounted(loadFolders)

const hasDelim = computed(() => state.folders.some((f) => f.delimiter))
const isSystem = (f: Folder) => !!f.role || state.folders.some((x) => x.role && f.delimiter && x.name.startsWith(f.name + f.delimiter))
const title = (f: Folder) => folderTitle(f)

// Куда можно положить папку: любая, кроме неё самой и её подпапок.
function parents(except?: Folder): Folder[] {
  return state.folders.filter((f) => !except || (f.name !== except.name && !f.name.startsWith(except.name + except.delimiter)))
}
const path = (f: Folder) => f.name.split(f.delimiter || '\u0000').join(' / ')

const newName = ref('')
const newParent = ref('')
const busy = ref(false)
async function create() {
  if (!newName.value.trim()) return
  busy.value = true
  if (await createFolder(newParent.value, newName.value)) newName.value = ''
  busy.value = false
}

const editing = ref('')
const editName = ref('')
const editParent = ref('')
function edit(f: Folder) {
  editing.value = f.name
  editName.value = f.display
  editParent.value = folderParent(f)
}
async function save(f: Folder) {
  if (!editName.value.trim()) return
  busy.value = true
  if (editName.value.trim() === f.display && editParent.value === folderParent(f)) editing.value = ''
  else if (await renameFolder(f, editParent.value, editName.value)) editing.value = ''
  busy.value = false
}
async function remove(f: Folder) {
  const sub = state.folders.some((x) => f.delimiter && x.name.startsWith(f.name + f.delimiter)) ? ' вместе с вложенными папками' : ''
  if (confirm(`Удалить папку «${f.display}»${sub} и все письма в ней? Это нельзя отменить.`)) await deleteFolder(f)
}
</script>

<template>
  <section>
    <h2>Папки</h2>
    <p class="faint">Отписанные папки остаются на сервере, но не показываются в списке слева.
      Системные папки (Входящие, Отправленные, Корзина и другие) переименовать и удалить нельзя.</p>

    <form class="card new" @submit.prevent="create">
      <label>Новая папка
        <input v-model="newName" class="input" maxlength="128" placeholder="Название" data-test="folder-new-name" />
      </label>
      <label v-if="hasDelim">Внутри
        <select v-model="newParent" class="input">
          <option value="">— верхний уровень —</option>
          <option v-for="f in state.folders" :key="f.name" :value="f.name">{{ path(f) }}</option>
        </select>
      </label>
      <div class="actions"><button class="btn primary" :disabled="busy || !newName.trim()">Создать</button></div>
    </form>

    <div class="card list">
      <div v-for="f in state.folders" :key="f.name" class="row" :data-folder="f.name">
        <template v-if="editing === f.name">
          <form class="edit" @submit.prevent="save(f)">
            <input v-model="editName" class="input" maxlength="128" aria-label="Имя папки" @keydown.esc="editing = ''" />
            <select v-if="hasDelim" v-model="editParent" class="input" aria-label="Расположение">
              <option value="">— верхний уровень —</option>
              <option v-for="p in parents(f)" :key="p.name" :value="p.name">{{ path(p) }}</option>
            </select>
            <button class="btn primary" :disabled="busy || !editName.trim()">Сохранить</button>
            <button type="button" class="btn" @click="editing = ''">Отмена</button>
          </form>
        </template>
        <template v-else>
          <label class="check sub" :style="{ paddingLeft: `${f.depth * 18}px` }"
                 :title="f.role ? 'Системная папка видна всегда' : 'Показывать в списке папок'">
            <input type="checkbox" :checked="f.subscribed || !!f.role" :disabled="!!f.role"
                   @change="subscribeFolder(f, ($event.target as HTMLInputElement).checked)" />
            <span class="ellipsis" :class="{ faint: !f.selectable }">{{ title(f) }}</span>
          </label>
          <span class="faint count">{{ f.selectable ? f.total : '' }}</span>
          <span class="tools">
            <template v-if="!isSystem(f)">
              <button class="icon" title="Переименовать или перенести" aria-label="Переименовать" @click="edit(f)"><Pencil :size="15" /></button>
              <button class="icon danger" title="Удалить" aria-label="Удалить" @click="remove(f)"><Trash2 :size="15" /></button>
            </template>
          </span>
        </template>
      </div>
    </div>
  </section>
</template>

<style scoped>
.card.new { grid-template-columns: 1fr 1fr auto; align-items: end; }
.card.list { gap: 0 !important; padding: 4px 14px !important; }
.row { display: flex; align-items: center; gap: 10px; min-height: 40px; border-bottom: 1px solid var(--border); }
.row:last-child { border-bottom: 0; }
.sub { flex: 1; min-width: 0; display: flex !important; margin: 0; padding-top: 0; padding-bottom: 0; }
.count { font-size: 12px; min-width: 40px; text-align: right; }
.tools { display: flex; gap: 2px; min-width: 60px; justify-content: flex-end; }
.icon { display: inline-flex; border: 0; background: none; padding: 6px; border-radius: 6px; color: var(--text-2); }
.icon:hover { background: var(--hover); color: var(--text); }
.icon.danger:hover { color: var(--danger); }
.edit { flex: 1; display: flex; gap: 8px; flex-wrap: wrap; padding: 6px 0; }
.edit input { flex: 1; min-width: 160px; }
.edit select { flex: 1; min-width: 160px; }
@media (max-width: 760px) { .card.new { grid-template-columns: 1fr; } }
</style>
