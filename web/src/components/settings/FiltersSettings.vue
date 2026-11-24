<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ArrowDown, ArrowUp, Code, Pencil, Plus, Trash2, X } from 'lucide-vue-next'
import type { Action, Condition, Rule } from '../../api'
import { cloneRuleset, loadSieve, saveSieve, sieve } from '../../sieve'
import { state } from '../../store'

const fields: Record<string, string> = {
  all: 'Все письма', from: 'От', to: 'Кому', cc: 'Копия', to_or_cc: 'Кому или Копия',
  subject: 'Тема', header: 'Заголовок…', body: 'Текст письма', size: 'Размер',
}
const ops: Record<string, string> = {
  contains: 'содержит', not_contains: 'не содержит', is: 'равно', not_is: 'не равно',
  matches: 'по маске (* ?)', not_matches: 'не по маске', exists: 'есть', not_exists: 'нет',
}
const actions: Record<string, string> = {
  fileinto: 'Переместить в папку', copyto: 'Копировать в папку', redirect: 'Переслать',
  redirect_copy: 'Переслать копию', seen: 'Пометить прочитанным', flag: 'Пометить флагом',
  discard: 'Удалить', reject: 'Отклонить с ответом', keep: 'Оставить во входящих',
}
const needsFolder = (t: string) => t === 'fileinto' || t === 'copyto'
const needsValue = (t: string) => ['fileinto', 'copyto', 'redirect', 'redirect_copy', 'reject'].includes(t)
const opsFor = (c: Condition) =>
  c.field === 'size' ? { over: 'больше', under: 'меньше' }
    : c.field === 'header' ? ops
      : Object.fromEntries(Object.entries(ops).filter(([k]) => !k.includes('exists')))

const editIndex = ref<number | null>(null) // -1 — новое правило
const draft = ref<Rule | null>(null)
const error = ref('')
const rawText = ref('')
const rawMode = ref(false)

onMounted(async () => {
  await loadSieve()
  rawMode.value = sieve.state?.mode === 'raw'
  rawText.value = sieve.state?.raw ?? ''
})

const rules = computed(() => sieve.state?.ruleset?.rules ?? [])
const folders = computed(() => state.folders.filter((f) => f.selectable))

function summary(r: Rule): string {
  const conds = r.conditions.map((c) =>
    c.field === 'all' ? 'все письма'
      : `${c.field === 'header' ? c.header : fields[c.field]?.toLowerCase()} ${ops[c.op] ?? (c.op === 'over' ? 'больше' : 'меньше')} ${c.value ?? ''}`.trim())
  const acts = r.actions.map((a) => `${actions[a.type]?.toLowerCase()}${a.value ? ` «${a.value}»` : ''}`)
  return `Если ${conds.join(r.match === 'any' ? ' или ' : ' и ')} — ${acts.join(', ')}${r.stop ? ', дальше не проверять' : ''}`
}

function startEdit(i: number) {
  error.value = ''
  editIndex.value = i
  draft.value = i >= 0
    ? JSON.parse(JSON.stringify(rules.value[i]))
    : { name: 'Новое правило', enabled: true, match: 'all', stop: false,
        conditions: [{ field: 'from', op: 'contains', value: '' }],
        actions: [{ type: 'fileinto', value: folders.value.find((f) => f.role === 'archive')?.name ?? 'INBOX' }] }
}

function addCondition() { draft.value!.conditions.push({ field: 'subject', op: 'contains', value: '' }) }
function addAction() { draft.value!.actions.push({ type: 'seen' }) }
function onField(c: Condition) {
  if (c.field === 'size') { c.op = 'over'; c.value = '1M' }
  else if (c.op === 'over' || c.op === 'under') c.op = 'contains'
  if (c.field !== 'header') delete c.header
}
function onAction(a: Action) {
  if (!needsValue(a.type)) delete a.value
  else if (needsFolder(a.type) && !a.value) a.value = folders.value[0]?.name
}

