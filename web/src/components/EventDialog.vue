<script setup lang="ts">
// Окно события: создание, правка и удаление. У повторяющегося события правка
// меняет всю серию; удалить можно одно вхождение или всю серию. Участникам
// своей встречи сервер рассылает приглашения, изменения и отмену (iTIP).
import { computed, onMounted, ref } from 'vue'
import { Check, CircleHelp, Repeat as RepeatIcon, Trash2, UserRound, X } from 'lucide-vue-next'
import { api, ApiError, type Address, type Attendee, type CalendarInfo, type EventForm, type MeetingNotify } from '../api'
import { isEmail, who } from '../format'
import AddressInput from './AddressInput.vue'
import {
  addDays, browserTZ, dateValue, fromInputs, isoLocal, REPEATS, repeatOf, timeValue, type Occ, type Repeat,
} from '../calendar'
import { t, type Key } from '../i18n'
import { reportError, toast } from '../store'

const props = defineProps<{
  calendars: CalendarInfo[]
  occ?: Occ            // открытое вхождение
  start?: Date         // новое событие: начало
  allDay?: boolean
}>()
const emit = defineEmits<{ close: []; saved: [] }>()

const loading = ref(!!props.occ)
const busy = ref(false)
const writable = computed(() => props.calendars.filter((c) => !c.readonly))
const cal = ref(props.occ?.cal ?? writable.value[0]?.id ?? '')
const readonly = computed(() => !!props.occ && (props.occ.readonly || !!props.calendars.find((c) => c.id === cal.value)?.readonly))
const href = ref(props.occ?.href ?? '')
const etag = ref(props.occ?.etag ?? '')

const summary = ref('')
const location = ref('')
const description = ref('')
const allDay = ref(!!props.allDay)
const startDate = ref('')
const startTime = ref('')
const endDate = ref('')
const endTime = ref('')
const repeat = ref<Repeat>('none')
const customRule = ref('')
const recurring = ref(false)
const uid = ref('')
const tz = ref(browserTZ())

const attendees = ref<Address[]>([])
const statuses = ref<Record<string, string>>({}) // адрес в нижнем регистре → PARTSTAT
const loadedPeople = ref<Attendee[]>([])
const organizer = ref<Attendee | null>(null)
const foreign = ref(false) // встречу организует не ящик
const notify = ref(true)
const hadAttendees = computed(() => loadedPeople.value.length > 0)
const showNotify = computed(() => !foreign.value && (attendees.value.length > 0 || hadAttendees.value))
const psOf = (a: Address) => statuses.value[a.email.toLowerCase()]
const answered = computed(() => attendees.value.filter((a) => psOf(a)))

function fill(s: Date, e: Date) {
  startDate.value = dateValue(s); startTime.value = timeValue(s)
  endDate.value = dateValue(e); endTime.value = timeValue(e)
}

onMounted(async () => {
  if (!props.occ) {
    const s = props.start ?? new Date()
    fill(s, allDay.value ? s : new Date(s.getTime() + 3600_000))
    return
  }
  try {
    const r = await api.calendarEvent(props.occ.cal, props.occ.href)
    const ev = r.event
    etag.value = r.etag
    summary.value = ev.summary
    location.value = ev.location ?? ''
    description.value = ev.description ?? ''
    allDay.value = !!ev.all_day
    uid.value = ev.uid ?? ''
    if (ev.tz) tz.value = ev.tz
    recurring.value = !!ev.recurring
    repeat.value = repeatOf(ev.rrule)
    customRule.value = ev.rrule ?? ''
    foreign.value = r.foreign
    organizer.value = ev.organizer ?? null
    loadedPeople.value = ev.attendees ?? []
    attendees.value = loadedPeople.value.map((a) => ({ name: a.name, email: a.email }))
    statuses.value = Object.fromEntries(loadedPeople.value.map((a) => [a.email.toLowerCase(), a.partstat ?? '']))
    if (ev.all_day) {
      // Даты — как записаны, без пересчёта поясов; конец в форме — включительно.
      const s = fromInputs(ev.start.slice(0, 10))
      const e = addDays(fromInputs(ev.end.slice(0, 10)), -1)
      fill(s, e < s ? s : e)
    } else {
      fill(new Date(ev.start), new Date(ev.end))
    }
  } catch (e) {
    reportError(e)
    emit('close')
  } finally { loading.value = false }
})

