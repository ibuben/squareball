<script setup lang="ts">
// Карточка события из письма-приглашения (text/calendar): когда, где, кто,
// и ответ организатору — «Принять», «Под вопросом», «Отклонить».
import { computed, ref, watch } from 'vue'
import { CalendarMinus, CalendarPlus, Check, CircleHelp, Clock, Download, MapPin, MessageSquare, Repeat, UserRound, Users, X } from 'lucide-vue-next'
import { api, ApiError, type Attendee, type Invite, type InviteView, type Message, type PartStat } from '../api'
import { who } from '../format'
import { dateLocale, t, tn, type Key } from '../i18n'
import { findSummary, reportError, state, toast } from '../store'

const props = defineProps<{ msg: Message; inv: Invite; view?: InviteView | null }>()

// Весь день и время без пояса показываем как написано, без пересчёта в местное.
const tz = computed(() => (props.inv.all_day || props.inv.floating ? 'UTC' : undefined))

const start = computed(() => new Date(props.inv.start))
const end = computed(() => {
  const e = new Date(props.inv.end)
  if (props.inv.all_day) e.setUTCDate(e.getUTCDate() - 1) // DTEND у событий на весь день — следующий день
  return e
})

const month = computed(() => start.value.toLocaleDateString(dateLocale(), { month: 'short', timeZone: tz.value }).replace('.', ''))
const day = computed(() => start.value.toLocaleDateString(dateLocale(), { day: 'numeric', timeZone: tz.value }))
const weekday = computed(() => start.value.toLocaleDateString(dateLocale(), { weekday: 'short', timeZone: tz.value }))

const when = computed(() => {
  const opt = { timeZone: tz.value }
  const date = (d: Date) => d.toLocaleDateString(dateLocale(), { weekday: 'short', day: 'numeric', month: 'long', year: 'numeric', ...opt })
  const time = (d: Date) => d.toLocaleTimeString(dateLocale(), { hour: '2-digit', minute: '2-digit', ...opt })
  const sameDay = date(start.value) === date(end.value)
  if (props.inv.all_day) return sameDay ? t('inv.allDay', { date: date(start.value) }) : `${date(start.value)} — ${date(end.value)}`
  if (sameDay) return `${date(start.value)}, ${time(start.value)}–${time(end.value)}`
  return `${date(start.value)}, ${time(start.value)} — ${date(end.value)}, ${time(end.value)}`
})

const cancelled = computed(() => props.inv.method === 'CANCEL' || props.inv.status === 'CANCELLED')

const PS = ['ACCEPTED', 'TENTATIVE', 'DECLINED', 'DELEGATED', 'NEEDS-ACTION']
const partLabel = (p?: string) => (p && PS.includes(p) ? t(`inv.ps.${p}` as Key) : p ?? '')
const replyVerb = (p?: string) => t((p && PS.slice(0, 4).includes(p) ? `inv.verb.${p}` : 'inv.verb.other') as Key)

const kind = computed(() => {
  const i = props.inv
  if (cancelled.value) return t(i.recurrence_id ? 'inv.kind.cancelOne' : 'inv.kind.cancelled')
  switch (i.method) {
    case 'REQUEST': return t(i.sequence ? 'inv.kind.updated' : 'inv.kind.request')
    case 'REPLY': {
      const a = i.attendees[0]
      return a ? `${who(a)} — ${replyVerb(a.partstat)}` : t('inv.kind.reply')
    }
    case 'COUNTER': return t('inv.kind.counter')
    case 'DECLINECOUNTER': return t('inv.kind.declineCounter')
    case 'REFRESH': return t('inv.kind.refresh')
    default: return t('inv.kind.event')
  }
})

