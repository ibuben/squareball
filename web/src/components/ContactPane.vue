<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Check, Mail, PenSquare, UserPlus } from 'lucide-vue-next'
import { api } from '../api'
import { listDate, who } from '../format'
import { t } from '../i18n'
import Avatar from './Avatar.vue'
import { currentFolder, openCompose, openMessage, reportError, state, toast } from '../store'

// Сводка о собеседнике, как боковая панель в eM Client: карточка и переписка из текущей папки.
// В «Отправленных» и «Черновиках» собеседник — получатель, а не сам пользователь.
const outgoing = computed(() => ['sent', 'drafts'].includes(currentFolder()?.role ?? ''))
const person = computed(() => {
  const m = state.current?.message
  return outgoing.value ? m?.to[0] : m?.from[0]
})
const history = computed(() => {
  const email = person.value?.email.toLowerCase()
  if (!email) return []
  return state.messages
    .filter((m) => (outgoing.value ? m.to : m.from).some((a) => a.email.toLowerCase() === email))
    .slice(0, 12)
})

// Есть ли собеседник в адресной книге (проверяется при смене письма).
const inBook = ref<boolean | null>(null)
watch(() => person.value?.email, async (email) => {
  inBook.value = null
  if (!email) return
  try {
    const r = await api.contacts(email)
    if (person.value?.email === email) inBook.value = r.contacts.some((c) => c.emails.some((e) => e.value === email.toLowerCase()))
  } catch { /* не критично */ }
}, { immediate: true })

async function addToBook() {
  const p = person.value
  if (!p) return
  try {
    await api.saveContact({ name: p.name ?? '', emails: [{ value: p.email, label: 'work' }] })
    inBook.value = true
    toast('Добавлено в контакты')
  } catch (e) { reportError(e) }
}
</script>

<template>
  <aside class="contact">
    <template v-if="person">
      <div class="card">
        <Avatar :addr="person" :size="64" />
        <div class="name">{{ who(person) }}</div>
        <div class="faint email">{{ person.email }}</div>
        <div class="btns">
          <button class="btn" @click="openCompose({ to: [person] })"><PenSquare :size="14" /> {{ t('compose.new') }}</button>
          <button v-if="inBook === false" class="btn" @click="addToBook"><UserPlus :size="14" /> В контакты</button>
          <span v-else-if="inBook" class="faint in-book"><Check :size="14" /> в контактах</span>
        </div>
      </div>
      <div class="section">{{ t('contact.history') }}</div>
      <button v-for="m in history" :key="m.uid" class="hist" :class="{ on: m.uid === state.currentUID }"
              @click="openMessage(m.uid)">
        <Mail :size="13" class="faint" />
        <span class="ellipsis">{{ m.subject || t('list.nosubject') }}</span>
        <span class="faint d">{{ listDate(m.date) }}</span>
      </button>
    </template>
  </aside>
</template>

<style scoped>
.contact { background: var(--pane-alt); border-left: 1px solid var(--border); padding: 18px 12px; overflow: auto; min-width: 0; }
.card { display: grid; justify-items: center; gap: 6px; text-align: center; padding-bottom: 16px; border-bottom: 1px solid var(--border); }
.name { font-weight: 600; font-size: 14px; margin-top: 4px; word-break: break-word; }
.email { font-size: 12px; word-break: break-all; margin-bottom: 6px; }
.btns { display: flex; flex-wrap: wrap; justify-content: center; gap: 6px; }
.in-book { display: inline-flex; align-items: center; gap: 4px; font-size: 12px; }
.section { margin: 14px 4px 6px; font-size: 12px; font-weight: 600; color: var(--text-2); }
.hist { width: 100%; display: flex; align-items: center; gap: 8px; padding: 6px; border: 0; background: none; border-radius: 6px; text-align: left; }
.hist:hover, .hist.on { background: var(--hover); }
.hist .ellipsis { flex: 1; }
.d { font-size: 11px; flex: none; }
</style>
