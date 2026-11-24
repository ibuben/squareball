<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Check, Eye, EyeOff, X } from 'lucide-vue-next'
import { api, ApiError, type PasswordPolicy } from '../../api'
import { state, toast } from '../../store'
import { t, type Key } from '../../i18n'

const policy = ref<PasswordPolicy | null>(null)
const current = ref('')
const next = ref('')
const repeat = ref('')
const show = ref(false)
const busy = ref(false)
const error = ref('')
const loadError = ref('')
// Сразу после входа из панели (SSO) текущий пароль не спрашивается.
const needCurrent = ref(true)

onMounted(async () => {
  try {
    const r = await api.passwordPolicy()
    policy.value = r.policy
    needCurrent.value = r.current_required !== false
  }
  catch (e) { loadError.value = e instanceof ApiError && e.status === 404 ? t('pw.notConfigured') : t('pw.loadFailed') }
})

function classes(pw: string) {
  let n = 0
  if (/\p{Ll}/u.test(pw)) n++
  if (/\p{Lu}/u.test(pw)) n++
  if (/\p{Nd}/u.test(pw)) n++
  if (/[^\p{Ll}\p{Lu}\p{Nd}]/u.test(pw)) n++
  return n
}
const local = computed(() => (state.me?.mailbox ?? '').split('@')[0].toLowerCase())

// Те же правила, что проверяет сервер (internal/password/policy.go).
const rules = computed(() => {
  const p = policy.value
  if (!p) return []
  const pw = next.value
  const len = [...pw].length
  const out = [{ ok: len >= p.min_length && len <= p.max_length, text: t('pw.ruleLength', { min: p.min_length, max: p.max_length }) }]
  if (p.require_classes > 0) {
    out.push({ ok: classes(pw) >= p.require_classes, text: t('pw.ruleClasses', { n: p.require_classes }) })
  }
  if (p.forbid_mailbox && local.value.length >= 3) {
    out.push({ ok: !!pw && !pw.toLowerCase().includes(local.value), text: t('pw.ruleMailbox', { name: local.value }) })
  }
  // eslint-disable-next-line no-control-regex
  const bad = (p.forbid_chars ?? '').split('').filter((c) => pw.includes(c)).length > 0 || /[\u0000-\u001f\u007f]/.test(pw)
  out.push({ ok: !!pw && !bad, text: p.forbid_chars ? t('pw.ruleChars', { chars: [...p.forbid_chars].map((c) => `«${c}»`).join(' ') }) : t('pw.ruleNoControl') })
  return out
})
const matches = computed(() => !!repeat.value && repeat.value === next.value)
const ready = computed(() => (!needCurrent.value || !!current.value) && rules.value.every((r) => r.ok) && matches.value && next.value !== current.value)

const WEAK = ['too_short', 'too_long', 'classes', 'chars', 'mailbox', 'same', 'breached', 'backend']
const weak = (reason: string) => (WEAK.includes(reason) ? t(`pw.weak.${reason}` as Key) : t('pw.weakGeneric'))

async function submit() {
  error.value = ''
  busy.value = true
  try {
    await api.changePassword(needCurrent.value ? current.value : '', next.value)
    current.value = next.value = repeat.value = ''
    show.value = false
    toast(t('pw.changed'))
  } catch (e) {
    if (!(e instanceof ApiError)) error.value = t('pw.noConnection')
    else if (e.code === 'password.current') error.value = t('pw.wrongCurrent')
    else if (e.code === 'password.weak') error.value = weak(e.detail ?? '')
    else if (e.code === 'auth.locked') error.value = t('pw.locked')
    else error.value = t('pw.unavailable')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section>
    <h2>{{ t('set.password') }}</h2>
    <p v-if="loadError" class="muted">{{ loadError }}</p>
    <form v-else-if="policy" class="card" @submit.prevent="submit" autocomplete="on">
      <input type="text" name="username" autocomplete="username" :value="state.me?.mailbox" hidden readonly />
      <label v-if="needCurrent">{{ t('pw.current') }}
        <input v-model="current" class="input" :type="show ? 'text' : 'password'" autocomplete="current-password" required data-test="pw-current" />
      </label>
      <label>{{ t('pw.new') }}
        <span class="pw">
          <input v-model="next" class="input" :type="show ? 'text' : 'password'" autocomplete="new-password" required
                 :maxlength="policy.max_length" aria-describedby="pw-rules" data-test="pw-new" />
          <button type="button" class="btn icon" :aria-label="t(show ? 'pw.hide' : 'pw.show')" :aria-pressed="show" @click="show = !show">
            <EyeOff v-if="show" :size="16" /><Eye v-else :size="16" />
          </button>
        </span>
      </label>
      <ul id="pw-rules" class="rules">
        <li v-for="r in rules" :key="r.text" :class="{ ok: r.ok }">
          <Check v-if="r.ok" :size="14" /><X v-else :size="14" /> {{ r.text }}
        </li>
      </ul>
      <label>{{ t('pw.repeat') }}
        <input v-model="repeat" class="input" :type="show ? 'text' : 'password'" autocomplete="new-password" required data-test="pw-repeat" />
      </label>
      <p v-if="repeat && !matches" class="muted">{{ t('pw.mismatch') }}</p>
      <p v-if="!needCurrent" class="muted small" data-test="pw-sso">{{ t('pw.sso') }}</p>
      <p v-if="needCurrent && next && next === current" class="muted">{{ t('pw.weak.same') }}</p>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div class="actions">
        <button class="btn primary" :disabled="!ready || busy">{{ t(busy ? 'pw.changing' : 'pw.change') }}</button>
      </div>
      <p class="muted small">{{ t('pw.afterHint') }}</p>
    </form>
  </section>
</template>

<style scoped>
.pw { display: flex; gap: 6px; }
.pw .input { flex: 1; min-width: 0; }
.rules { list-style: none; margin: -4px 0 0; padding: 0; display: grid; gap: 4px; color: var(--text-3); font-size: 13px; }
.rules li { display: flex; align-items: center; gap: 6px; }
.rules li.ok { color: var(--success); }
form p { margin: 0; }
.small { font-size: 12px; }
</style>
