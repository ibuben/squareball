<script setup lang="ts">
import { computed, ref } from 'vue'
import { X } from 'lucide-vue-next'
import type { Address } from '../api'
import { isEmail, parseAddresses, who } from '../format'
import { state } from '../store'

const model = defineModel<Address[]>({ required: true })
defineProps<{ label: string }>()
const text = ref('')
const focused = ref(false)

// Подсказки из адресов, встречавшихся в загруженных письмах (до появления адресной книги).
const known = computed(() => {
  const map = new Map<string, Address>()
  for (const m of state.messages) for (const a of [...m.from, ...m.to]) {
    if (a.email && !map.has(a.email.toLowerCase())) map.set(a.email.toLowerCase(), a)
  }
  return [...map.values()]
})
const suggestions = computed(() => {
  const q = text.value.trim().toLowerCase()
  if (q.length < 2) return []
  const chosen = new Set(model.value.map((a) => a.email.toLowerCase()))
  return known.value
    .filter((a) => !chosen.has(a.email.toLowerCase()) && (a.email.toLowerCase().includes(q) || a.name?.toLowerCase().includes(q)))
    .slice(0, 6)
})

function commit() {
  const parsed = parseAddresses(text.value)
  if (parsed.length) model.value = [...model.value, ...parsed]
  text.value = ''
}
function pick(a: Address) {
  model.value = [...model.value, a]
  text.value = ''
}
function onKey(e: KeyboardEvent) {
  if ((e.key === 'Enter' || e.key === ',' || e.key === ';' || e.key === 'Tab') && text.value.trim()) {
    e.preventDefault()
    if (suggestions.value.length && e.key !== ',' && e.key !== ';') pick(suggestions.value[0])
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
    <ul v-if="focused && suggestions.length" class="suggest">
      <li v-for="s in suggestions" :key="s.email" @mousedown.prevent="pick(s)">
        <strong>{{ s.name || s.email }}</strong> <span class="faint">{{ s.name ? s.email : '' }}</span>
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
.suggest li:hover { background: var(--hover); }
</style>
