<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Plus, Trash2 } from 'lucide-vue-next'
import { api, ApiError, type Template } from '../../api'
import { loadTemplates, state, toast } from '../../store'
import { t, type Key } from '../../i18n'
import RichEditor from '../RichEditor.vue'

const editing = ref<Partial<Template> | null>(null)
const editor = ref<InstanceType<typeof RichEditor>>()
const editorKey = ref(0)
const error = ref('')
const saving = ref(false)

onMounted(loadTemplates)

function edit(tpl?: Template) {
  error.value = ''
  editorKey.value++
  editing.value = tpl ? { ...tpl } : { id: 0, name: '', html: '' }
}

const errors: Record<string, Key> = {
  'template.name': 'tpl.errName',
  'template.too_large': 'tpl.errTooLarge',
  'template.limit': 'tpl.errLimit',
}

async function save() {
  if (!editing.value) return
  saving.value = true
  error.value = ''
  try {
    const html = editor.value?.isEmpty() ? '' : editor.value?.getHTML() ?? ''
    const saved = await api.saveTemplate({ ...editing.value, html })
    await loadTemplates()
    editing.value = null
    toast(t('tpl.saved', { name: saved.name }))
  } catch (e) {
    error.value = e instanceof ApiError ? (errors[e.code] ? t(errors[e.code]) : e.detail || e.code) : String(e)
  } finally {
    saving.value = false
  }
}

async function remove(tpl: Template) {
  if (!confirm(t('tpl.confirmDelete', { name: tpl.name }))) return
  try {
    await api.deleteTemplate(tpl.id)
    await loadTemplates()
    if (editing.value?.id === tpl.id) editing.value = null
  } catch { toast(t('common.deleteFailed')) }
}

const preview = (html: string) => new DOMParser().parseFromString(html, 'text/html').body.textContent?.trim().slice(0, 90) ?? ''
</script>

<template>
  <section>
    <h2>{{ t('set.templates') }}</h2>
    <p class="muted">{{ t('tpl.hint') }}</p>

    <div class="list">
      <div v-for="tpl in state.templates" :key="tpl.id" class="row" :class="{ on: editing?.id === tpl.id }"
           :data-template="tpl.name" @click="edit(tpl)">
        <div class="who">
          <strong>{{ tpl.name }}</strong>
          <span class="faint ellipsis">{{ preview(tpl.html) }}</span>
        </div>
        <button class="icon-btn" :aria-label="t('common.delete')" :title="t('common.delete')" @click.stop="remove(tpl)"><Trash2 :size="16" /></button>
      </div>
      <p v-if="!state.templates.length" class="faint">{{ t('tpl.noneYet') }}</p>
    </div>
    <button class="btn" data-test="template-new" @click="edit()"><Plus :size="15" /> {{ t('tpl.new') }}</button>

    <form v-if="editing" class="card" @submit.prevent="save">
      <label>{{ t('tpl.name') }} <input v-model="editing.name" class="input" maxlength="100" required data-test="template-name" /></label>
      <div class="sig-label">{{ t('tpl.text') }}</div>
      <div class="sig-editor">
        <RichEditor :key="editorKey" ref="editor" :content="editing.html ?? ''" :placeholder="t('tpl.textPh')" />
      </div>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div class="actions">
        <button class="btn primary" :disabled="saving">{{ t('common.save') }}</button>
        <button type="button" class="btn" @click="editing = null">{{ t('common.cancel') }}</button>
      </div>
    </form>
  </section>
</template>

<style scoped>
.list { display: grid; gap: 6px; margin: 14px 0; }
.row { display: flex; align-items: center; gap: 10px; padding: 10px 12px; border: 1px solid var(--border); border-radius: 8px; background: var(--pane); cursor: pointer; }
.row:hover, .row.on { border-color: var(--accent); }
.who { flex: 1; display: grid; min-width: 0; }
.sig-label { color: var(--text-2); }
.sig-editor { display: flex; flex-direction: column; min-height: 200px; border: 1px solid var(--border); border-radius: 8px; overflow: hidden; background: var(--pane); }
</style>
