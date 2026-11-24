<script setup lang="ts">
import { computed, ref } from 'vue'
import { Plus, Star, Trash2 } from 'lucide-vue-next'
import { api, ApiError, type Identity } from '../../api'
import { loadIdentities, state, toast } from '../../store'
import RichEditor from '../RichEditor.vue'

const editing = ref<Partial<Identity> | null>(null)
const editor = ref<InstanceType<typeof RichEditor>>()
const error = ref('')
const saving = ref(false)
const editorKey = ref(0) // пересоздаём редактор подписи при выборе другого отправителя

const emailLocked = computed(() => state.identityLevel === 'single')
const levelHint = computed(() => ({
  single: 'Адрес отправителя — только адрес вашего ящика. Можно завести несколько отправителей с разными именами и подписями.',
  domain: 'Адрес отправителя — любой адрес вашего домена. Сервер всё равно проверит, можно ли вам отправлять от него.',
  any: 'Адрес отправителя — любой. Сервер всё равно проверит, можно ли вам отправлять от него.',
}[state.identityLevel]))

function edit(i?: Identity) {
  error.value = ''
  editorKey.value++
  editing.value = i
    ? { ...i }
    : { id: 0, name: '', email: state.me?.mailbox ?? '', reply_to: '', bcc: '', signature: '', is_default: false }
}

async function save() {
  if (!editing.value) return
  saving.value = true
  error.value = ''
  try {
    const sig = editor.value?.isEmpty() ? '' : editor.value?.getHTML() ?? ''
    const saved = await api.saveIdentity({ ...editing.value, signature: sig })
    await loadIdentities()
    editing.value = null
    toast(`Сохранено: ${saved.name || saved.email}`)
  } catch (e) {
    error.value = e instanceof ApiError
      ? ({
          'identity.email_not_allowed': 'Этот адрес нельзя использовать как отправителя.',
          'identity.invalid_reply_to': 'Неверный адрес в поле «Ответ на».',
          'identity.invalid_bcc': 'Неверный адрес в поле «Скрытая копия».',
        } as Record<string, string>)[e.code] ?? e.detail ?? e.code
      : String(e)
  } finally {
    saving.value = false
  }
}

async function remove(i: Identity) {
  if (!confirm(`Удалить отправителя «${i.name || i.email}»?`)) return
  try {
    await api.deleteIdentity(i.id)
    await loadIdentities()
    if (editing.value?.id === i.id) editing.value = null
  } catch (e) {
    toast(e instanceof ApiError && e.code === 'identity.last' ? 'Нельзя удалить единственного отправителя' : 'Не удалось удалить')
  }
}
</script>

<template>
  <section>
    <h2>Отправители и подписи</h2>
    <p class="muted">{{ levelHint }}</p>

    <div class="list">
      <div v-for="i in state.identities" :key="i.id" class="row" :class="{ on: editing?.id === i.id }" @click="edit(i)">
        <div class="who">
          <strong>{{ i.name || i.email }}</strong>
          <span class="faint">{{ i.email }}</span>
        </div>
        <span v-if="i.is_default" class="badge"><Star :size="12" /> по умолчанию</span>
        <span v-if="i.signature" class="faint small">есть подпись</span>
        <button v-if="state.identities.length > 1" class="icon-btn" aria-label="Удалить" title="Удалить" @click.stop="remove(i)">
          <Trash2 :size="16" />
        </button>
      </div>
    </div>
    <button class="btn" @click="edit()"><Plus :size="15" /> Добавить отправителя</button>

    <form v-if="editing" class="card" @submit.prevent="save">
      <label>Имя <input v-model="editing.name" class="input" placeholder="Иван Петров" /></label>
      <label>Адрес
        <input v-model="editing.email" class="input" type="email" :readonly="emailLocked" required />
      </label>
      <label>Ответ на (Reply-To) <input v-model="editing.reply_to" class="input" placeholder="необязательно" /></label>
      <label>Скрытая копия каждому письму <input v-model="editing.bcc" class="input" placeholder="необязательно" /></label>
      <label class="check"><input v-model="editing.is_default" type="checkbox" /> Отправитель по умолчанию</label>
      <div class="sig-label">Подпись</div>
      <div class="sig-editor">
        <RichEditor :key="editorKey" ref="editor" :content="editing.signature ?? ''" placeholder="Например: С уважением, Иван" />
      </div>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div class="actions">
        <button class="btn primary" :disabled="saving">Сохранить</button>
        <button type="button" class="btn" @click="editing = null">Отмена</button>
      </div>
    </form>
  </section>
</template>

<style scoped>
.list { display: grid; gap: 6px; margin: 14px 0; }
.row { display: flex; align-items: center; gap: 10px; padding: 10px 12px; border: 1px solid var(--border); border-radius: 8px; background: var(--pane); cursor: pointer; }
.row:hover, .row.on { border-color: var(--accent); }
.who { flex: 1; display: grid; min-width: 0; }
.badge { display: inline-flex; align-items: center; gap: 4px; font-size: 12px; color: var(--accent); }
.small { font-size: 12px; }
.sig-label { color: var(--text-2); }
.sig-editor { display: flex; flex-direction: column; min-height: 160px; border: 1px solid var(--border); border-radius: 8px; overflow: hidden; background: var(--pane); }
</style>