const err = ref('')

function form(): EventForm | null {
  let s: Date, e: Date
  if (allDay.value) {
    s = fromInputs(startDate.value)
    e = addDays(fromInputs(endDate.value), 1)
  } else {
    s = fromInputs(startDate.value, startTime.value)
    e = fromInputs(endDate.value, endTime.value)
  }
  if (!startDate.value || !endDate.value || Number.isNaN(s.getTime()) || Number.isNaN(e.getTime()) || e < s) {
    err.value = t('ev.badTime')
    return null
  }
  const rrule = repeat.value === 'custom' ? customRule.value : REPEATS[repeat.value]
  if (attendees.value.some((a) => !isEmail(a.email))) {
    err.value = t('ev.badAttendee')
    return null
  }
  return {
    uid: uid.value || undefined,
    summary: summary.value.trim(),
    location: location.value.trim(),
    description: description.value.trim(),
    all_day: allDay.value,
    // Весь день: даты в UTC, чтобы сервер не сдвинул их поясом.
    start: allDay.value ? `${dateValue(s)}T00:00:00Z` : isoLocal(s),
    end: allDay.value ? `${dateValue(e)}T00:00:00Z` : isoLocal(e),
    tz: tz.value,
    rrule,
    // У чужой встречи участников не передаём: сервер их не меняет.
    attendees: foreign.value ? undefined : attendees.value.map((a) => ({ name: a.name, email: a.email })),
  }
}

const NOTIFY_ERR: Record<string, Key> = { 'send.rate_limited': 'ev.notifyRate', 'send.too_many_recipients': 'ev.notifyTooMany' }
function report(r: MeetingNotify, plain: Key, withSent: Key) {
  if (r.notify_error) toast(t(NOTIFY_ERR[r.notify_error] ?? 'ev.notifyFailed'))
  else if (r.sent) toast(t(withSent, { n: r.sent }))
  else toast(t(plain))
}

const CODES: Record<string, Key> = {
  'calendar.conflict': 'ev.conflict',
  'calendar.readonly': 'ev.readonly',
  'calendar.invalid': 'ev.invalid',
}
function fail(e: unknown) {
  if (e instanceof ApiError && CODES[e.code]) err.value = t(CODES[e.code])
  else if (e instanceof ApiError && e.code.startsWith('calendar.remote_')) err.value = t('cal.err.unavailable')
  else reportError(e)
}

async function save() {
  err.value = ''
  const f = form()
  if (!f || busy.value) return
  busy.value = true
  try {
    const r = await api.saveEvent(cal.value, f, href.value, etag.value, showNotify.value && notify.value)
    report(r, 'ev.saved', 'ev.sent')
    emit('saved')
  } catch (e) { fail(e) } finally { busy.value = false }
}

async function remove(one: boolean) {
  if (!props.occ || busy.value) return
  busy.value = true
  err.value = ''
  try {
    const r = await api.deleteEvent(props.occ.cal, props.occ.href, one ? '' : etag.value, uid.value || props.occ.uid,
      one ? props.occ.recurrence_id : '', !foreign.value && hadAttendees.value && notify.value)
    report(r, 'ev.deleted', 'ev.deletedSent')
    emit('saved')
  } catch (e) { fail(e) } finally { busy.value = false }
}

const title = computed(() => (props.occ ? (readonly.value ? t('ev.view') : t('ev.edit')) : t('ev.new')))
const repeatKeys: Repeat[] = ['none', 'daily', 'weekdays', 'weekly', 'monthly', 'yearly']
</script>

