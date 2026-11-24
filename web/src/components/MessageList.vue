<script setup lang="ts">
import { computed } from 'vue'
import { Archive, ChevronDown, ChevronRight, Flag, Forward, MessagesSquare, Paperclip, Reply, Search, Trash2 } from 'lucide-vue-next'
import type { Summary } from '../api'
import { dateGroup, listDate, who } from '../format'
import { t } from '../i18n'
import Avatar from './Avatar.vue'
import {
  archiveSelected, currentFolder, deleteSelected, folderTitle, headOf, isExpanded, loadList, setThreads, threadOf, toggleThread, visibleRows, openDraft, openMessage, roleFolder, setFlag, state,
} from '../store'

const sentLike = computed(() => ['sent', 'drafts'].includes(currentFolder()?.role ?? ''))

// Строка списка: письмо и, если это голова свёрнутой цепочки или корень развёрнутой, — цепочка.
// tree — разметка веток для писем развёрнутой цепочки.
interface Tree {
  depth: number // уровень письма в дереве (0 — корень, у него аватар)
  lines: boolean[] // по столбцам 0..depth-2: продолжается ли ветка предка ниже этой строки
  more: boolean // ниже есть ещё ответы того же родителя (├ вместо └)
  kids: boolean // у письма есть ответы: ветка уходит вниз из его узла
}
interface Row { m: Summary; head?: Summary; collapsed: boolean; size: number; tree?: Tree }
const threads = computed(() => state.prefs.threads === 'on')
const maxDepth = 6

// Ветки цепочки: письма идут в порядке обхода дерева, родитель раньше ответов.
function treeOf(thread: Summary[]): Map<number, Tree> {
  // Если корней несколько (общий предок не дошёл), остальные корни вешаем на первый.
  const second = thread.findIndex((x, i) => i > 0 && !x.depth)
  const d = thread.map((x, i) => (i === 0 ? 0 : Math.min((x.depth ?? 0) + (second > 0 && i >= second ? 1 : 0), maxDepth)))
  // Продолжается ли ниже строки i ветка, идущая из столбца col (то есть будет ли ещё
  // ответ уровня col+1 раньше, чем дерево поднимется до уровня col).
  const continues = (i: number, col: number) => {
    for (let j = i + 1; j < d.length; j++) {
      if (d[j] <= col) return false
      if (d[j] === col + 1) return true
    }
    return false
  }
  const out = new Map<number, Tree>()
  thread.forEach((x, i) => out.set(x.uid, {
    depth: d[i],
    lines: Array.from({ length: Math.max(d[i] - 1, 0) }, (_, col) => continues(i, col)),
    more: d[i] > 0 && continues(i, d[i] - 1),
    kids: i + 1 < d.length && d[i + 1] > d[i],
  }))
  return out
}

const rows = computed<Row[]>(() => {
  const trees = new Map<number, Map<number, Tree>>()
  return visibleRows().map((m) => {
    const head = m.thread ? m : undefined
    if (head && !isExpanded(head)) return { m, head, collapsed: true, size: head.thread!.length }
    const h = threads.value ? headOf(m.uid) : undefined
    if (!h?.thread) return { m, collapsed: false, size: 0 }
    if (!trees.has(h.uid)) trees.set(h.uid, treeOf(h.thread))
    const tree = trees.get(h.uid)!.get(m.uid)
    // В развёрнутой цепочке переключатель — у её первого письма.
    if (h.thread[0].uid === m.uid) return { m, head: h, collapsed: false, size: h.thread.length, tree }
    return { m, collapsed: false, size: 0, tree }
  })
})

// Геометрия веток, px: столбец 0 — под центром аватара корня, дальше шаг step.
const step = 16
const col0 = 17
const colX = (c: number) => col0 + c * step
const treeWidth = (t: Tree) => colX(t.depth) + 10

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
        <article v-for="{ m, head, collapsed, size, tree } in g.items" :key="m.uid" class="item" :data-uid="m.uid"
                 :class="{ unread: any({ m, head, collapsed, size }, (x) => !x.seen), selected: state.selected.has(m.uid),
                           current: m.uid === state.currentUID, member: !!tree, reply: !!tree?.depth }"
                 draggable="true" tabindex="0"
                 @click="onClick($event, m)" @keydown.enter="openMessage(m.uid)"
                 @dblclick="currentFolder()?.role === 'drafts' && openDraft(m.uid)"
                 @dragstart="onDragStart($event, m)">
          <div v-if="tree?.depth" class="tree" :style="{ width: `${treeWidth(tree)}px` }" aria-hidden="true">
            <i v-for="(on, c) in tree.lines" v-show="on" :key="c" class="v" :style="{ left: `${colX(c)}px` }" />
            <i class="v" :class="tree.more ? '' : 'half'" :style="{ left: `${colX(tree.depth - 1)}px` }" />
            <i class="h" :style="{ left: `${colX(tree.depth - 1) + 2}px`, width: `${step - 4}px` }" />
            <i v-if="tree.kids" class="v down" :style="{ left: `${colX(tree.depth)}px` }" />
            <i class="dot" :style="{ left: `${colX(tree.depth)}px` }" />
          </div>
          <template v-else>
            <Avatar :addr="sentLike ? m.to[0] : m.from[0]" :size="34" />
            <i v-if="tree?.kids" class="stem" aria-hidden="true" />
          </template>
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
.messages { display: flex; flex-direction: column; min-width: 0; background: var(--pane);
  --thread-line: color-mix(in srgb, var(--text-2) 75%, transparent); }
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
.item.reply { padding-top: 6px; padding-bottom: 6px; }
/* Ветки цепочки: линии на всю высоту строки, чтобы соседние строки сливались в дерево. */
/* Линии — пунктир: штрих 3px, просвет 2px; одинаковые по вертикали и горизонтали; координаты целые,
   чтобы не размывались. --y — высота узла: середина строки с отправителем. */
.tree { flex: none; align-self: stretch; position: relative; margin: -6px 0; --y: 16px; }
.tree i, .stem { position: absolute; display: block; }
.tree .v, .stem { top: 0; bottom: 0; width: 1px;
  background: repeating-linear-gradient(to bottom, var(--thread-line) 0 3px, transparent 3px 5px); }
.tree .v.half { bottom: auto; height: calc(var(--y) + 1px); }
.tree .v.down { top: calc(var(--y) + 3px); }
.tree .h { top: var(--y); height: 1px;
  background: repeating-linear-gradient(to right, var(--thread-line) 0 3px, transparent 3px 5px); }
.tree .dot { top: calc(var(--y) - 2px); width: 5px; height: 5px; margin-left: -2px; box-sizing: border-box;
  border: 1px solid var(--thread-line); border-radius: 50%; background: var(--pane); }
.item.current .tree .dot { border-color: var(--accent); background: var(--accent); }
.stem { left: 31px; top: 43px; }
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
