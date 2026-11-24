<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ArrowDown, ArrowUp, Code, Pencil, Plus, Trash2, X } from 'lucide-vue-next'
import type { Action, Condition, Rule } from '../../api'
import { cloneRuleset, loadSieve, saveSieve, sieve } from '../../sieve'
import { state } from '../../store'
import { t, type Key } from '../../i18n'

// Подписи — по ключам flt.f.*, flt.op.*, flt.a.*; пересчитываются при смене языка.
const labels = (prefix: string, keys: string[]) =>
  computed(() => Object.fromEntries(keys.map((k) => [k, t(`${prefix}.${k}` as Key)])) as Record<string, string>)
const fields = labels('flt.f', ['all', 'from', 'to', 'cc', 'to_or_cc', 'subject', 'header', 'body', 'size'])
const ops = labels('flt.op', ['contains', 'not_contains', 'is', 'not_is', 'matches', 'not_matches', 'exists', 'not_exists'])
const sizeOps = labels('flt.op', ['over', 'under'])
const actions = labels('flt.a', ['fileinto', 'copyto', 'redirect', 'redirect_copy', 'seen', 'flag', 'discard', 'reject', 'keep'])
const needsFolder = (a: string) => a === 'fileinto' || a === 'copyto'
const needsValue = (a: string) => ['fileinto', 'copyto', 'redirect', 'redirect_copy', 'reject'].includes(a)
const opsFor = (c: Condition) =>
  c.field === 'size' ? sizeOps.value
    : c.field === 'header' ? ops.value
      : Object.fromEntries(Object.entries(ops.value).filter(([k]) => !k.includes('exists')))

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
    c.field === 'all' ? t('flt.allLower')
      : `${c.field === 'header' ? c.header : fields.value[c.field]?.toLowerCase()} ${ops.value[c.op] ?? sizeOps.value[c.op] ?? c.op} ${c.value ?? ''}`.trim())
  const acts = r.actions.map((a) => `${actions.value[a.type]?.toLowerCase()}${a.value ? ` «${a.value}»` : ''}`)
  return t('flt.summary', { conds: conds.join(t(r.match === 'any' ? 'flt.or' : 'flt.and')), acts: acts.join(', ') }) + (r.stop ? t('flt.stopSuffix') : '')
}

