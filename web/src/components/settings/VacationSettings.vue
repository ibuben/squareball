<script setup lang="ts">
import { onMounted, ref } from 'vue'
import type { Forward, Vacation } from '../../api'
import { cloneRuleset, loadSieve, saveSieve, sieve } from '../../sieve'
import { t } from '../../i18n'

// Один компонент на два раздела: автоответ и пересылка — части того же скрипта.
const props = defineProps<{ kind: 'vacation' | 'forward' }>()

const vacation = ref<Vacation | null>(null)
const forward = ref<Forward | null>(null)
const error = ref('')

function reset() {
  const rs = cloneRuleset()
  vacation.value = rs.vacation
  forward.value = rs.forward
}
onMounted(async () => { await loadSieve(); reset() })

async function save() {
  const rs = cloneRuleset()
  if (props.kind === 'vacation') rs.vacation = vacation.value!
  else rs.forward = forward.value!
  error.value = await saveSieve({ ruleset: rs })
  if (!error.value) reset()
}
</script>

<template>
  <section>
    <h2>{{ t(kind === 'vacation' ? 'set.vacation' : 'set.forward') }}</h2>
    <p v-if="sieve.disabled" class="muted">{{ t(kind === 'vacation' ? 'vac.noSieveVacation' : 'vac.noSieveForward') }}</p>
    <p v-else-if="sieve.error" class="error">{{ sieve.error }}</p>
    <p v-else-if="sieve.state?.mode === 'raw'" class="muted">
      {{ t('vac.rawMode') }}
    </p>

    <form v-else-if="kind === 'vacation' && vacation" class="card" @submit.prevent="save">
      <p class="muted">{{ t('vac.hint') }}</p>
      <label class="check"><input v-model="vacation.enabled" type="checkbox" data-test="vacation-enabled" /> {{ t('vac.enabled') }}</label>
      <div class="line">
        <label>{{ t('vac.from') }} <input v-model="vacation.start" class="input" type="date" /></label>
        <label>{{ t('vac.to') }} <input v-model="vacation.end" class="input" type="date" /></label>
      </div>
      <label>{{ t('vac.subject') }} <input v-model="vacation.subject" class="input" :placeholder="t('vac.subjectPh')" /></label>
      <label>{{ t('vac.body') }}
        <textarea v-model="vacation.body" class="input area" rows="6" :required="vacation.enabled"
                  :placeholder="t('vac.bodyPh')" />
      </label>
      <label>{{ t('vac.every') }}
        <input v-model.number="vacation.days" class="input days" type="number" min="1" max="365" /> {{ t('vac.days') }}
      </label>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div class="actions"><button class="btn primary" :disabled="sieve.saving">{{ t('common.save') }}</button></div>
    </form>

    <form v-else-if="kind === 'forward' && forward" class="card" @submit.prevent="save">
      <p class="muted">{{ t('fwd.hint') }}</p>
      <label class="check"><input v-model="forward.enabled" type="checkbox" /> {{ t('fwd.enabled') }}</label>
      <label>{{ t('fwd.address') }} <input v-model="forward.to" class="input" type="email" :required="forward.enabled" placeholder="name@example.com" /></label>
      <label class="check"><input v-model="forward.keep_copy" type="checkbox" /> {{ t('fwd.keepCopy') }}</label>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div class="actions"><button class="btn primary" :disabled="sieve.saving">{{ t('common.save') }}</button></div>
    </form>
  </section>
</template>

<style scoped>
.line { display: flex; gap: 14px; flex-wrap: wrap; }
.area { height: auto; padding: 8px 10px; resize: vertical; }
.days { width: 80px; display: inline-block; margin: 0 6px; }
</style>