// Участники: сначала организатор, затем обязательные, затем остальные.
const people = computed<Attendee[]>(() => {
  const org = props.inv.organizer?.email.toLowerCase()
  const rank = (a: Attendee) => (a.role === 'OPT-PARTICIPANT' ? 2 : a.role === 'NON-PARTICIPANT' ? 3 : 1)
  return props.inv.attendees.filter((a) => a.email.toLowerCase() !== org).sort((a, b) => rank(a) - rank(b))
})
const isSelf = (a: Attendee) => !!props.view && a.email.toLowerCase() === props.view.self.toLowerCase()
// Свой ответ в письме-приглашении не записан — берём его из пометки письма.
const ps = (a: Attendee) => (isSelf(a) && props.view?.replied ? props.view.replied : a.partstat)
const showAll = ref(false)
const shown = computed(() => (showAll.value ? people.value : people.value.slice(0, 6)))
const counts = computed(() => {
  const c = { ACCEPTED: 0, TENTATIVE: 0, DECLINED: 0 } as Record<string, number>
  for (const a of people.value) { const p = ps(a); if (p && p in c) c[p]++ }
  return c
})

const busy = ref<PartStat | ''>('')
const commenting = ref(false)
const comment = ref('')

async function answer(ps: PartStat) {
  if (busy.value) return
  busy.value = ps
  try {
    const r = await api.inviteReply(props.msg.folder, props.msg.uid, ps, comment.value.trim())
    if (state.current?.invite) state.current.invite.replied = ps
    const s = findSummary(props.msg.uid)
    if (s) s.answered = true
    commenting.value = false
    comment.value = ''
    toast(t('inv.replySent', { ps: partLabel(ps), to: r.to }))
  } catch (e) { reportError(e); return } finally { busy.value = '' }
  if (hasCalendar.value) await toCalendar(ps, true)
}

// Календарь: принятая встреча сохраняется, отклонённая или отменённая — убирается.
const hasCalendar = computed(() => !!state.me?.features.calendar)
const canSave = computed(() => hasCalendar.value && !['REPLY', 'COUNTER', 'DECLINECOUNTER', 'REFRESH'].includes(props.inv.method ?? ''))
const saving = ref(false)
async function toCalendar(ps: PartStat | '' = props.view?.replied ?? '', quiet = false) {
  if (saving.value) return
  saving.value = true
  try {
    const r = await api.inviteSave(props.msg.folder, props.msg.uid, ps)
    toast(t(r.saved ? 'inv.savedToCalendar' : 'inv.removedFromCalendar'))
  } catch (e) {
    if (e instanceof ApiError && e.code === 'calendar.none') { if (!quiet) toast(t('inv.noCalendar')) }
    else if (!quiet) reportError(e)
    else toast(t('inv.calendarFailed'))
  } finally { saving.value = false }
}

// Ответ участника на свою встречу записываем в календарь сразу при открытии
// письма: так же делают Gmail и Outlook. Повторное открытие ничего не меняет.
const replyNote = ref('')
watch(() => `${props.msg.folder}\u0000${props.msg.uid}`, async (key) => {
  replyNote.value = ''
  if (props.inv.method !== 'REPLY' || !props.view?.is_organizer || !hasCalendar.value) return
  try {
    await api.inviteSave(props.msg.folder, props.msg.uid)
    if (key === `${props.msg.folder}\u0000${props.msg.uid}`) replyNote.value = t('inv.replyRecorded')
  } catch (e) {
    if (e instanceof ApiError && e.code === 'calendar.event_not_found' && key === `${props.msg.folder}\u0000${props.msg.uid}`) {
      replyNote.value = t('inv.replyNotInCalendar')
    }
  }
}, { immediate: true })

