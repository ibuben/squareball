<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import {
  Archive, ArrowLeft, Code, Download, FileArchive, Flag, Forward, ImageOff, MailOpen, Paperclip, Pencil, Reply, ReplyAll, ShieldAlert, Trash2,
} from 'lucide-vue-next'
import { api, type Address } from '../api'
import { escapeHTML, fullAddr, fullDate, replySubject, size, who, whoList } from '../format'
import { t } from '../i18n'
import Avatar from './Avatar.vue'
import {
  archiveSelected, currentFolder, deleteSelected, moveSelected, openCompose, openDraft, openMessage, roleFolder, setFlag, state,
} from '../store'

defineEmits<{ back: [] }>()

const frame = ref<HTMLIFrameElement>()
const msg = computed(() => state.current?.message)
const summary = computed(() => state.messages.find((m) => m.uid === state.currentUID))
const isDraft = computed(() => currentFolder()?.role === 'drafts')
const files = computed(() => (msg.value?.attachments ?? []).filter((a) => !a.inline || !a.content_id))

const srcdoc = computed(() => {
  if (!state.current) return ''
  // Письмо — в iframe sandbox без allow-scripts. <base target=_blank> открывает ссылки во вкладке.
  return `<!doctype html><html><head><meta charset="utf-8"><base target="_blank">
<style>
  html,body{margin:0;background:#fff;color:#1d2330}
  body{font:14px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Arial,sans-serif;padding:14px 16px 18px;overflow-wrap:anywhere}
  img{max-width:100%;height:auto} table{max-width:100%}
  blockquote{margin:8px 0;padding-left:12px;border-left:3px solid #c9d2e3;color:#4b5565}
  pre{white-space:pre-wrap}
</style></head><body>${state.current.body_html}</body></html>`
})

function fit() {
  const f = frame.value
  const doc = f?.contentDocument
  if (!f || !doc?.body) return
  f.style.height = `${doc.documentElement.scrollHeight}px`
}
function onLoad() {
  fit()
  const doc = frame.value?.contentDocument
  if (doc) {
    new ResizeObserver(fit).observe(doc.documentElement)
    doc.querySelectorAll('img').forEach((img) => img.addEventListener('load', fit))
  }
}
watch(srcdoc, () => nextTick(fit))

const me = () => state.me?.mailbox.toLowerCase() ?? ''
const notMe = (list: Address[] = []) => list.filter((a) => a.email.toLowerCase() !== me())

function quoteHTML(): string {
  const m = msg.value!
  const head = `${escapeHTML(fullDate(m.date))}, ${escapeHTML(fullAddr(m.from[0] ?? { email: '' }))} ${t('compose.wrote')}:`
  return `<p>${head}</p><blockquote type="cite">${state.current!.body_html}</blockquote>`
}

function reply(all: boolean) {
  const m = msg.value
  if (!m) return
  const primary = m.reply_to?.length ? m.reply_to : m.from
  const to = all ? notMe([...primary, ...m.to]) : primary
  openCompose({
    to: to.length ? to : primary,
    cc: all ? notMe(m.cc) : [],
    subject: replySubject(m.subject, 'Re'),
    quote: quoteHTML(),
    inReplyTo: m.message_id,
    references: m.references,
    source: { folder: m.folder, uid: m.uid, mode: 'reply' },
  })
}

function forward() {
  const m = msg.value
  if (!m) return
  const head = [
    `-------- ${t('compose.forwarded')} --------`,
    `From: ${escapeHTML(whoList(m.from))}`,
    `Date: ${escapeHTML(fullDate(m.date))}`,
    `Subject: ${escapeHTML(m.subject)}`,
    `To: ${escapeHTML(whoList(m.to))}`,
  ].join('<br>')
  openCompose({
    subject: replySubject(m.subject, 'Fwd'),
    quote: `<p>${head}</p>${state.current!.body_html}`,
    source: { folder: m.folder, uid: m.uid, mode: 'forward' },
  })
}

const archive = () => void archiveSelected()
const junk = () => { const j = roleFolder('junk'); if (j) void moveSelected(j.name) }

defineExpose({ reply, forward })
</script>

