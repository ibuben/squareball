<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Users, X } from 'lucide-vue-next'
import { api, type Address, type Suggestion } from '../api'
import { isEmail, parseAddresses, who } from '../format'
import { state } from '../store'

const model = defineModel<Address[]>({ required: true })
defineProps<{ label: string }>()
const text = ref('')
const focused = ref(false)
const remote = ref<Suggestion[]>([])
const active = ref(0)

// Подсказки: адресная книга, группы и собранные адреса с сервера + адреса
// из загруженных писем (они видны сразу, пока идёт запрос).
let timer = 0
let seq = 0
watch(text, (v) => {
  clearTimeout(timer)
  const q = v.trim()
  if (q.length < 1) { remote.value = []; return }
  timer = window.setTimeout(async () => {
    const my = ++seq
    try {
      const r = await api.suggest(q)
      if (my === seq) remote.value = r.suggestions
    } catch { /* подсказки необязательны */ }
  }, 150)
})

const suggestions = computed<Suggestion[]>(() => {
  const q = text.value.trim().toLowerCase()
  if (!q) return []
  const chosen = new Set(model.value.map((a) => a.email.toLowerCase()))
  const out: Suggestion[] = remote.value.filter((s) => s.kind === 'group' || !chosen.has(s.email!.toLowerCase()))
  const seen = new Set(out.map((s) => s.email?.toLowerCase()))
  for (const m of state.messages) {
    for (const a of [...m.from, ...m.to]) {
      const e = a.email.toLowerCase()
      if (!seen.has(e) && !chosen.has(e) && (e.includes(q) || a.name?.toLowerCase().includes(q))) {
        seen.add(e)
        out.push({ kind: 'collected', name: a.name ?? '', email: a.email })
      }
    }
  }
  return out.slice(0, 8)
})
watch(suggestions, () => { active.value = 0 })

function commit() {
  const parsed = parseAddresses(text.value)
  if (parsed.length) model.value = [...model.value, ...parsed]
  text.value = ''
}
function pick(s: Suggestion) {
  const chosen = new Set(model.value.map((a) => a.email.toLowerCase()))
  const add: Address[] = s.kind === 'group'
    ? (s.members ?? []).filter((m) => !chosen.has(m.email)).map((m) => ({ name: m.name, email: m.email }))
    : [{ name: s.name || undefined, email: s.email! }]
  model.value = [...model.value, ...add]
  text.value = ''
  remote.value = []
}
function onKey(e: KeyboardEvent) {
  const list = suggestions.value
  if (e.key === 'ArrowDown' && list.length) { e.preventDefault(); active.value = (active.value + 1) % list.length; return }
  if (e.key === 'ArrowUp' && list.length) { e.preventDefault(); active.value = (active.value - 1 + list.length) % list.length; return }
  if (e.key === 'Escape') { remote.value = []; text.value = text.value.trim(); return }
  if ((e.key === 'Enter' || e.key === ',' || e.key === ';' || e.key === 'Tab') && text.value.trim()) {
    e.preventDefault()
    // Полный адрес без точного совпадения в подсказках добавляем как введён,
    // а не первой подсказкой (она может быть другим адресом с тем же началом).
    const typed = text.value.trim().toLowerCase()
    const exact = list.find((s) => s.email?.toLowerCase() === typed)
    if (exact) pick(exact)
    else if (isEmail(typed) && !parseAddresses(text.value).some((a) => !isEmail(a.email))) commit()
    else if (list.length && e.key !== ',' && e.key !== ';') pick(list[active.value])
    else commit()
  } else if (e.key === 'Backspace' && !text.value && model.value.length) {
    model.value = model.value.slice(0, -1)
  }
}
function remove(i: number) { model.value = model.value.filter((_, j) => j !== i) }
</script>

<template>
  <div class="field">
    <span class="label">{{ label }}</span>
    <div class="chips">
      <span v-for="(a, i) in model" :key="a.email + i" class="chip" :class="{ bad: !isEmail(a.email) }" :title="a.email">
        {{ who(a) }}
        <button type="button" @click="remove(i)" aria-label="remove"><X :size="12" /></button>
      </span>
      <input v-model="text" @keydown="onKey" @focus="focused = true"
             @blur="focused = false; text.trim() && commit()" @paste.prevent="text += $event.clipboardData?.getData('text') ?? ''; commit()" />
    </div>
    <ul v-if="focused && suggestions.length" class="suggest" role="listbox">
      <li v-for="(s, i) in suggestions" :key="s.kind + (s.email ?? s.group_id)" role="option"
          :class="{ on: i === active }" :aria-selected="i === active"
          @mousedown.prevent="pick(s)" @mouseenter="active = i">
        <template v-if="s.kind === 'group'">
          <Users :size="14" class="faint" /> <strong>{{ s.name }}</strong>
          <span class="faint">группа · {{ s.members?.length }}</span>
        </template>
        <template v-else>
          <strong>{{ s.name || s.email }}</strong> <span class="faint">{{ s.name ? s.email : '' }}</span>
          <span v-if="s.kind === 'contact'" class="tag">контакт</span>
        </template>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.field { position: relative; display: flex; align-items: center; gap: 8px; min-height: 36px; padding: 2px 12px; border-bottom: 1px solid var(--border); }
.label { color: var(--text-3); width: 56px; flex: none; }
.chips { flex: 1; display: flex; flex-wrap: wrap; gap: 4px; align-items: center; min-width: 0; }
.chips input { flex: 1; min-width: 120px; border: 0; outline: none; background: transparent; height: 28px; }
.chip { display: inline-flex; align-items: center; gap: 4px; padding: 2px 4px 2px 8px; border-radius: 12px; background: var(--accent-soft); color: var(--text); }
.chip.bad { background: #fde3e3; color: var(--danger); }
.chip button { display: inline-flex; border: 0; background: none; color: inherit; padding: 2px; border-radius: 50%; }
.suggest { position: absolute; left: 76px; top: 100%; z-index: 5; margin: 0; padding: 4px; list-style: none; min-width: 260px;
  background: var(--pane); border: 1px solid var(--border); border-radius: 8px; box-shadow: var(--shadow); }
.suggest li { padding: 6px 8px; border-radius: 6px; cursor: pointer; }
.suggest li { display: flex; align-items: center; gap: 6px; }
.suggest li.on { background: var(--hover); }
.tag { margin-left: auto; font-size: 11px; color: var(--accent); }
</style>
