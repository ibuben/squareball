<script setup lang="ts">
import { onMounted, ref } from 'vue'
import type { Forward, Vacation } from '../../api'
import { cloneRuleset, loadSieve, saveSieve, sieve } from '../../sieve'

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
    <h2>{{ kind === 'vacation' ? 'Автоответ' : 'Пересылка' }}</h2>
    <p v-if="sieve.disabled" class="muted">На сервере не включён ManageSieve — {{ kind === 'vacation' ? 'автоответ' : 'пересылка' }} недоступны.</p>
    <p v-else-if="sieve.error" class="error">{{ sieve.error }}</p>
    <p v-else-if="sieve.state?.mode === 'raw'" class="muted">
      Фильтры открыты как текст (раздел «Фильтры»), поэтому эту настройку нельзя изменить формой.
    </p>

    <form v-else-if="kind === 'vacation' && vacation" class="card" @submit.prevent="save">
      <p class="muted">Сервер ответит отправителю один раз за указанное число дней, даже когда почта закрыта.</p>
      <label class="check"><input v-model="vacation.enabled" type="checkbox" data-test="vacation-enabled" /> Автоответ включён</label>
      <div class="line">
        <label>С <input v-model="vacation.start" class="input" type="date" /></label>
        <label>по <input v-model="vacation.end" class="input" type="date" /></label>
      </div>
      <label>Тема <input v-model="vacation.subject" class="input" placeholder="Я в отпуске" /></label>
      <label>Текст ответа
        <textarea v-model="vacation.body" class="input area" rows="6" :required="vacation.enabled"
                  placeholder="Здравствуйте! Я в отпуске до 10 декабря. По срочным вопросам пишите коллегам." />
      </label>
      <label>Отвечать одному отправителю не чаще, чем раз в
        <input v-model.number="vacation.days" class="input days" type="number" min="1" max="365" /> дн.
      </label>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div class="actions"><button class="btn primary" :disabled="sieve.saving">Сохранить</button></div>
    </form>

    <form v-else-if="kind === 'forward' && forward" class="card" @submit.prevent="save">
      <p class="muted">Все входящие письма будут пересылаться на указанный адрес.</p>
      <label class="check"><input v-model="forward.enabled" type="checkbox" /> Пересылка включена</label>
      <label>Адрес <input v-model="forward.to" class="input" type="email" :required="forward.enabled" placeholder="name@example.com" /></label>
      <label class="check"><input v-model="forward.keep_copy" type="checkbox" /> Оставлять копию в этом ящике</label>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div class="actions"><button class="btn primary" :disabled="sieve.saving">Сохранить</button></div>
    </form>
  </section>
</template>

<style scoped>
.line { display: flex; gap: 14px; flex-wrap: wrap; }
.area { height: auto; padding: 8px 10px; resize: vertical; }
.days { width: 80px; display: inline-block; margin: 0 6px; }
</style>