<template>
  <div class="overlay" @mousedown.self="emit('close')">
    <form class="dlg" role="dialog" aria-modal="true" aria-labelledby="ev-title" data-test="event-dialog"
          @keydown.esc="emit('close')" @submit.prevent="save">
      <header>
        <h2 id="ev-title">{{ title }}</h2>
        <button type="button" class="icon-btn" :aria-label="t('common.close')" @click="emit('close')"><X :size="18" /></button>
      </header>
      <p v-if="loading" class="faint">{{ t('common.loading') }}</p>
      <fieldset v-else :disabled="readonly || busy">
        <input v-model="summary" class="input title" maxlength="1000" :placeholder="t('ev.title')" :aria-label="t('ev.title')"
               data-test="event-title" autofocus>
        <label class="check"><input v-model="allDay" type="checkbox"> {{ t('ev.allDay') }}</label>
        <div class="when" :class="{ dateonly: allDay }">
          <span class="lbl">{{ t('ev.start') }}</span>
          <input v-model="startDate" type="date" class="input" required :aria-label="t('ev.start')" data-test="event-start-date">
          <input v-if="!allDay" v-model="startTime" type="time" class="input" required :aria-label="t('ev.startTime')">
          <span class="lbl">{{ t('ev.end') }}</span>
          <input v-model="endDate" type="date" class="input" required :aria-label="t('ev.end')">
          <input v-if="!allDay" v-model="endTime" type="time" class="input" required :aria-label="t('ev.endTime')">
        </div>
        <label class="row">
          <span class="lbl"><RepeatIcon :size="14" /> {{ t('ev.repeat') }}</span>
          <select v-model="repeat" class="input">
            <option v-for="k in repeatKeys" :key="k" :value="k">{{ t(`ev.rep.${k}` as Key) }}</option>
            <option v-if="customRule && repeatOf(customRule) === 'custom'" value="custom">{{ t('ev.rep.custom') }}</option>
          </select>
        </label>
        <p v-if="recurring && occ" class="faint note">{{ t('ev.seriesNote') }}</p>
        <label class="row">
          <span class="lbl">{{ t('ev.location') }}</span>
          <input v-model="location" class="input" maxlength="1000">
        </label>
        <div v-if="!foreign" class="people" data-test="event-attendees">
          <AddressInput v-model="attendees" :label="t('ev.attendees')" />
        </div>
        <ul v-if="!foreign && answered.length" class="ps-list">
          <li v-for="a in answered" :key="a.email" :title="a.email">
            <Check v-if="psOf(a) === 'ACCEPTED'" :size="13" class="ps ok" />
            <CircleHelp v-else-if="psOf(a) === 'TENTATIVE'" :size="13" class="ps maybe" />
            <X v-else-if="psOf(a) === 'DECLINED'" :size="13" class="ps no" />
            <span v-else class="ps dot" />
            <span class="ellipsis">{{ who(a) }}</span>
            <span class="faint">{{ t(`inv.ps.${psOf(a)}` as Key) }}</span>
          </li>
        </ul>
        <template v-if="foreign">
          <div v-if="organizer" class="row">
            <span class="lbl"><UserRound :size="14" /> {{ t('ev.organizer') }}</span>
            <span>{{ who(organizer) }} <span class="faint">&lt;{{ organizer.email }}&gt;</span></span>
          </div>
          <div v-if="loadedPeople.length" class="row top">
            <span class="lbl">{{ t('ev.attendees') }}</span>
            <ul class="ps-list flat">
              <li v-for="a in loadedPeople" :key="a.email" :title="a.email">
                <Check v-if="a.partstat === 'ACCEPTED'" :size="13" class="ps ok" />
                <CircleHelp v-else-if="a.partstat === 'TENTATIVE'" :size="13" class="ps maybe" />
                <X v-else-if="a.partstat === 'DECLINED'" :size="13" class="ps no" />
                <span v-else class="ps dot" />
                <span class="ellipsis">{{ who(a) }}</span>
              </li>
            </ul>
          </div>
          <p class="faint note">{{ t('ev.foreignNote', { who: organizer ? who(organizer) : '' }) }}</p>
        </template>
        <label v-if="showNotify" class="check" data-test="event-notify">
          <input v-model="notify" type="checkbox">
          {{ hadAttendees && !attendees.length ? t('ev.notifyCancel') : t('ev.notify') }}
        </label>
        <label class="row top">
          <span class="lbl">{{ t('ev.description') }}</span>
          <textarea v-model="description" class="input area" rows="4" maxlength="20000" />
        </label>
        <label v-if="!occ && writable.length > 1" class="row">
          <span class="lbl">{{ t('ev.calendar') }}</span>
          <select v-model="cal" class="input">
            <option v-for="c in writable" :key="c.id" :value="c.id">{{ c.name }}</option>
          </select>
        </label>
      </fieldset>
      <p v-if="readonly && !loading" class="faint note">{{ t('ev.readonly') }}</p>
      <p v-if="err" class="error" role="alert">{{ err }}</p>
      <footer v-if="!loading">
        <template v-if="occ && !readonly">
          <button v-if="occ.recurrence_id" type="button" class="btn danger" :disabled="busy" @click="remove(true)">
            <Trash2 :size="14" /> {{ t('ev.deleteOne') }}</button>
          <button type="button" class="btn danger" :disabled="busy" data-test="event-delete" @click="remove(false)">
            <Trash2 :size="14" /> {{ occ.recurrence_id ? t('ev.deleteAll') : t('common.delete') }}</button>
        </template>
        <span class="spacer" />
        <button type="button" class="btn" @click="emit('close')">{{ readonly ? t('common.close') : t('common.cancel') }}</button>
        <button v-if="!readonly" type="submit" class="btn primary" :disabled="busy || !cal" data-test="event-save">{{ t('common.save') }}</button>
      </footer>
    </form>
  </div>
