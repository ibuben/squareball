<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { ChevronDown, ChevronUp, Maximize2, Minimize2, Minus, Paperclip, Send, Trash2, X } from 'lucide-vue-next'
import { api, ApiError, type Address, type Attachment, type MsgRef } from '../api'
import { size } from '../format'
import { t } from '../i18n'
import AddressInput from './AddressInput.vue'
import RichEditor from './RichEditor.vue'
import { closeCompose, loadFolders, loadList, state, toast, unsaved, type ComposeInit } from '../store'

const props = defineProps<{ id: number; init: ComposeInit; index: number }>()

const to = ref<Address[]>(props.init.to ?? [])
const cc = ref<Address[]>(props.init.cc ?? [])
const bcc = ref<Address[]>(props.init.bcc ?? [])
const showCc = ref(cc.value.length > 0 || bcc.value.length > 0)
const subject = ref(props.init.subject ?? '')
const files = ref<File[]>([])                              // ещё не сохранённые на сервере
const existing = ref<Attachment[]>(props.init.existing ?? []) // уже лежат в черновике
const draftRef = ref<MsgRef | undefined>(props.init.draftRef)
const editor = ref<InstanceType<typeof RichEditor>>()
// Цитата исходного письма живёт отдельно от редактора и вставляется при сохранении
// без изменений, внутри <div data-wm-quote> — так не теряется вёрстка оригинала.
const quote = ref(props.init.quote ?? '')
const quoteOpen = ref(false)
const quoteFrame = ref<HTMLIFrameElement>()
const quoteDoc = computed(() => `<!doctype html><html><head><meta charset="utf-8"><base target="_blank">
<style>html,body{margin:0;background:#fff;color:#1d2330}body{font:13px/1.5 -apple-system,"Segoe UI",Roboto,Arial,sans-serif;padding:8px 12px}
img{max-width:100%;height:auto}blockquote{margin:8px 0;padding-left:12px;border-left:3px solid #c9d2e3;color:#4b5565}</style>
</head><body>${quote.value}</body></html>`)
function fitQuote() {
  const f = quoteFrame.value
  if (f?.contentDocument?.documentElement) f.style.height = `${Math.min(f.contentDocument.documentElement.scrollHeight, 400)}px`
}
const sending = ref(false)
const minimized = ref(false)
const maximized = ref(false)
const dragging = ref(false)

// Автосохранение: версия правок против сохранённой версии.
const version = ref(0)
const savedVersion = ref(0)
const saving = ref(false)
const savedAt = ref<Date | null>(null)
const saveError = ref(false)
const dirty = computed(() => version.value !== savedVersion.value)
const AUTOSAVE_DELAY = 4000
let saveTimer = 0

const totalSize = computed(() => files.value.reduce((s, f) => s + f.size, 0) + existing.value.reduce((s, a) => s + a.size, 0))
const status = computed(() => {
  if (saving.value) return t('compose.saving')
  if (saveError.value) return t('compose.saveFailed')
  if (savedAt.value) return `${t('compose.saved')} ${savedAt.value.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`
  return ''
})

function touch() {
  version.value++
  unsaved.add(props.id)
  clearTimeout(saveTimer)
  saveTimer = window.setTimeout(() => void save(), AUTOSAVE_DELAY)
}

onMounted(() => {
  // Есть получатель (ответ) — курсор сразу в текст; иначе фокус останется на «Кому».
  if (to.value.length) void nextTick(() => editor.value?.focusStart())
})
onBeforeUnmount(() => { clearTimeout(saveTimer); unsaved.delete(props.id) })

function addFiles(list: FileList | File[] | null | undefined) {
  const arr = Array.from(list ?? [])
  if (!arr.length) return
  files.value = [...files.value, ...arr]
  touch()
}

function bodyHTML(): string {
  const text = editor.value?.getHTML() ?? ''
  return quote.value ? `${text}<div data-wm-quote="1">${quote.value}</div>` : text
}
function removeQuote() { quote.value = ''; touch() }
function removeFile(i: number) { files.value.splice(i, 1); touch() }
function removeExisting(i: number) { existing.value.splice(i, 1); touch() }

function buildForm(): FormData {
  const form = new FormData()
  form.append('draft', JSON.stringify({
    to: to.value, cc: cc.value, bcc: bcc.value, subject: subject.value,
    html: bodyHTML(), in_reply_to: props.init.inReplyTo ?? '',
    references: props.init.references ?? [], source: props.init.source ?? null,
    draft_ref: draftRef.value ?? null, keep_attachments: existing.value.map((a) => a.index),
  }))
  for (const f of files.value) form.append('attachments', f, f.name)
  return form
}