const safeURL = computed(() => (/^https?:\/\//i.test(props.inv.url ?? '') ? props.inv.url : ''))
</script>

<template>
  <div class="invite" :class="{ cancelled }" data-test="invite-card">
    <div class="head">
      <div class="leaf" aria-hidden="true">
        <span class="m">{{ month }}</span><span class="d">{{ day }}</span><span class="w">{{ weekday }}</span>
      </div>
      <div class="title">
        <div class="kind">{{ kind }}</div>
        <div class="summary">{{ inv.summary || msg.subject || t('inv.untitled') }}</div>
      </div>
    </div>

    <dl class="rows">
      <div><dt><Clock :size="15" /></dt><dd>
        {{ when }}
        <span v-if="inv.recurring" class="tag"><Repeat :size="12" /> {{ t('inv.recurring') }}</span>
        <span v-else-if="inv.recurrence_id" class="tag"><Repeat :size="12" /> {{ t('inv.oneOccurrence') }}</span>
      </dd></div>
      <div v-if="inv.location || safeURL"><dt><MapPin :size="15" /></dt><dd>
        <span v-if="inv.location">{{ inv.location }}</span>
        <a v-if="safeURL" :href="safeURL" target="_blank" rel="noopener noreferrer" class="url">{{ safeURL }}</a>
      </dd></div>
      <div v-if="inv.organizer"><dt><UserRound :size="15" /></dt><dd>
        {{ who(inv.organizer) }} <span class="faint">&lt;{{ inv.organizer.email }}&gt;</span> <span class="faint">· {{ t('inv.organizer') }}</span>
      </dd></div>
      <div v-if="people.length && inv.method !== 'REPLY'"><dt><Users :size="15" /></dt><dd>
        <div class="faint count">
          {{ tn('inv.people', people.length) }}<template
            v-if="counts.ACCEPTED">{{ t('inv.accepted', { n: counts.ACCEPTED }) }}</template><template
            v-if="counts.TENTATIVE">{{ t('inv.tentative', { n: counts.TENTATIVE }) }}</template><template
            v-if="counts.DECLINED">{{ t('inv.declined', { n: counts.DECLINED }) }}</template>
        </div>
        <ul class="people">
          <li v-for="a in shown" :key="a.email" :title="`${a.email} — ${partLabel(ps(a))}`" :class="{ self: isSelf(a) }">
            <Check v-if="ps(a) === 'ACCEPTED'" :size="13" class="ps ok" />
            <CircleHelp v-else-if="ps(a) === 'TENTATIVE'" :size="13" class="ps maybe" />
            <X v-else-if="ps(a) === 'DECLINED'" :size="13" class="ps no" />
            <span v-else class="ps dot" />
            <span class="ellipsis">{{ who(a) }}</span>
            <span v-if="a.role === 'OPT-PARTICIPANT'" class="faint">{{ t('inv.optional') }}</span>
          </li>
          <li v-if="people.length > shown.length" class="more"><button class="link" @click="showAll = true">{{ t('inv.more', { n: people.length - shown.length }) }}</button></li>
        </ul>
      </dd></div>
      <div v-if="inv.method === 'REPLY' && inv.attendees[0]"><dt><Users :size="15" /></dt><dd>
        {{ who(inv.attendees[0]) }} <span class="faint">&lt;{{ inv.attendees[0].email }}&gt;</span>:
        <strong>{{ partLabel(inv.attendees[0].partstat) }}</strong>
      </dd></div>
    </dl>

    <div v-if="view?.can_reply" class="answer">
      <div class="buttons" role="group" :aria-label="t('inv.answer')">
        <button class="btn" :class="{ on: view.replied === 'ACCEPTED' }" :disabled="!!busy" data-test="rsvp-accept" @click="answer('ACCEPTED')">
          <Check :size="15" /> {{ t('inv.accept') }}</button>
        <button class="btn" :class="{ on: view.replied === 'TENTATIVE' }" :disabled="!!busy" data-test="rsvp-tentative" @click="answer('TENTATIVE')">
          <CircleHelp :size="15" /> {{ t('inv.maybe') }}</button>
        <button class="btn" :class="{ on: view.replied === 'DECLINED' }" :disabled="!!busy" data-test="rsvp-decline" @click="answer('DECLINED')">
          <X :size="15" /> {{ t('inv.decline') }}</button>
        <button v-if="!commenting" class="icon-btn" :title="t('inv.addComment')" :aria-label="t('inv.comment')" @click="commenting = true">
          <MessageSquare :size="16" /></button>
      </div>
      <input v-if="commenting" v-model="comment" class="input comment" maxlength="2000"
             :placeholder="t('inv.commentPh')" @keydown.esc="commenting = false">
      <div class="faint note">
        <template v-if="view.replied">{{ t('inv.youReplied', { ps: partLabel(view.replied) }) }}</template>
        <template v-else-if="!view.attendee">{{ t('inv.notListed', { self: view.self }) }}</template>
        <template v-else>{{ t('inv.toOrganizer') }}</template>
      </div>
    </div>
    <div v-else-if="view?.is_organizer && inv.method === 'REQUEST'" class="faint note">{{ t('inv.youOrganize') }}</div>
    <div v-if="replyNote" class="faint note" data-test="invite-reply-note">{{ replyNote }}</div>

    <div v-if="canSave" class="calendar">
      <button class="btn" :disabled="saving" data-test="invite-calendar" @click="toCalendar()">
        <template v-if="cancelled || view?.replied === 'DECLINED'"><CalendarMinus :size="15" /> {{ t('inv.removeFromCalendar') }}</template>
        <template v-else><CalendarPlus :size="15" /> {{ t('inv.toCalendar') }}</template>
      </button>
    </div>

    <a class="ics" :href="api.partURL(msg.folder, msg.uid, inv.part, true)"><Download :size="14" /> {{ t('inv.ics') }}</a>
  </div>
</template>

<style scoped>
.invite {
  margin-top: 14px; padding: 14px 16px; border: 1px solid var(--border); border-left: 3px solid var(--accent);
  border-radius: 10px; background: var(--pane-alt); display: grid; gap: 12px; position: relative;
}
.invite.cancelled { border-left-color: var(--danger); }
.invite.cancelled .summary { text-decoration: line-through; color: var(--text-2); }
.head { display: flex; gap: 14px; align-items: center; padding-right: 96px; }
.leaf {
  flex: none; width: 52px; display: grid; justify-items: center; border-radius: 8px; overflow: hidden;
  background: var(--pane); border: 1px solid var(--border); line-height: 1.1; padding-bottom: 4px;
}
.leaf .m { width: 100%; text-align: center; background: var(--accent); color: var(--accent-contrast); font-size: 11px;
  font-weight: 700; text-transform: uppercase; padding: 2px 0; }
.cancelled .leaf .m { background: var(--danger); }
.leaf .d { font-size: 20px; font-weight: 700; margin-top: 2px; }
.leaf .w { font-size: 11px; color: var(--text-3); }
.title { min-width: 0; }
.kind { font-size: 12px; color: var(--text-2); }
.cancelled .kind { color: var(--danger); font-weight: 600; }
.summary { font-size: 16px; font-weight: 600; overflow-wrap: anywhere; }
.rows { margin: 0; display: grid; gap: 6px; }
.rows > div { display: flex; gap: 10px; align-items: flex-start; }
.rows dt { flex: none; color: var(--text-3); padding-top: 2px; }
.rows dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
.tag { display: inline-flex; align-items: center; gap: 3px; margin-left: 6px; font-size: 12px; color: var(--text-2);
  border: 1px solid var(--border); border-radius: 10px; padding: 0 7px; }
.url { display: block; }
.count { font-size: 12px; }
.people { list-style: none; margin: 4px 0 0; padding: 0; display: flex; flex-wrap: wrap; gap: 4px 14px; }
.people li { display: inline-flex; align-items: center; gap: 5px; max-width: 240px; }
.people li.self { font-weight: 600; }
.ps { flex: none; }
.ps.ok { color: var(--success); }
.ps.maybe { color: #c98a00; }
.ps.no { color: var(--danger); }
.ps.dot { width: 7px; height: 7px; border-radius: 50%; border: 1.5px solid var(--text-3); margin: 0 3px; }
.link { background: none; border: 0; padding: 0; color: var(--accent); cursor: pointer; font: inherit; }
.answer { display: grid; gap: 8px; }
.buttons { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
.buttons .btn.on { background: var(--accent); border-color: var(--accent); color: var(--accent-contrast); }
.comment { width: 100%; }
.calendar { display: flex; }
.note { font-size: 12px; }
.ics { position: absolute; top: 12px; right: 14px; display: inline-flex; gap: 4px; align-items: center; font-size: 12px;
  color: var(--text-2); text-decoration: none; }
.ics:hover { color: var(--accent); }
@media (max-width: 520px) { .ics { position: static; } .head { padding-right: 0; } }
</style>
