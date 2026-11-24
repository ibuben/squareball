<script setup lang="ts">
import { computed } from 'vue'
import { Mail, PenSquare } from 'lucide-vue-next'
import { listDate, who } from '../format'
import { t } from '../i18n'
import Avatar from './Avatar.vue'
import { openCompose, openMessage, state } from '../store'

// Сводка о собеседнике, как боковая панель в eM Client: карточка и переписка из текущей папки.
const person = computed(() => state.current?.message.from[0])
const history = computed(() => {
  const email = person.value?.email.toLowerCase()
  if (!email) return []
  return state.messages.filter((m) => m.from.some((a) => a.email.toLowerCase() === email)).slice(0, 12)
})
</script>

<template>
  <aside class="contact">
    <template v-if="person">
      <div class="card">
        <Avatar :addr="person" :size="64" />
        <div class="name">{{ who(person) }}</div>
        <div class="faint email">{{ person.email }}</div>
        <button class="btn" @click="openCompose({ to: [person] })"><PenSquare :size="14" /> {{ t('compose.new') }}</button>
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
.section { margin: 14px 4px 6px; font-size: 12px; font-weight: 600; color: var(--text-2); }
.hist { width: 100%; display: flex; align-items: center; gap: 8px; padding: 6px; border: 0; background: none; border-radius: 6px; text-align: left; }
.hist:hover, .hist.on { background: var(--hover); }
.hist .ellipsis { flex: 1; }
.d { font-size: 11px; flex: none; }
</style>
