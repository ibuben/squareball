<script setup lang="ts">
import { defineAsyncComponent, onMounted } from 'vue'
import { setUnauthorizedHandler } from './api'
import { t } from './i18n'
import LoginView from './components/LoginView.vue'
import MailView from './components/MailView.vue'
// Окно письма с редактором (TipTap/ProseMirror) грузится отдельным чанком при первом открытии.
const ComposeWindow = defineAsyncComponent(() => import('./components/ComposeWindow.vue'))
import { applyTheme, boot, setMe, state, toast, unsaved } from './store'

setUnauthorizedHandler(() => {
  if (state.me) toast(t('toast.sessionExpired'))
  state.composes = []
  setMe(null)
})

// Несохранённый черновик — предупреждение при закрытии вкладки.
window.addEventListener('beforeunload', (e) => { if (unsaved.size) e.preventDefault() })

onMounted(() => {
  applyTheme()
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', applyTheme)
  void boot()
})
</script>

<template>
  <template v-if="!state.booting">
    <MailView v-if="state.me" />
    <LoginView v-else />
  </template>
  <ComposeWindow v-for="(c, i) in state.composes" :key="c.id" :id="c.id" :init="c.init" :index="i" />
  <div class="toasts" aria-live="polite">
    <div v-for="x in state.toasts" :key="x.id" class="toast">
      {{ x.text }}
      <button v-if="x.action" @click="x.action.run()">{{ x.action.label }}</button>
    </div>
  </div>
</template>