async function persist(mutate: (rs: ReturnType<typeof cloneRuleset>) => void): Promise<boolean> {
  const rs = cloneRuleset()
  mutate(rs)
  error.value = await saveSieve({ ruleset: rs })
  return !error.value
}

async function saveRule() {
  const d = draft.value!
  const i = editIndex.value!
  if (await persist((rs) => { if (i >= 0) rs.rules[i] = d; else rs.rules.push(d) })) {
    editIndex.value = null
    draft.value = null
  }
}
const toggle = (i: number) => persist((rs) => { rs.rules[i].enabled = !rs.rules[i].enabled })
const move = (i: number, d: -1 | 1) => persist((rs) => { const [r] = rs.rules.splice(i, 1); rs.rules.splice(i + d, 0, r) })
async function remove(i: number) {
  if (confirm(`Удалить правило «${rules.value[i].name}»?`)) await persist((rs) => { rs.rules.splice(i, 1) })
}

async function saveRaw() {
  error.value = await saveSieve({ raw: rawText.value })
  if (!error.value && sieve.state) {
    // После сохранения текста пробуем снова показать правила формой.
    await loadSieve(true)
    rawMode.value = sieve.state?.mode === 'raw'
  }
}
function openRaw() { rawText.value = sieve.state?.raw ?? ''; rawMode.value = true; error.value = '' }
</script>