function startEdit(i: number) {
  error.value = ''
  editIndex.value = i
  draft.value = i >= 0
    ? JSON.parse(JSON.stringify(rules.value[i]))
    : { name: t('flt.newRule'), enabled: true, match: 'all', stop: false,
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
  if (confirm(t('flt.confirmDelete', { name: rules.value[i].name }))) await persist((rs) => { rs.rules.splice(i, 1) })
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
    <h2>{{ t('set.filters') }}</h2>
    <p v-if="sieve.disabled" class="muted">{{ t('flt.noSieve') }}</p>
    <p v-else-if="sieve.loading && !sieve.state" class="muted">{{ t('common.loading') }}</p>
    <p v-else-if="sieve.error" class="error">{{ sieve.error }}</p>

    <template v-else-if="sieve.state">
      <template v-if="rawMode">
        <p class="muted">
          <template v-if="sieve.state.mode === 'raw'">
            {{ t('flt.rawForced', { name: sieve.state.script_name }) }}
          </template>
          <template v-else>{{ t('flt.rawHint', { name: sieve.state.script_name }) }}</template>
        </p>
        <textarea v-model="rawText" class="raw" spellcheck="false" />
        <p v-if="error" class="error" role="alert">{{ error }}</p>
        <div class="actions">
          <button class="btn primary" :disabled="sieve.saving" @click="saveRaw">{{ t('flt.saveScript') }}</button>
          <button v-if="sieve.state.mode === 'rules'" class="btn" @click="rawMode = false">{{ t('flt.backToRules') }}</button>
        </div>
      </template>

      <template v-else>
        <p class="muted">{{ t('flt.hint') }}</p>
        <div class="list">
          <div v-for="(r, i) in rules" :key="i" class="row" :class="{ off: !r.enabled }">
            <label class="switch" :title="t(r.enabled ? 'flt.disable' : 'flt.enable')">
              <input type="checkbox" :checked="r.enabled" :disabled="sieve.saving" @change="toggle(i)" />
            </label>
            <div class="text">
              <strong>{{ r.name || t('inv.untitled') }}</strong>
              <span class="muted small">{{ summary(r) }}</span>
            </div>
            <button class="icon-btn" :aria-label="t('flt.up')" :disabled="i === 0 || sieve.saving" @click="move(i, -1)"><ArrowUp :size="15" /></button>
            <button class="icon-btn" :aria-label="t('flt.down')" :disabled="i === rules.length - 1 || sieve.saving" @click="move(i, 1)"><ArrowDown :size="15" /></button>
            <button class="icon-btn" :aria-label="t('common.edit')" @click="startEdit(i)"><Pencil :size="15" /></button>
            <button class="icon-btn" :aria-label="t('common.delete')" @click="remove(i)"><Trash2 :size="15" /></button>
          </div>
          <p v-if="!rules.length" class="faint">{{ t('flt.none') }}</p>
        </div>
        <div class="actions">
          <button class="btn" @click="startEdit(-1)"><Plus :size="15" /> {{ t('flt.add') }}</button>
          <button class="btn" @click="openRaw"><Code :size="15" /> {{ t('flt.editRaw') }}</button>
        </div>

        <form v-if="draft" class="card" @submit.prevent="saveRule">
          <label>{{ t('flt.name') }} <input v-model="draft.name" class="input" required /></label>
          <div class="line">
            {{ t('flt.ifMatch') }}
            <select v-model="draft.match" class="input sel">
              <option value="all">{{ t('flt.matchAll') }}</option>
              <option value="any">{{ t('flt.matchAny') }}</option>
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
                     :placeholder="c.field === 'size' ? '1M, 500K' : t('flt.value')" required />
            </template>
            <button v-if="draft.conditions.length > 1" type="button" class="icon-btn" :aria-label="t('flt.removeCond')"
                    @click="draft.conditions.splice(ci, 1)"><X :size="15" /></button>
          </div>
          <button type="button" class="link" @click="addCondition">{{ t('flt.addCond') }}</button>

          <div class="line head">{{ t('flt.then') }}</div>
          <div v-for="(a, ai) in draft.actions" :key="'a' + ai" class="line">
            <select v-model="a.type" class="input sel" @change="onAction(a)">
              <option v-for="(label, k) in actions" :key="k" :value="k">{{ label }}</option>
            </select>
            <select v-if="needsFolder(a.type)" v-model="a.value" class="input val" required>
              <option v-for="f in folders" :key="f.name" :value="f.name">{{ f.name }}</option>
            </select>
            <input v-else-if="needsValue(a.type)" v-model="a.value" class="input val" required
                   :type="a.type.startsWith('redirect') ? 'email' : 'text'"
                   :placeholder="t(a.type === 'reject' ? 'flt.rejectPh' : 'flt.addressPh')" />
            <button v-if="draft.actions.length > 1" type="button" class="icon-btn" :aria-label="t('flt.removeAction')"
                    @click="draft.actions.splice(ai, 1)"><X :size="15" /></button>
          </div>
          <button type="button" class="link" @click="addAction">{{ t('flt.addAction') }}</button>
          <label class="check"><input v-model="draft.stop" type="checkbox" /> {{ t('flt.stop') }}</label>
          <label class="check"><input v-model="draft.enabled" type="checkbox" /> {{ t('flt.enabled') }}</label>
          <p v-if="error" class="error" role="alert">{{ error }}</p>
          <div class="actions">
            <button class="btn primary" :disabled="sieve.saving">{{ t('flt.saveRule') }}</button>
            <button type="button" class="btn" @click="draft = null; editIndex = null">{{ t('common.cancel') }}</button>
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