let savePromise: Promise<void> | null = null

// save сохраняет черновик; параллельные вызовы ждут текущий и при необходимости повторяют.
async function save(): Promise<void> {
  clearTimeout(saveTimer)
  if (savePromise) { await savePromise; if (!dirty.value) return }
  if (!dirty.value || sending.value) return
  const v = version.value
  saving.value = true
  savePromise = (async () => {
    try {
      const { draft_ref } = await api.saveDraft(buildForm())
      draftRef.value = draft_ref
      // Загруженные файлы теперь лежат в черновике: берём их список оттуда.
      const view = await api.message(draft_ref.folder, draft_ref.uid, false, true)
      existing.value = view.message.attachments.filter((a) => !a.inline || !a.content_id)
      files.value = []
      savedVersion.value = v
      savedAt.value = new Date()
      saveError.value = false
      if (!dirty.value) unsaved.delete(props.id)
      if (state.folder === draft_ref.folder) void loadList()
    } catch (e) {
      saveError.value = true
      console.error(e)
    } finally {
      saving.value = false
      savePromise = null
    }
  })()
  await savePromise
  // Правки, сделанные во время сохранения, сохраняем следующим заходом.
  if (dirty.value && !saveError.value) saveTimer = window.setTimeout(() => void save(), AUTOSAVE_DELAY)
}

async function send() {
  if (sending.value) return
  if (!to.value.length && !cc.value.length && !bcc.value.length) { toast(t('compose.noRecipients')); return }
  if (state.me && totalSize.value > state.me.limits.max_upload) { toast(t('compose.tooLarge')); return }
  clearTimeout(saveTimer)
  if (savePromise) await savePromise
  sending.value = true
  try {
    await api.send(buildForm())
    toast(t('compose.sent'))
    unsaved.delete(props.id)
    closeCompose(props.id)
    void loadFolders()
    if (draftRef.value && state.folder === draftRef.value.folder) void loadList()
  } catch (e) {
    toast(e instanceof ApiError && e.detail ? e.detail : t('toast.error'))
  } finally {
    sending.value = false
  }
}

// Закрытие сохраняет черновик (как в десктопных клиентах), а не спрашивает об удалении.
async function close() {
  const hasContent = to.value.length || subject.value || files.value.length || existing.value.length ||
    !(editor.value?.isEmpty() ?? true)
  if (dirty.value && hasContent) {
    await save()
    if (saveError.value) return // окно остаётся, чтобы не потерять текст
    toast(t('compose.draftSaved'))
    void loadFolders()
  }
  unsaved.delete(props.id)
  closeCompose(props.id)
}

async function discard() {
  clearTimeout(saveTimer)
  if (savePromise) await savePromise
  if (draftRef.value) {
    try { await api.discardDraft(draftRef.value) } catch (e) { console.error(e) }
    if (state.folder === draftRef.value.folder) void loadList()
    void loadFolders()
  }
  toast(t('compose.draftDeleted'))
  unsaved.delete(props.id)
  closeCompose(props.id)
}
</script>

