// Общее состояние фильтров для разделов «Фильтры», «Автоответ» и «Пересылка»:
// все три — части одного Sieve-скрипта и сохраняются вместе.
import { reactive } from 'vue'
import { api, ApiError, type Ruleset, type SieveState } from './api'
import { toast } from './store'
import { t } from './i18n'

export const sieve = reactive({
  loading: false,
  saving: false,
  disabled: false, // на сервере не включён ManageSieve
  state: null as SieveState | null,
  error: '',
})

export function emptyRuleset(): Ruleset {
  return {
    rules: [],
    vacation: { enabled: false, subject: '', body: '', days: 7 },
    forward: { enabled: false, to: '', keep_copy: true },
  }
}

export async function loadSieve(force = false) {
  if (sieve.loading || (sieve.state && !force)) return
  sieve.loading = true
  sieve.error = ''
  try {
    sieve.state = await api.sieve()
    if (sieve.state.mode === 'rules' && !sieve.state.ruleset) sieve.state.ruleset = emptyRuleset()
  } catch (e) {
    if (e instanceof ApiError && e.code === 'sieve.disabled') sieve.disabled = true
    else sieve.error = e instanceof ApiError ? e.detail || e.code : String(e)
  } finally {
    sieve.loading = false
  }
}

// saveSieve сохраняет правила (или текст скрипта). Ошибку сервера возвращает
// строкой, чтобы показать её рядом с формой.
export async function saveSieve(body: { ruleset?: Ruleset; raw?: string }): Promise<string> {
  if (!sieve.state) return 'not loaded'
  sieve.saving = true
  try {
    sieve.state = await api.putSieve({
      mode: body.raw !== undefined ? 'raw' : 'rules',
      tag: sieve.state.tag,
      ruleset: body.ruleset, raw: body.raw,
    })
    toast(t('toast.saved'))
    return ''
  } catch (e) {
    if (e instanceof ApiError && e.code === 'sieve.conflict') {
      await loadSieve(true)
      return t('sieve.conflict')
    }
    return e instanceof ApiError ? e.detail || e.code : String(e)
  } finally {
    sieve.saving = false
  }
}

// Копия набора правил для редактирования в форме.
export function cloneRuleset(): Ruleset {
  return JSON.parse(JSON.stringify(sieve.state?.ruleset ?? emptyRuleset()))
}