<template>
  <section class="reading" aria-live="polite">
    <div v-if="!msg" class="empty">
      <MailOpen :size="44" />
      <p>{{ state.loadingMessage ? '…' : t('read.empty') }}</p>
      <p class="faint hint">{{ t('help.shortcuts') }}</p>
    </div>
    <template v-else>
      <div class="toolbar">
        <button class="icon-btn back" aria-label="back" @click="$emit('back')"><ArrowLeft :size="18" /></button>
        <button v-if="isDraft" class="btn primary" :aria-label="t('read.continue')" :title="t('read.continue')" @click="openDraft(msg.uid)"><Pencil :size="15" /> <span class="lbl">{{ t('read.continue') }}</span></button>
        <template v-else>
        <button class="btn" :aria-label="t('read.reply')" :title="t('read.reply')" @click="reply(false)"><Reply :size="15" /> <span class="lbl">{{ t('read.reply') }}</span></button>
        <button class="btn" :aria-label="t('read.replyAll')" :title="t('read.replyAll')" @click="reply(true)"><ReplyAll :size="15" /> <span class="lbl">{{ t('read.replyAll') }}</span></button>
        <button class="btn" :aria-label="t('read.forward')" :title="t('read.forward')" @click="forward"><Forward :size="15" /> <span class="lbl">{{ t('read.forward') }}</span></button>
        </template>
        <span class="spacer" />
        <button v-if="roleFolder('archive')" class="icon-btn" :title="t('read.archive')" @click="archive"><Archive :size="17" /></button>
        <button v-if="roleFolder('junk')" class="icon-btn" :title="t('read.junk')" @click="junk"><ShieldAlert :size="17" /></button>
        <button class="icon-btn" :title="t('read.delete')" :aria-label="t('read.delete')" @click="deleteSelected"><Trash2 :size="17" /></button>
        <button class="icon-btn" :class="{ active: summary?.flagged }" :title="t('read.flag')" :aria-label="t('read.flag')"
                @click="setFlag([msg.uid], '\\Flagged', !summary?.flagged)"><Flag :size="17" /></button>
        <button class="icon-btn" :title="t('read.unread')" :aria-label="t('read.unread')" @click="setFlag([msg.uid], '\\Seen', false)"><MailOpen :size="17" /></button>
        <a class="icon-btn" :title="t('read.source')" :aria-label="t('read.source')" :href="api.rawURL(msg.folder, msg.uid)"><Code :size="17" /></a>
      </div>

      <div class="scroll content">
        <h1 class="subject">{{ msg.subject || t('list.nosubject') }}</h1>
        <div class="from">
          <Avatar :addr="msg.from[0]" :size="40" />
          <div class="who">
            <div><strong>{{ who(msg.from[0]) }}</strong> <span class="faint">&lt;{{ msg.from[0]?.email }}&gt;</span></div>
            <div class="muted ellipsis">{{ t('read.to') }}: {{ whoList(msg.to) }}</div>
            <div v-if="msg.cc?.length" class="muted ellipsis">{{ t('read.cc') }}: {{ whoList(msg.cc) }}</div>
          </div>
          <span class="faint date">{{ fullDate(msg.date) }}</span>
        </div>

        <div v-if="state.current!.blocked_images" class="banner">
          <ImageOff :size="16" /> {{ t('read.images') }}
          <button class="btn" @click="openMessage(msg.uid, true)">{{ t('read.showImages') }}</button>
        </div>

        <div v-if="files.length" class="files">
          <a v-for="a in files" :key="a.index" class="file" :href="api.partURL(msg.folder, msg.uid, a.index, true)">
            <Paperclip :size="15" />
            <span class="ellipsis">{{ a.filename }}</span>
            <span class="faint">{{ size(a.size) }}</span>
            <Download :size="14" class="faint" />
          </a>
          <a v-if="files.length > 1" class="file zip" data-test="zip-all"
             :href="api.zipURL(msg.folder, msg.uid, msg.subject || 'attachments')">
            <FileArchive :size="15" />
            <span>Все вложения ZIP</span>
            <Download :size="14" class="faint" />
          </a>
        </div>

        <iframe ref="frame" class="body" title="message" :srcdoc="srcdoc"
                sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox" @load="onLoad" />
      </div>
    </template>
  </section>
</template>

<style scoped>
.reading { display: flex; flex-direction: column; min-width: 0; background: var(--pane); container-type: inline-size; }
.empty { margin: auto; text-align: center; color: var(--text-3); padding: 24px; }
.hint { max-width: 320px; font-size: 12px; }
.toolbar { display: flex; align-items: center; gap: 6px; padding: 8px 12px; border-bottom: 1px solid var(--border); }
@container (max-width: 620px) { .toolbar .lbl { display: none; } .toolbar .btn { padding: 0 8px; } }
.spacer { flex: 1; }
.back { display: none; }
.content { flex: 1; padding: 16px 22px 24px; }
.subject { margin: 0 0 14px; font-size: 19px; line-height: 1.3; }
.from { display: flex; gap: 12px; align-items: flex-start; }
.who { flex: 1; min-width: 0; display: grid; gap: 2px; }
.date { flex: none; font-size: 12px; }
.banner {
  margin: 14px 0 0; display: flex; align-items: center; gap: 10px; padding: 8px 12px;
  border-radius: 8px; background: var(--pane-alt); border: 1px solid var(--border); color: var(--text-2);
}
.banner .btn { margin-left: auto; height: 26px; }
.files { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 14px; }
.file {
  display: inline-flex; align-items: center; gap: 8px; max-width: 280px; padding: 6px 10px;
  border: 1px solid var(--border); border-radius: 8px; color: var(--text); text-decoration: none; background: var(--pane-alt);
}
.file:hover { border-color: var(--accent); }
.file.zip { color: var(--accent); border-style: dashed; }
.body { display: block; width: 100%; min-height: 80px; border: 0; margin-top: 16px; border-radius: 8px; background: #fff; }
:root:not([data-theme='dark']) .body { margin-left: -16px; width: calc(100% + 32px); }
@media (max-width: 760px) {
  .back { display: inline-flex; }
  .toolbar .btn { padding: 0 8px; }
}
</style>
