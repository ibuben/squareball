<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Mail } from 'lucide-vue-next'
import { api, ApiError, type Brand } from '../api'
import { t } from '../i18n'
import { applyBrand, brandLogo, restoreBrand } from '../brand'
import { setMe, state } from '../store'

const username = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)
const brand = ref<Brand>(restoreBrand() ?? { name: '' })
const domain = ref('')

onMounted(async () => {
  try {
    const c = await api.config()
    brand.value = c.brand
    domain.value = c.default_domain
    applyBrand(c.brand)
    document.title = c.brand.name
  } catch { /* экран входа работает и без конфига */ }
})

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
    <form class="card" @submit.prevent="submit">
      <div class="logo">
        <img v-if="brandLogo(brand, state.dark)" :src="brandLogo(brand, state.dark)" alt="" />
        <Mail v-else :size="28" />
        <span>{{ brand.name }}</span>
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
.login { min-height: 100%; display: grid; place-items: center; padding: 16px; }
.card {
  width: 100%; max-width: 360px; background: var(--pane); border: 1px solid var(--border);
  border-radius: 12px; padding: 28px; display: grid; gap: 14px; box-shadow: var(--shadow);
}
.logo { display: flex; align-items: center; gap: 10px; color: var(--accent); font-weight: 600; font-size: 15px; }
.logo img { height: 28px; }
h1 { margin: 4px 0 6px; font-size: 20px; }
label { display: grid; gap: 6px; color: var(--text-2); }
.error { margin: 0; color: var(--danger); }
.submit { height: 36px; justify-content: center; margin-top: 4px; }
</style>
