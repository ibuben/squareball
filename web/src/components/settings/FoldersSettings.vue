<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Pencil, Trash2 } from 'lucide-vue-next'
import { api, type Folder, type Prefs } from '../../api'
import {
  createFolder, deleteFolder, folderParent, folderTitle, loadFolders, renameFolder, reportError, roleFolder, state,
  subscribeFolder, toast,
} from '../../store'
import { t } from '../../i18n'

const archive = ref<Prefs['archive_structure'] | null>(null)
onMounted(async () => {
  void loadFolders()
  try { archive.value = (await api.prefs()).prefs.archive_structure } catch (e) { reportError(e) }
})
async function setArchive(v: Prefs['archive_structure']) {
  try {
    archive.value = (await api.setPrefs({ archive_structure: v })).prefs.archive_structure
    toast(t('toast.saved'))
  } catch (e) { reportError(e) }
}

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
  const sub = state.folders.some((x) => f.delimiter && x.name.startsWith(f.name + f.delimiter)) ? t('folder.withSub') : ''
  if (confirm(t('folder.confirmDeleteAll', { name: f.display, sub }))) await deleteFolder(f)
}
</script>

<template>
  <section>
    <h2>{{ t('set.folders') }}</h2>
    <p class="faint">{{ t('folder.hint') }}</p>

    <form class="card new" @submit.prevent="create">
      <label>{{ t('folder.new') }}
        <input v-model="newName" class="input" maxlength="128" :placeholder="t('folder.namePh')" data-test="folder-new-name" />
      </label>
      <label v-if="hasDelim">{{ t('folder.inside') }}
        <select v-model="newParent" class="input">
          <option value="">{{ t('folder.topLevel') }}</option>
          <option v-for="f in state.folders" :key="f.name" :value="f.name">{{ path(f) }}</option>
        </select>
      </label>
      <div class="actions"><button class="btn primary" :disabled="busy || !newName.trim()">{{ t('folder.create') }}</button></div>
    </form>

    <div v-if="roleFolder('archive') && archive !== null" class="card">
      <label>{{ t('folder.archiveTo') }}
        <select class="input" :value="archive" data-test="archive-structure"
                @change="setArchive(($event.target as HTMLSelectElement).value as Prefs['archive_structure'])">
          <option value="">{{ t('folder.archiveFlat', { name: folderTitle(roleFolder('archive')) }) }}</option>
          <option value="year">{{ t('folder.archiveYear', { path: `${folderTitle(roleFolder('archive'))} / ${new Date().getFullYear()}` }) }}</option>
          <option value="month">{{ t('folder.archiveMonth', { path: `${folderTitle(roleFolder('archive'))} / ${new Date().getFullYear()} / ${String(new Date().getMonth() + 1).padStart(2, '0')}` }) }}</option>
        </select>
      </label>
    </div>

    <div class="card list">
      <div v-for="f in state.folders" :key="f.name" class="row" :data-folder="f.name">
        <template v-if="editing === f.name">
          <form class="edit" @submit.prevent="save(f)">
            <input v-model="editName" class="input" maxlength="128" :aria-label="t('folder.nameLabel')" @keydown.esc="editing = ''" />
            <select v-if="hasDelim" v-model="editParent" class="input" :aria-label="t('folder.location')">
              <option value="">{{ t('folder.topLevel') }}</option>
              <option v-for="p in parents(f)" :key="p.name" :value="p.name">{{ path(p) }}</option>
            </select>
            <button class="btn primary" :disabled="busy || !editName.trim()">{{ t('common.save') }}</button>
            <button type="button" class="btn" @click="editing = ''">{{ t('common.cancel') }}</button>
          </form>
        </template>
        <template v-else>
          <label class="check sub" :style="{ paddingLeft: `${f.depth * 18}px` }"
                 :title="t(f.role ? 'folder.systemVisible' : 'folder.showInList')">
            <input type="checkbox" :checked="f.subscribed || !!f.role" :disabled="!!f.role"
                   @change="subscribeFolder(f, ($event.target as HTMLInputElement).checked)" />
            <span class="ellipsis" :class="{ faint: !f.selectable }">{{ title(f) }}</span>
          </label>
          <span class="faint count">{{ f.selectable ? f.total : '' }}</span>
          <span class="tools">
            <template v-if="!isSystem(f)">
              <button class="icon" :title="t('folder.renameMove')" :aria-label="t('folder.rename')" @click="edit(f)"><Pencil :size="15" /></button>
              <button class="icon danger" :title="t('common.delete')" :aria-label="t('common.delete')" @click="remove(f)"><Trash2 :size="15" /></button>
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
