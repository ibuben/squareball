<script setup lang="ts">
// Окно «О программе». Скрывается брендингом: ui.brand.hide_about.
import { onMounted, ref } from 'vue'
import { ExternalLink, Mail, X } from 'lucide-vue-next'
import { t } from '../i18n'
import { state } from '../store'

const emit = defineEmits<{ close: [] }>()
const closeBtn = ref<HTMLButtonElement>()
onMounted(() => closeBtn.value?.focus())

const VENDOR = { name: 'eXUnity LAB', url: 'https://exunity.uz' }
const REPO = 'https://github.com/ibuben/squareball'
const LICENSE = 'https://opensource.org/license/mit'
</script>

<template>
  <div class="overlay" @mousedown.self="emit('close')">
    <div class="about" role="dialog" aria-modal="true" aria-labelledby="about-title" data-test="about-dialog"
         @keydown.esc="emit('close')">
      <button ref="closeBtn" type="button" class="icon-btn close" :aria-label="t('common.close')" @click="emit('close')">
        <X :size="18" />
      </button>
      <div class="mark" aria-hidden="true"><Mail :size="30" /></div>
      <h2 id="about-title">SquareBall</h2>
      <p class="faint version">{{ t('about.version', { v: state.me?.version || 'dev' }) }}</p>
      <p class="lead">{{ t('about.tagline') }}</p>
      <p>
        {{ t('about.developedBy') }}
        <a :href="VENDOR.url" target="_blank" rel="noopener noreferrer">{{ VENDOR.name }}</a>
        © 2026
      </p>
      <dl>
        <dt>{{ t('about.repository') }}</dt>
        <dd><a :href="REPO" target="_blank" rel="noopener noreferrer">github.com/ibuben/squareball <ExternalLink :size="12" /></a></dd>
        <dt>{{ t('about.license') }}</dt>
        <dd><a :href="LICENSE" target="_blank" rel="noopener noreferrer">MIT <ExternalLink :size="12" /></a></dd>
        <dt>{{ t('about.built') }}</dt>
        <dd>Go · Vue 3 · TipTap · Lucide</dd>
      </dl>
    </div>
  </div>
</template>

<style scoped>
.overlay { position: fixed; inset: 0; z-index: 80; display: grid; place-items: center; padding: 16px; background: rgb(0 0 0 / 35%); }
.about {
  position: relative; width: min(420px, 100%); padding: 28px 28px 22px; text-align: center;
  background: var(--pane); border: 1px solid var(--border); border-radius: 12px; box-shadow: var(--shadow);
}
.close { position: absolute; top: 10px; right: 10px; }
.mark {
  width: 56px; height: 56px; margin: 0 auto 10px; display: grid; place-items: center; border-radius: 14px;
  background: var(--accent-soft); color: var(--accent);
}
h2 { margin: 0; font-size: 20px; }
.version { margin: 2px 0 12px; font-size: 12px; }
.lead { margin: 0 0 10px; color: var(--text-2); }
p { margin: 0 0 14px; }
a { color: var(--accent); text-decoration: none; }
a:hover { text-decoration: underline; }
dl { display: grid; grid-template-columns: auto 1fr; gap: 6px 14px; margin: 0; padding-top: 14px; border-top: 1px solid var(--border); text-align: left; font-size: 13px; }
dt { color: var(--text-2); }
dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
dd a { display: inline-flex; align-items: center; gap: 4px; }
</style>