<template>
  <div class="compose" :class="{ min: minimized, max: maximized }" :style="{ right: `${16 + index * 24}px`, bottom: `${index * 24}px` }"
       role="dialog" :aria-label="t('compose.new')" @keydown.ctrl.enter="send" @keydown.meta.enter="send">
    <header @dblclick="maximized = !maximized">
      <span class="ellipsis">{{ subject || t('compose.new') }}</span>
      <button class="icon-btn" @click="minimized = !minimized"><Minus :size="15" /></button>
      <button class="icon-btn" @click="maximized = !maximized; minimized = false">
        <component :is="maximized ? Minimize2 : Maximize2" :size="14" />
      </button>
      <button class="icon-btn" :aria-label="'close'" @click="close"><X :size="16" /></button>
    </header>
    <template v-if="!minimized">
      <AddressInput v-model="to" :label="t('compose.to')" @update:model-value="touch" />
      <button v-if="!showCc" type="button" class="cc-toggle" @click="showCc = true">{{ t('compose.cc') }} / {{ t('compose.bcc') }}</button>
      <template v-if="showCc">
        <AddressInput v-model="cc" :label="t('compose.cc')" @update:model-value="touch" />
        <AddressInput v-model="bcc" :label="t('compose.bcc')" @update:model-value="touch" />
      </template>
      <div class="field">
        <span class="label">{{ t('compose.subject') }}</span>
        <input v-model="subject" @input="touch" />
      </div>
      <div class="body" :class="{ drag: dragging }"
           @dragover.prevent="dragging = true" @dragleave.self="dragging = false" @drop="dragging = false">
        <RichEditor ref="editor" :content="init.html ?? ''" :placeholder="t('compose.placeholder')" @change="touch" @files="addFiles" @submit="send" />
        <div v-if="quote" class="quote">
          <div class="quote-bar">
            <button type="button" class="link-btn" @click="quoteOpen = !quoteOpen">
              <component :is="quoteOpen ? ChevronUp : ChevronDown" :size="14" />
              {{ quoteOpen ? t('compose.hideQuote') : t('compose.showQuote') }}
            </button>
            <button type="button" class="link-btn" @click="removeQuote">{{ t('compose.removeQuote') }}</button>
          </div>
          <iframe v-if="quoteOpen" ref="quoteFrame" class="quote-frame" title="quote" :srcdoc="quoteDoc"
                  sandbox="allow-same-origin allow-popups" @load="fitQuote" />
        </div>
      </div>
      <div v-if="files.length || existing.length" class="files">
        <span v-for="(a, i) in existing" :key="'e' + a.index" class="file">
          <Paperclip :size="13" /> <span class="ellipsis">{{ a.filename }}</span> <span class="faint">{{ size(a.size) }}</span>
          <button class="icon-btn x" @click="removeExisting(i)"><X :size="13" /></button>
        </span>
        <span v-for="(f, i) in files" :key="'n' + i" class="file">
          <Paperclip :size="13" /> <span class="ellipsis">{{ f.name }}</span> <span class="faint">{{ size(f.size) }}</span>
          <button class="icon-btn x" @click="removeFile(i)"><X :size="13" /></button>
        </span>
      </div>
      <footer>
        <button class="btn primary" :disabled="sending" @click="send"><Send :size="15" /> {{ t('compose.send') }}</button>
        <label class="icon-btn" :title="t('compose.attach')">
          <Paperclip :size="17" />
          <input type="file" multiple hidden @change="addFiles(($event.target as HTMLInputElement).files)" />
        </label>
        <span class="status faint" :class="{ err: saveError }" data-draft-status>{{ status }}</span>
        <button class="icon-btn" :title="t('compose.discard')" @click="discard"><Trash2 :size="16" /></button>
      </footer>
    </template>
  </div>
</template>

<style scoped>
.compose {
  position: fixed; z-index: 30; width: min(560px, calc(100vw - 32px)); height: min(560px, calc(100vh - 80px));
  display: flex; flex-direction: column; background: var(--pane); border: 1px solid var(--border);
  border-radius: 10px 10px 0 0; box-shadow: var(--shadow); overflow: hidden;
}
.compose.min { height: auto; width: 300px; }
.compose.max { inset: 40px 6vw 0 6vw !important; width: auto; height: auto; }
header { display: flex; align-items: center; gap: 2px; padding: 6px 6px 6px 14px; background: var(--pane-alt); border-bottom: 1px solid var(--border); font-weight: 600; cursor: default; }
header .ellipsis { flex: 1; }
.cc-toggle { align-self: flex-end; margin: -30px 10px 0 0; height: 26px; border: 0; background: none; color: var(--text-2); position: relative; z-index: 1; }
.field { display: flex; align-items: center; gap: 8px; height: 36px; padding: 0 12px; border-bottom: 1px solid var(--border); }
.field .label { color: var(--text-3); width: 56px; flex: none; }
.field input { flex: 1; border: 0; outline: none; background: transparent; height: 32px; }
.body { flex: 1; min-height: 0; display: flex; flex-direction: column; overflow: auto; }
.body.drag { background: var(--accent-soft); }
.quote { padding: 0 14px 10px; }
.quote-bar { display: flex; gap: 14px; }
.link-btn { display: inline-flex; align-items: center; gap: 4px; padding: 2px 0; border: 0; background: none; color: var(--text-2); font-size: 12px; }
.link-btn:hover { color: var(--accent); }
.quote-frame { display: block; width: 100%; height: 120px; margin-top: 6px; border: 1px solid var(--border); border-radius: 6px; background: #fff; }
.files { display: flex; flex-wrap: wrap; gap: 6px; padding: 8px 12px; border-top: 1px solid var(--border); }
.file { display: inline-flex; align-items: center; gap: 6px; max-width: 240px; padding: 2px 2px 2px 8px; border-radius: 6px; background: var(--pane-alt); border: 1px solid var(--border); }
.file .x { width: 22px; height: 22px; }
footer { display: flex; align-items: center; gap: 8px; padding: 10px 12px; border-top: 1px solid var(--border); }
.status { margin-left: auto; font-size: 12px; }
.status.err { color: var(--danger); }
@media (max-width: 760px) {
  .compose, .compose.max { inset: 0 !important; width: auto; height: auto; border-radius: 0; }
}
</style>
