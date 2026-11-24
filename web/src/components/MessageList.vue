<script setup lang="ts">
import { computed } from 'vue'
import { Archive, ChevronDown, ChevronRight, Flag, Forward, MessagesSquare, Paperclip, Reply, Search, Trash2 } from 'lucide-vue-next'
import type { Summary } from '../api'
import { dateGroup, listDate, who } from '../format'
import { t } from '../i18n'
import Avatar from './Avatar.vue'
import {
  archiveSelected, currentFolder, deleteSelected, folderTitle, headOf, loadList, setThreads, threadOf, toggleThread, visibleRows, openDraft, openMessage, roleFolder, setFlag, state,
} from '../store'

const sentLike = computed(() => ['sent', 'drafts'].includes(currentFolder()?.role ?? ''))

// Строка списка: письмо и, если это голова свёрнутой цепочки или корень развёрнутой, — цепочка.
interface Row { m: Summary; head?: Summary; collapsed: boolean; size: number }
const threads = computed(() => state.prefs.threads === 'on')

const rows = computed<Row[]>(() => visibleRows().map((m) => {
  const head = m.thread ? m : undefined
  if (head && !state.expanded.has(head.uid)) return { m, head, collapsed: true, size: head.thread!.length }
  const h = threads.value ? headOf(m.uid) : undefined
  // В развёрнутой цепочке переключатель — у её первого письма.
  if (h?.thread && h.thread[0].uid === m.uid) return { m, head: h, collapsed: false, size: h.thread.length }
  return { m, collapsed: false, size: 0 }
}))

// Свёрнутая цепочка показывает сводку: непрочитанное, флаг, вложения — по всем письмам.
const any = (r: Row, f: (m: Summary) => boolean) => (r.collapsed && r.head ? threadOf(r.head).some(f) : f(r.m))
const unreadIn = (r: Row) => (r.collapsed && r.head ? threadOf(r.head).filter((x) => !x.seen).length : 0)

const groups = computed(() => {
  const out: { label: string; items: Row[] }[] = []
  for (const r of rows.value) {
    // Вся цепочка — в группе даты её самого нового письма.
    const label = dateGroup((threads.value ? headOf(r.m.uid) ?? r.m : r.m).date)
    const last = out[out.length - 1]
    if (last && last.label === label) last.items.push(r)
    else out.push({ label, items: [r] })
  }
  return out
})

let lastClicked = 0
function onClick(e: MouseEvent, m: Summary) {
  if (e.ctrlKey || e.metaKey) {
    const s = new Set(state.selected)
    s.has(m.uid) ? s.delete(m.uid) : s.add(m.uid)
    state.selected = s
  } else if (e.shiftKey && lastClicked) {
    const ids = visibleRows().map((x) => x.uid)
    const [a, b] = [ids.indexOf(lastClicked), ids.indexOf(m.uid)].sort((x, y) => x - y)
    state.selected = new Set(ids.slice(a, b + 1))
  } else {
    void openMessage(m.uid)
  }
  lastClicked = m.uid
}

function onDragStart(e: DragEvent, m: Summary) {
  if (!state.selected.has(m.uid)) state.selected = new Set([m.uid])
  e.dataTransfer?.setData('application/x-webmail-uids', [...state.selected].join(','))
  if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
}

function quick(action: 'flag' | 'delete' | 'archive', m: Summary) {
  state.selected = new Set([m.uid])
  if (action === 'flag') void setFlag([m.uid], '\\Flagged', !m.flagged)
  else if (action === 'delete') void deleteSelected()
  else void archiveSelected()
}

let searchTimer = 0
function onSearch() {
  clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => void loadList(), 300)
}
</script>