</template>

<style scoped>
.overlay { position: fixed; inset: 0; z-index: 80; display: grid; place-items: center; padding: 16px; background: rgb(0 0 0 / 35%); }
.dlg {
  width: min(520px, 100%); max-height: 100%; overflow: auto; padding: 18px 20px; display: grid; gap: 12px;
  background: var(--pane); border: 1px solid var(--border); border-radius: 12px; box-shadow: var(--shadow);
}
header { display: flex; align-items: center; justify-content: space-between; }
h2 { margin: 0; font-size: 17px; }
fieldset { border: 0; margin: 0; padding: 0; display: grid; gap: 10px; min-width: 0; }
.title { font-size: 15px; height: 38px; }
.check { display: inline-flex; align-items: center; gap: 6px; }
.when { display: grid; grid-template-columns: 80px minmax(0, 1fr) 110px; gap: 8px; align-items: center; }
.when.dateonly { grid-template-columns: 80px minmax(0, 1fr); }
.row { display: grid; grid-template-columns: 80px minmax(0, 1fr); gap: 8px; align-items: center; }
.row.top { align-items: start; }
.row.top .lbl { padding-top: 8px; }
.lbl { color: var(--text-2); display: inline-flex; align-items: center; gap: 4px; }
.area { height: auto; padding: 8px 10px; resize: vertical; }
.note { margin: 0; font-size: 12px; }
.error { margin: 0; color: var(--danger); }
footer { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.spacer { flex: 1; }
.btn.danger { color: var(--danger); }
.people :deep(.field) { padding: 2px 10px 2px 0; border: 0; }
.people :deep(.label) { width: 80px; color: var(--text-2); }
.people :deep(.chips) { border: 1px solid var(--border); border-radius: 6px; padding: 2px 6px; background: var(--pane); }
.people :deep(.chips:focus-within) { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
.people :deep(.suggest) { left: 88px; }
.ps-list { list-style: none; margin: -4px 0 0 88px; padding: 0; display: flex; flex-wrap: wrap; gap: 4px 14px; font-size: 13px; }
.ps-list.flat { margin: 0; }
.ps-list li { display: inline-flex; align-items: center; gap: 5px; max-width: 260px; }
.ps { flex: none; }
.ps.ok { color: var(--success); }
.ps.maybe { color: #c98a00; }
.ps.no { color: var(--danger); }
.ps.dot { width: 7px; height: 7px; border-radius: 50%; border: 1.5px solid var(--text-3); margin: 0 3px; }
@media (max-width: 480px) {
  .when { grid-template-columns: minmax(0, 1fr) 100px; }
  .when .lbl { grid-column: 1 / -1; }
  .when.dateonly { grid-template-columns: minmax(0, 1fr); }
  .row { grid-template-columns: 1fr; }
  .people :deep(.field) { flex-wrap: wrap; }
  .people :deep(.suggest) { left: 0; }
  .ps-list { margin-left: 0; }
}
</style>