<template>
  <section>
    <h2>Фильтры</h2>
    <p v-if="sieve.disabled" class="muted">На сервере не включён ManageSieve — фильтры недоступны.</p>
    <p v-else-if="sieve.loading && !sieve.state" class="muted">Загрузка…</p>
    <p v-else-if="sieve.error" class="error">{{ sieve.error }}</p>

    <template v-else-if="sieve.state">
      <template v-if="rawMode">
        <p class="muted">
          <template v-if="sieve.state.mode === 'raw'">
            Скрипт «{{ sieve.state.script_name }}» содержит конструкции, которые нельзя показать формой без потерь,
            поэтому он открыт как текст.
          </template>
          <template v-else>Текст скрипта «{{ sieve.state.script_name }}». Сервер проверит его перед сохранением.</template>
        </p>
        <textarea v-model="rawText" class="raw" spellcheck="false" />
        <p v-if="error" class="error" role="alert">{{ error }}</p>
        <div class="actions">
          <button class="btn primary" :disabled="sieve.saving" @click="saveRaw">Сохранить скрипт</button>
          <button v-if="sieve.state.mode === 'rules'" class="btn" @click="rawMode = false">Вернуться к правилам</button>
        </div>
      </template>

      <template v-else>
        <p class="muted">Правила применяются сервером к каждому входящему письму сверху вниз — даже когда почта закрыта.</p>
        <div class="list">
          <div v-for="(r, i) in rules" :key="i" class="row" :class="{ off: !r.enabled }">
            <label class="switch" :title="r.enabled ? 'Выключить' : 'Включить'">
              <input type="checkbox" :checked="r.enabled" :disabled="sieve.saving" @change="toggle(i)" />
            </label>
            <div class="text">
              <strong>{{ r.name || 'Без названия' }}</strong>
              <span class="muted small">{{ summary(r) }}</span>
            </div>
            <button class="icon-btn" aria-label="Выше" :disabled="i === 0 || sieve.saving" @click="move(i, -1)"><ArrowUp :size="15" /></button>
            <button class="icon-btn" aria-label="Ниже" :disabled="i === rules.length - 1 || sieve.saving" @click="move(i, 1)"><ArrowDown :size="15" /></button>
            <button class="icon-btn" aria-label="Изменить" @click="startEdit(i)"><Pencil :size="15" /></button>
            <button class="icon-btn" aria-label="Удалить" @click="remove(i)"><Trash2 :size="15" /></button>
          </div>
          <p v-if="!rules.length" class="faint">Правил пока нет.</p>
        </div>
        <div class="actions">
          <button class="btn" @click="startEdit(-1)"><Plus :size="15" /> Добавить правило</button>
          <button class="btn" @click="openRaw"><Code :size="15" /> Редактировать как текст</button>
        </div>

        <form v-if="draft" class="card" @submit.prevent="saveRule">
          <label>Название <input v-model="draft.name" class="input" required /></label>
          <div class="line">
            Если выполняются
            <select v-model="draft.match" class="input sel">
              <option value="all">все условия</option>
              <option value="any">любое из условий</option>
            </select>
          </div>
          <div v-for="(c, ci) in draft.conditions" :key="'c' + ci" class="line">
            <select v-model="c.field" class="input sel" @change="onField(c)">
              <option v-for="(label, k) in fields" :key="k" :value="k">{{ label }}</option>
            </select>
            <input v-if="c.field === 'header'" v-model="c.header" class="input hdr" placeholder="X-Header" required />
            <template v-if="c.field !== 'all'">
              <select v-model="c.op" class="input sel">
                <option v-for="(label, k) in opsFor(c)" :key="k" :value="k">{{ label }}</option>
              </select>
              <input v-if="!c.op.includes('exists')" v-model="c.value" class="input val"
                     :placeholder="c.field === 'size' ? '1M, 500K' : 'значение'" required />
            </template>
            <button v-if="draft.conditions.length > 1" type="button" class="icon-btn" aria-label="Убрать условие"
                    @click="draft.conditions.splice(ci, 1)"><X :size="15" /></button>
          </div>
          <button type="button" class="link" @click="addCondition">+ условие</button>

          <div class="line head">то</div>
          <div v-for="(a, ai) in draft.actions" :key="'a' + ai" class="line">
            <select v-model="a.type" class="input sel" @change="onAction(a)">
              <option v-for="(label, k) in actions" :key="k" :value="k">{{ label }}</option>
            </select>
            <select v-if="needsFolder(a.type)" v-model="a.value" class="input val" required>
              <option v-for="f in folders" :key="f.name" :value="f.name">{{ f.name }}</option>
            </select>
            <input v-else-if="needsValue(a.type)" v-model="a.value" class="input val" required
                   :type="a.type.startsWith('redirect') ? 'email' : 'text'"
                   :placeholder="a.type === 'reject' ? 'Текст ответа отправителю' : 'адрес'" />
            <button v-if="draft.actions.length > 1" type="button" class="icon-btn" aria-label="Убрать действие"
                    @click="draft.actions.splice(ai, 1)"><X :size="15" /></button>
          </div>
          <button type="button" class="link" @click="addAction">+ действие</button>
          <label class="check"><input v-model="draft.stop" type="checkbox" /> Не проверять следующие правила</label>
          <label class="check"><input v-model="draft.enabled" type="checkbox" /> Правило включено</label>
          <p v-if="error" class="error" role="alert">{{ error }}</p>
          <div class="actions">
            <button class="btn primary" :disabled="sieve.saving">Сохранить правило</button>
            <button type="button" class="btn" @click="draft = null; editIndex = null">Отмена</button>
          </div>
        </form>
        <p v-else-if="error" class="error" role="alert">{{ error }}</p>
      </template>
    </template>
  </section>
</template>

<style scoped>
.list { display: grid; gap: 6px; margin: 14px 0; }
.row { display: flex; align-items: center; gap: 8px; padding: 8px 10px; border: 1px solid var(--border); border-radius: 8px; background: var(--pane); }
.row.off .text { opacity: .55; }
.text { flex: 1; display: grid; min-width: 0; }
.small { font-size: 12px; }
.line { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.line.head { color: var(--text-2); margin-top: 6px; }
.sel { width: auto; min-width: 150px; }
.hdr { width: 140px; }
.val { flex: 1; min-width: 160px; }
.link { align-self: flex-start; border: 0; background: none; color: var(--accent); padding: 0; }
.raw { width: 100%; min-height: 360px; font: 12px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace; padding: 10px;
  border: 1px solid var(--border); border-radius: 8px; background: var(--pane); color: var(--text); }
.switch input { width: 16px; height: 16px; }
</style>