<template>
  <section class="messages" aria-label="messages">
    <header class="head">
      <div class="title">
        <h2 class="ellipsis">{{ folderTitle(currentFolder()) }}</h2>
        <span class="faint">{{ state.total }}</span>
        <button class="threads" :class="{ on: threads }" role="switch" :aria-checked="threads" data-test="threads-switch"
                title="Группировать письма в цепочки" @click="setThreads(!threads)">
          <MessagesSquare :size="15" /> <span>Цепочки</span>
        </button>
      </div>
      <label class="search">
        <Search :size="15" class="faint" />
        <input v-model="state.search" :placeholder="t('search.placeholder')" @input="onSearch" data-search />
      </label>
      <div class="filters" role="tablist">
        <button v-for="f in (['', 'unread', 'flagged'] as const)" :key="f" role="tab"
                :class="{ on: state.filter === f }" @click="state.filter = f; loadList()">
          {{ t(f === '' ? 'filter.all' : f === 'unread' ? 'filter.unread' : 'filter.flagged') }}
        </button>
      </div>
    </header>

    <div class="scroll body">
      <p v-if="!state.loadingList && !state.messages.length" class="empty faint">{{ t('list.empty') }}</p>
      <template v-for="g in groups" :key="g.label">
        <div class="group">{{ g.label }}</div>
        <article v-for="{ m, head, collapsed, size } in g.items" :key="m.uid" class="item" :data-uid="m.uid"
                 :class="{ unread: any({ m, head, collapsed, size }, (x) => !x.seen), selected: state.selected.has(m.uid),
                           current: m.uid === state.currentUID, member: threads && !head && !!headOf(m.uid)?.thread }"
                 :style="m.depth && !collapsed ? { paddingLeft: `${14 + Math.min(m.depth, 6) * 14}px` } : undefined"
                 draggable="true" tabindex="0"
                 @click="onClick($event, m)" @keydown.enter="openMessage(m.uid)"
                 @dblclick="currentFolder()?.role === 'drafts' && openDraft(m.uid)"
                 @dragstart="onDragStart($event, m)">
          <Avatar :addr="sentLike ? m.to[0] : m.from[0]" :size="34" />
          <div class="main">
            <div class="row1">
              <span class="from ellipsis">{{ sentLike ? (who(m.to[0]) || '—') : who(m.from[0]) }}</span>
              <Reply v-if="m.answered" :size="13" class="faint" />
              <Forward v-if="m.forwarded" :size="13" class="faint" />
              <Paperclip v-if="any({ m, head, collapsed, size }, (x) => x.has_attachments)" :size="13" class="faint" />
              <span class="date faint">{{ listDate(m.date) }}</span>
            </div>
            <div class="row2">
              <button v-if="head" class="thread-toggle" :class="{ open: !collapsed }" data-test="thread-toggle"
                      :aria-expanded="!collapsed" :title="collapsed ? 'Показать цепочку' : 'Свернуть цепочку'"
                      @click.stop="toggleThread(head)">
                <component :is="collapsed ? ChevronRight : ChevronDown" :size="13" />
                {{ size }}<template v-if="unreadIn({ m, head, collapsed, size })"> · {{ unreadIn({ m, head, collapsed, size }) }} нов.</template>
              </button>
              <span v-if="m.priority === 1" class="prio" title="Высокая важность">!</span>
              <span class="subject ellipsis">{{ m.subject || t('list.nosubject') }}</span>
              <Flag v-if="any({ m, head, collapsed, size }, (x) => x.flagged)" :size="13" class="flag" />
            </div>
          </div>
          <div class="hover-actions" @click.stop>
            <button class="icon-btn" :title="t('read.flag')" @click="quick('flag', m)"><Flag :size="15" /></button>
            <button v-if="roleFolder('archive')" class="icon-btn" :title="t('read.archive')" @click="quick('archive', m)"><Archive :size="15" /></button>
            <button class="icon-btn" :title="t('read.delete')" @click="quick('delete', m)"><Trash2 :size="15" /></button>
          </div>
        </article>
      </template>
      <button v-if="state.messages.length < state.total" class="btn more" :disabled="state.loadingList"
              @click="loadList(true)">{{ t('list.more') }}</button>
    </div>
  </section>
</template>

<style scoped>
.messages { display: flex; flex-direction: column; min-width: 0; background: var(--pane); }
.head { padding: 12px 12px 8px; display: grid; gap: 10px; border-bottom: 1px solid var(--border); }
.title { display: flex; align-items: baseline; gap: 8px; }
h2 { margin: 0; font-size: 17px; }
.search { display: flex; align-items: center; gap: 8px; height: 32px; padding: 0 10px; border-radius: 6px; background: var(--pane-alt); border: 1px solid var(--border); }
.search input { flex: 1; min-width: 0; border: 0; background: transparent; outline: none; }
.filters { display: flex; gap: 4px; }
.filters button { border: 0; background: transparent; padding: 3px 10px; border-radius: 12px; color: var(--text-2); }
.filters button.on { background: var(--accent-soft); color: var(--accent); font-weight: 600; }
.body { flex: 1; }
.empty { text-align: center; margin-top: 40px; }
.group { position: sticky; top: 0; z-index: 1; padding: 8px 14px 4px; font-size: 12px; font-weight: 600; color: var(--text-2); background: var(--pane); }
.item {
  position: relative; display: flex; gap: 10px; padding: 9px 12px 9px 14px; cursor: default;
  border-left: 3px solid transparent; outline: none;
}
.item:hover { background: var(--hover); }
.item.selected { background: var(--selected); }
.item.current { border-left-color: var(--accent); }
.main { flex: 1; min-width: 0; display: grid; gap: 2px; }
.row1, .row2 { display: flex; align-items: center; gap: 6px; min-width: 0; }
.from { flex: 1; }
.subject { flex: 1; color: var(--text-2); }
.prio { flex: none; color: var(--danger); font-weight: 800; margin-right: 4px; }
.thread-toggle { flex: none; margin-right: 2px; display: inline-flex; align-items: center; gap: 2px; height: 20px; padding: 0 6px 0 3px;
  border: 1px solid var(--border); border-radius: 10px; background: var(--pane); color: var(--text-2); font-size: 12px; }
.thread-toggle:hover, .thread-toggle.open { border-color: var(--accent); color: var(--accent); }
.item.member { background: color-mix(in srgb, var(--pane-alt) 60%, transparent); }
.item.member.selected { background: var(--selected); }
.threads { margin-left: auto; flex: none; display: inline-flex; align-items: center; gap: 4px; height: 24px; padding: 0 8px;
  border: 1px solid var(--border); border-radius: 12px; background: none; color: var(--text-2); font-size: 12px; }
.threads.on { background: var(--accent-soft); border-color: transparent; color: var(--accent); font-weight: 600; }
.item.unread .from, .item.unread .subject { font-weight: 700; color: var(--text); }
.item.unread::before {
  content: ''; position: absolute; left: 4px; top: 50%; width: 6px; height: 6px; margin-top: -3px;
  border-radius: 50%; background: var(--accent);
}
.date { flex: none; font-size: 12px; }
.flag { color: var(--flag); flex: none; }
.hover-actions { position: absolute; right: 8px; bottom: 4px; display: none; background: var(--hover); border-radius: 6px; }
.item:hover .hover-actions { display: flex; }
.item.selected .hover-actions { background: var(--selected); }
.more { margin: 12px auto; display: flex; }
</style>
