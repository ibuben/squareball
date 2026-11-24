<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Check, Eye, EyeOff, X } from 'lucide-vue-next'
import { api, ApiError, type PasswordPolicy } from '../../api'
import { state, toast } from '../../store'

const policy = ref<PasswordPolicy | null>(null)
const current = ref('')
const next = ref('')
const repeat = ref('')
const show = ref(false)
const busy = ref(false)
const error = ref('')
const loadError = ref('')

onMounted(async () => {
  try { policy.value = (await api.passwordPolicy()).policy }
  catch (e) { loadError.value = e instanceof ApiError && e.status === 404 ? 'Смена пароля на этом сервере не настроена.' : 'Не удалось загрузить правила пароля.' }
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
  const out = [{ ok: len >= p.min_length && len <= p.max_length, text: `от ${p.min_length} до ${p.max_length} символов` }]
  if (p.require_classes > 0) {
    out.push({ ok: classes(pw) >= p.require_classes, text: `${p.require_classes} из 4 групп: строчные, заглавные, цифры, другие символы` })
  }
  if (p.forbid_mailbox && local.value.length >= 3) {
    out.push({ ok: !!pw && !pw.toLowerCase().includes(local.value), text: `не содержит имя ящика «${local.value}»` })
  }
  // eslint-disable-next-line no-control-regex
  const bad = (p.forbid_chars ?? '').split('').filter((c) => pw.includes(c)).length > 0 || /[\u0000-\u001f\u007f]/.test(pw)
  out.push({ ok: !!pw && !bad, text: p.forbid_chars ? `без символов ${[...p.forbid_chars].map((c) => `«${c}»`).join(' ')} и переводов строк` : 'без переводов строк и управляющих символов' })
  return out
})
const matches = computed(() => !!repeat.value && repeat.value === next.value)
const ready = computed(() => !!current.value && rules.value.every((r) => r.ok) && matches.value && next.value !== current.value)

const weak: Record<string, string> = {
  too_short: 'Пароль слишком короткий.',
  too_long: 'Пароль слишком длинный.',
  classes: 'Пароль слишком простой: добавьте символы других групп.',
  chars: 'Пароль содержит недопустимые символы.',
  mailbox: 'Пароль не должен содержать имя ящика.',
  same: 'Новый пароль совпадает с текущим.',
  breached: 'Этот пароль встречается в известных утечках. Выберите другой.',
  backend: 'Сервер учётных записей не принял новый пароль. Попробуйте другой.',
}

async function submit() {
  error.value = ''
  busy.value = true
  try {
    await api.changePassword(current.value, next.value)
    current.value = next.value = repeat.value = ''
    show.value = false
    toast('Пароль изменён. Другие сеансы этого ящика завершены.')
  } catch (e) {
    if (!(e instanceof ApiError)) error.value = 'Нет связи с сервером.'
    else if (e.code === 'password.current') error.value = 'Текущий пароль указан неверно.'
    else if (e.code === 'password.weak') error.value = weak[e.detail ?? ''] ?? 'Новый пароль не подходит.'
    else if (e.code === 'auth.locked') error.value = 'Слишком много попыток. Попробуйте позже.'
    else error.value = 'Сервис смены пароля сейчас недоступен. Попробуйте позже.'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section>
    <h2>Пароль</h2>
    <p v-if="loadError" class="muted">{{ loadError }}</p>
    <form v-else-if="policy" class="card" @submit.prevent="submit" autocomplete="on">
      <input type="text" name="username" autocomplete="username" :value="state.me?.mailbox" hidden readonly />
      <label>Текущий пароль
        <input v-model="current" class="input" :type="show ? 'text' : 'password'" autocomplete="current-password" required data-test="pw-current" />
      </label>
      <label>Новый пароль
        <span class="pw">
          <input v-model="next" class="input" :type="show ? 'text' : 'password'" autocomplete="new-password" required
                 :maxlength="policy.max_length" aria-describedby="pw-rules" data-test="pw-new" />
          <button type="button" class="btn icon" :aria-label="show ? 'Скрыть пароли' : 'Показать пароли'" :aria-pressed="show" @click="show = !show">
            <EyeOff v-if="show" :size="16" /><Eye v-else :size="16" />
          </button>
        </span>
      </label>
      <ul id="pw-rules" class="rules">
        <li v-for="r in rules" :key="r.text" :class="{ ok: r.ok }">
          <Check v-if="r.ok" :size="14" /><X v-else :size="14" /> {{ r.text }}
        </li>
      </ul>
      <label>Новый пароль ещё раз
        <input v-model="repeat" class="input" :type="show ? 'text' : 'password'" autocomplete="new-password" required data-test="pw-repeat" />
      </label>
      <p v-if="repeat && !matches" class="muted">Пароли не совпадают.</p>
      <p v-if="next && next === current" class="muted">Новый пароль совпадает с текущим.</p>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div class="actions">
        <button class="btn primary" :disabled="!ready || busy">{{ busy ? 'Меняем…' : 'Сменить пароль' }}</button>
      </div>
      <p class="muted small">После смены пароля другие сеансы этого ящика будут завершены. Обновите пароль и в почтовых программах на телефоне и компьютере.</p>
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
