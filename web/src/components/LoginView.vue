<script setup lang="ts">
import { computed, ref } from 'vue'
import { Globe, Mail } from 'lucide-vue-next'
import { api, ApiError } from '../api'
import { availableLangs, lang, LANGS, setLang, t, type Lang } from '../i18n'
import { applyBrand, brandLogo, restoreBrand } from '../brand'
import { setMe, state } from '../store'
import ThemeToggle from './ThemeToggle.vue'

const username = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)
// Бренд прошлого визита — пока не пришёл конфиг (его загружает boot).
const saved = restoreBrand()
const brand = computed(() => state.config?.brand ?? saved ?? { name: '' })
const domain = computed(() => state.config?.default_domain ?? '')
const langs = computed(() => availableLangs(state.config?.languages))
if (state.config) {
  applyBrand(state.config.brand)
  document.title = state.config.brand.name
}

async function submit() {
  if (busy.value) return
  error.value = ''
  busy.value = true
  try {
    setMe(await api.login(username.value, password.value))
  } catch (e) {
    const code = e instanceof ApiError ? e.code : ''
    error.value = code === 'auth.locked' ? t('login.locked')
      : code === 'mail.unavailable' ? t('login.unavailable') : t('login.failed')
    password.value = ''
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="login">
    <div class="tools">
      <label v-if="langs.length > 1" class="lang" :title="t('login.language')">
        <Globe :size="16" aria-hidden="true" />
        <select :value="lang()" :aria-label="t('login.language')" data-test="login-lang"
                @change="setLang(($event.target as HTMLSelectElement).value as Lang)">
          <option v-for="l in langs" :key="l" :value="l">{{ LANGS[l] }}</option>
        </select>
      </label>
      <ThemeToggle :size="18" />
    </div>
    <form class="card" @submit.prevent="submit">
      <div class="logo">
        <img v-if="brandLogo(brand, state.dark)" :src="brandLogo(brand, state.dark)" :alt="brand.name || ''" />
        <Mail v-else :size="48" aria-hidden="true" />
      </div>
      <h1>{{ t('login.title') }}</h1>
      <label>
        {{ t('login.username') }}
        <input v-model="username" class="input" autocomplete="username" autofocus required
               :placeholder="domain ? `name@${domain}` : 'name@example.com'" />
      </label>
      <label>
        {{ t('login.password') }}
        <input v-model="password" class="input" type="password" autocomplete="current-password" required />
      </label>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <button class="btn primary submit" :disabled="busy">{{ t('login.submit') }}</button>
    </form>
  </main>
</template>

<style scoped>
.login { position: relative; min-height: 100%; display: grid; place-items: center; padding: 56px 16px 16px; }
.tools { position: absolute; top: 12px; right: 12px; display: flex; align-items: center; gap: 6px; }
.lang {
  display: flex; align-items: center; gap: 6px; height: 32px; padding: 0 4px 0 10px; color: var(--text-2);
  border: 1px solid var(--border); border-radius: 16px; background: var(--pane);
}
.lang select { height: 30px; border: 0; background: transparent; color: var(--text); font: inherit; cursor: pointer; outline: none; }
.lang:focus-within { border-color: var(--accent); }
.card {
  width: 100%; max-width: 360px; background: var(--pane); border: 1px solid var(--border);
  border-radius: 12px; padding: 28px; display: grid; gap: 14px; box-shadow: var(--shadow);
}
.logo { display: flex; justify-content: center; align-items: center; color: var(--accent); margin-bottom: 4px; }
.logo img { height: 56px; max-width: 100%; object-fit: contain; }
h1 { margin: 4px 0 6px; font-size: 20px; text-align: center; }
.card label { display: grid; gap: 6px; color: var(--text-2); }
.error { margin: 0; color: var(--danger); }
.submit { height: 36px; justify-content: center; margin-top: 4px; }
</style>
