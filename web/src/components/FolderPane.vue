<script setup lang="ts">
import { computed, ref } from 'vue'
import { Archive, FileText, Folder, Inbox, PenSquare, Send, ShieldAlert, Trash2 } from 'lucide-vue-next'
import { t } from '../i18n'
import { folderTitle, moveSelected, openCompose, openFolder, state } from '../store'

const icons: Record<string, unknown> = {
  inbox: Inbox, drafts: FileText, sent: Send, archive: Archive, junk: ShieldAlert, trash: Trash2,
}
const special = computed(() => state.folders.filter((f) => f.role))
const others = computed(() => state.folders.filter((f) => !f.role))
const dropTarget = ref('')

function onDrop(e: DragEvent, name: string) {
  dropTarget.value = ''
  if (e.dataTransfer?.getData('application/x-webmail-uids')) void moveSelected(name)
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
        <div v-if="group === others && others.length" class="sep" />
        <button v-for="f in group" :key="f.name" class="folder"
                :class="{ active: f.name === state.folder, drop: dropTarget === f.name, dim: !f.selectable }"
                :style="{ paddingLeft: `${12 + f.depth * 14}px` }"
                :disabled="!f.selectable"
                @click="openFolder(f.name)"
                @dragover.prevent="dropTarget = f.name"
                @dragleave="dropTarget = ''"
                @drop.prevent="onDrop($event, f.name)">
          <component :is="(f.role && icons[f.role]) || Folder" :size="16" class="ico" />
          <span class="name ellipsis">{{ folderTitle(f) }}</span>
          <span v-if="f.unread && f.role !== 'sent' && f.role !== 'trash'" class="count"
                :class="{ strong: f.role === 'inbox' }">{{ f.unread }}</span>
        </button>
      </template>
    </div>
  </nav>
</template>

<style scoped>
.folders { display: flex; flex-direction: column; min-width: 0; background: var(--pane-alt); }
.head { padding: 12px; }
.compose { width: 100%; justify-content: center; height: 34px; }
.list { flex: 1; padding: 0 8px 12px; }
.sep { height: 1px; background: var(--border); margin: 8px 6px; }
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
</style>
