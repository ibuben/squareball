// Package calendar — события iCalendar (RFC 5545) без сети: разворот
// повторений в диапазон, сборка и правка объекта для CalDAV-сервера.
// Поля, которых нет в форме (напоминания, участники, X-свойства), при правке
// сохраняются как были.
package calendar

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

const (
	// MaxPerObject — вхождений одного повторяющегося события в ответе.
	MaxPerObject = 1000
	// MaxRange — самый длинный диапазон запроса.
	MaxRange = 366 * 24 * time.Hour
)

var (
	ErrNoEvent  = errors.New("calendar: no VEVENT")
	ErrInvalid  = errors.New("calendar: invalid event")
	ErrNotFound = errors.New("calendar: event not found")
)

// Occurrence — одно вхождение события в диапазоне.
type Occurrence struct {
	UID          string    `json:"uid"`
	RecurrenceID string    `json:"recurrence_id,omitempty"` // у вхождений повторяющегося события
	Summary      string    `json:"summary"`
	Location     string    `json:"location,omitempty"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	AllDay       bool      `json:"all_day,omitempty"` // Start/End — полночь в поясе запроса, End не включается
	Recurring    bool      `json:"recurring,omitempty"`
	Status       string    `json:"status,omitempty"` // CANCELLED, TENTATIVE…
}

// Decode разбирает iCalendar.
func Decode(ics []byte) (*ical.Calendar, error) {
	return ical.NewDecoder(bytes.NewReader(ics)).Decode()
}

// Encode собирает iCalendar.
func Encode(cal *ical.Calendar) ([]byte, error) {
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// span — начало и конец VEVENT. Плавающее время и даты переводятся в loc.
type span struct {
	start, end      time.Time
	allDay, floated bool
}

func eventSpan(cal *ical.Calendar, ev *ical.Component, loc *time.Location) (span, error) {
	ds := ev.Props.Get(ical.PropDateTimeStart)
	if ds == nil {
		return span{}, ErrInvalid
	}
	start, allDay, floating, err := PropTime(cal, ds)
	if err != nil {
		return span{}, err
	}
	var end time.Time
	switch {
	case ev.Props.Get(ical.PropDateTimeEnd) != nil:
		end, _, _, err = PropTime(cal, ev.Props.Get(ical.PropDateTimeEnd))
		if err != nil {
			end = time.Time{}
		}
	case ev.Props.Get(ical.PropDuration) != nil:
		if d, err := ev.Props.Get(ical.PropDuration).Duration(); err == nil {
			end = start.Add(d)
		}
	}
	if end.Before(start) || end.IsZero() {
		end = start
		if allDay {
			end = start.AddDate(0, 0, 1)
		}
	}
	s := span{start: start, end: end, allDay: allDay, floated: floating || allDay}
	if s.floated {
		s.start, s.end = rewall(start, loc), rewall(end, loc)
	}
	return s, nil
}

// rewall — то же местное время (записанное в UTC) в поясе loc.
func rewall(t time.Time, loc *time.Location) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc)
}

// occKey — ключ вхождения для сравнения с RECURRENCE-ID.
func occKey(t time.Time, allDay bool) string {
	if allDay {
		return t.Format("20060102")
	}
	return t.UTC().Format("20060102T150405Z")
}

// Expand возвращает вхождения событий объекта, пересекающие [from, to).
// loc — пояс, в котором показываются события на весь день и «плавающие».
func Expand(ics []byte, from, to time.Time, loc *time.Location) ([]Occurrence, error) {
	cal, err := Decode(ics)
	if err != nil {
		return nil, err
	}
	var masters, overrides []*ical.Component
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent {
			continue
		}
		if c.Props.Get(ical.PropRecurrenceID) != nil {
			overrides = append(overrides, c)
		} else {
			masters = append(masters, c)
		}
	}
	if len(masters)+len(overrides) == 0 {
		return nil, ErrNoEvent
	}
	overlaps := func(s span) bool {
		return s.start.Before(to) && (s.end.After(from) || (s.end.Equal(s.start) && !s.start.Before(from)))
	}

	var out []Occurrence
	replaced := map[string]map[string]bool{} // UID → ключи вхождений, заменённых исключениями
	for _, o := range overrides {
		uid := text(o, ical.PropUID)
		rid, allDay, floating, err := PropTime(cal, o.Props.Get(ical.PropRecurrenceID))
		if err != nil {
			continue
		}
		if floating {
			rid = rewall(rid, loc)
		}
		if replaced[uid] == nil {
			replaced[uid] = map[string]bool{}
		}
		key := occKey(rid, allDay)
		replaced[uid][key] = true
		s, err := eventSpan(cal, o, loc)
		if err != nil || !overlaps(s) {
			continue
		}
		occ := occurrence(o, s)
		occ.RecurrenceID, occ.Recurring = key, true
		out = append(out, occ)
	}

	for _, m := range masters {
		s, err := eventSpan(cal, m, loc)
		if err != nil {
			continue
		}
		uid := text(m, ical.PropUID)
		set, err := recurrence(cal, m, s)
		if err != nil {
			return nil, err
		}
		if set == nil {
			if overlaps(s) {
				out = append(out, occurrence(m, s))
			}
			continue
		}
		dur := s.end.Sub(s.start)
		after := from.Add(-dur)
		if dur == 0 {
			after = from.Add(-time.Second)
		}
		n := 0
		for _, t := range set.Between(after, to, true) {
			key := occKey(t, s.allDay)
			if replaced[uid][key] {
				continue
			}
			os := span{start: t, end: t.Add(dur), allDay: s.allDay}
			if s.allDay {
				os.end = t.AddDate(0, 0, int((dur+12*time.Hour)/(24*time.Hour)))
			}
			if !overlaps(os) {
				continue
			}
			occ := occurrence(m, os)
			occ.RecurrenceID, occ.Recurring = key, true
			out = append(out, occ)
			if n++; n == MaxPerObject {
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

func occurrence(c *ical.Component, s span) Occurrence {
	return Occurrence{
		UID:      text(c, ical.PropUID),
		Summary:  text(c, ical.PropSummary),
		Location: text(c, ical.PropLocation),
		Start:    s.start,
		End:      s.end,
		AllDay:   s.allDay,
		Status:   strings.ToUpper(text(c, ical.PropStatus)),
	}
}

func text(c *ical.Component, name string) string {
	s, _ := c.Props.Text(name)
	return strings.TrimSpace(s)
}

// recurrence — набор повторений VEVENT; nil, если событие не повторяется.
func recurrence(cal *ical.Calendar, ev *ical.Component, s span) (*rrule.Set, error) {
	opt, err := ev.Props.RecurrenceRule()
	if err != nil {
		return nil, nil // непонятное правило — показываем первое вхождение
	}
	rdates := ev.Props.Values(ical.PropRecurrenceDates)
	if opt == nil && len(rdates) == 0 {
		return nil, nil
	}
	set := &rrule.Set{}
	if opt != nil {
		opt.Dtstart = s.start
		if !opt.Until.IsZero() && s.floated {
			opt.Until = rewall(opt.Until, s.start.Location())
		}
		r, err := rrule.NewRRule(*opt)
		if err != nil {
			return nil, nil
		}
		set.RRule(r)
	} else {
		set.RDate(s.start)
	}
	for _, t := range listTimes(cal, rdates, s) {
		set.RDate(t)
	}
	for _, t := range listTimes(cal, ev.Props.Values(ical.PropExceptionDates), s) {
		set.ExDate(t)
	}
	return set, nil
}

// listTimes — моменты из RDATE/EXDATE (значения через запятую, пояс — из параметра).
func listTimes(cal *ical.Calendar, props []ical.Prop, s span) []time.Time {
	var out []time.Time
	for _, p := range props {
		for _, v := range strings.Split(p.Value, ",") {
			one := p
			one.Value = strings.TrimSpace(v)
			t, allDay, floating, err := PropTime(cal, &one)
			if err != nil {
				continue
			}
			if allDay && !s.allDay { // EXDATE;VALUE=DATE у события со временем
				t = time.Date(t.Year(), t.Month(), t.Day(), s.start.Hour(), s.start.Minute(), s.start.Second(), 0, s.start.Location())
			} else if allDay || floating {
				t = rewall(t, s.start.Location())
			}
			out = append(out, t)
		}
	}
	return out
}

// --- правка --------------------------------------------------------------------

// Event — поля формы события.
type Event struct {
	UID         string    `json:"uid,omitempty"`
	Summary     string    `json:"summary"`
	Location    string    `json:"location,omitempty"`
	Description string    `json:"description,omitempty"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	AllDay      bool      `json:"all_day,omitempty"` // Start/End — даты, End не включается
	TZ          string    `json:"tz,omitempty"`      // IANA-пояс события; пусто — UTC
	RRule       string    `json:"rrule,omitempty"`   // FREQ=WEEKLY;COUNT=10… без "RRULE:"
	Recurring   bool      `json:"recurring,omitempty"`
}

// Validate проверяет и нормализует поля формы.
func (e *Event) Validate() error {
	e.Summary = strings.TrimSpace(e.Summary)
	if len(e.Summary) > 1000 || len(e.Location) > 1000 || len(e.Description) > 64<<10 {
		return ErrInvalid
	}
	if e.Start.IsZero() || e.End.Before(e.Start) {
		return ErrInvalid
	}
	if e.AllDay {
		e.Start = dateOnly(e.Start)
		e.End = dateOnly(e.End)
		if !e.End.After(e.Start) {
			e.End = e.Start.AddDate(0, 0, 1)
		}
	}
	if e.TZ != "" {
		if _, err := time.LoadLocation(e.TZ); err != nil || e.TZ == "Local" {
			return ErrInvalid
		}
	}
	e.RRule = strings.TrimPrefix(strings.TrimSpace(e.RRule), "RRULE:")
	if e.RRule != "" {
		if _, err := rrule.StrToROption(e.RRule); err != nil || len(e.RRule) > 500 {
			return ErrInvalid
		}
	}
	return nil
}

func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// FormEvent — поля формы из объекта (событие UID без RECURRENCE-ID).
func FormEvent(ics []byte) (Event, error) {
	cal, err := Decode(ics)
	if err != nil {
		return Event{}, err
	}
	m := master(cal, "")
	if m == nil {
		return Event{}, ErrNoEvent
	}
	s, err := eventSpan(cal, m, time.UTC)
	if err != nil {
		return Event{}, err
	}
	e := Event{
		UID: text(m, ical.PropUID), Summary: text(m, ical.PropSummary), Location: text(m, ical.PropLocation),
		Description: text(m, ical.PropDescription), Start: s.start, End: s.end, AllDay: s.allDay,
	}
	if p := m.Props.Get(ical.PropDateTimeStart); p != nil && !s.allDay {
		if tz := strings.TrimPrefix(strings.Trim(p.Params.Get(ical.PropTimezoneID), `"`), "/"); tz != "" {
			if loc, err := time.LoadLocation(tz); err == nil {
				e.TZ = tz
				e.Start, e.End = e.Start.In(loc), e.End.In(loc)
			}
		}
	}
	if p := m.Props.Get(ical.PropRecurrenceRule); p != nil {
		e.RRule = p.Value
	}
	e.Recurring = e.RRule != "" || m.Props.Get(ical.PropRecurrenceDates) != nil
	return e, nil
}

// master — VEVENT без RECURRENCE-ID (с этим UID, если он задан).
func master(cal *ical.Calendar, uid string) *ical.Component {
	for _, c := range cal.Children {
		if c.Name == ical.CompEvent && c.Props.Get(ical.PropRecurrenceID) == nil &&
			(uid == "" || text(c, ical.PropUID) == uid) {
			return c
		}
	}
	return nil
}

// NewUID — случайный UID события.
func NewUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) + "@webmail"
}

// Build создаёт объект (orig == nil) или меняет в orig событие e.UID — для
// повторяющегося события всю серию; исключения серии сохраняются.
func Build(orig []byte, e Event, now time.Time) ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	var cal *ical.Calendar
	var ev *ical.Component
	if orig == nil {
		cal = ical.NewCalendar()
		cal.Props.SetText(ical.PropProductID, "-//webmail//calendar//EN")
		cal.Props.SetText(ical.PropVersion, "2.0")
		ev = ical.NewComponent(ical.CompEvent)
		if e.UID == "" {
			e.UID = NewUID()
		}
		ev.Props.SetText(ical.PropUID, e.UID)
		ev.Props.SetDateTime(ical.PropCreated, now.UTC())
		cal.Children = append(cal.Children, ev)
	} else {
		var err error
		if cal, err = Decode(orig); err != nil {
			return nil, err
		}
		if ev = master(cal, e.UID); ev == nil {
			return nil, ErrNotFound
		}
		if p := ev.Props.Get(ical.PropSequence); p != nil {
			n, _ := p.Int()
			seq := ical.NewProp(ical.PropSequence)
			seq.Value = itoa(n + 1)
			ev.Props.Set(seq)
		}
	}
	setText(ev, ical.PropSummary, e.Summary)
	setText(ev, ical.PropLocation, strings.TrimSpace(e.Location))
	setText(ev, ical.PropDescription, strings.TrimSpace(e.Description))
	ev.Props.Del(ical.PropDuration)
	tz := ""
	if !e.AllDay && e.TZ != "" && e.TZ != "UTC" {
		tz = e.TZ
	}
	ev.Props.Set(timeProp(ical.PropDateTimeStart, e.Start, e.AllDay, tz))
	ev.Props.Set(timeProp(ical.PropDateTimeEnd, e.End, e.AllDay, tz))
	if e.RRule != "" {
		p := ical.NewProp(ical.PropRecurrenceRule)
		p.Value = e.RRule
		ev.Props.Set(p)
	} else {
		ev.Props.Del(ical.PropRecurrenceRule)
		ev.Props.Del(ical.PropRecurrenceDates)
		ev.Props.Del(ical.PropExceptionDates)
	}
	ev.Props.SetDateTime(ical.PropDateTimeStamp, now.UTC())
	ev.Props.SetDateTime(ical.PropLastModified, now.UTC())
	if tz != "" {
		ensureTimezone(cal, tz, e.Start)
	}
	return Encode(cal)
}

func setText(c *ical.Component, name, v string) {
	if v == "" {
		c.Props.Del(name)
		return
	}
	c.Props.SetText(name, v)
}

func itoa(n int) string {
	if n < 0 {
		n = 0
	}
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		if n /= 10; n == 0 {
			break
		}
	}
	return string(b[i:])
}

// timeProp — DTSTART/DTEND/EXDATE: дата, местное время с TZID или UTC.
func timeProp(name string, t time.Time, allDay bool, tz string) *ical.Prop {
	p := ical.NewProp(name)
	switch {
	case allDay:
		p.Params.Set(ical.ParamValue, string(ical.ValueDate))
		p.Value = t.Format("20060102")
	case tz != "":
		loc, _ := time.LoadLocation(tz)
		p.Params.Set(ical.PropTimezoneID, tz)
		p.Value = t.In(loc).Format("20060102T150405")
	default:
		p.Value = t.UTC().Format("20060102T150405Z")
	}
	return p
}

// DeleteOccurrence исключает одно вхождение серии UID (EXDATE) и убирает
// его исключение (VEVENT с RECURRENCE-ID), если оно было.
func DeleteOccurrence(orig []byte, uid, recurrenceID string, now time.Time) ([]byte, error) {
	cal, err := Decode(orig)
	if err != nil {
		return nil, err
	}
	m := master(cal, uid)
	if m == nil {
		return nil, ErrNotFound
	}
	allDay := len(recurrenceID) == 8
	layout := "20060102T150405Z"
	if allDay {
		layout = "20060102"
	}
	t, err := time.Parse(layout, recurrenceID)
	if err != nil {
		return nil, ErrInvalid
	}
	tz := ""
	if p := m.Props.Get(ical.PropDateTimeStart); p != nil && !allDay {
		tz = strings.TrimPrefix(strings.Trim(p.Params.Get(ical.PropTimezoneID), `"`), "/")
		if _, err := time.LoadLocation(tz); err != nil {
			tz = ""
		}
	}
	m.Props.Add(timeProp(ical.PropExceptionDates, t, allDay, tz))
	m.Props.SetDateTime(ical.PropDateTimeStamp, now.UTC())
	kept := cal.Children[:0]
	for _, c := range cal.Children {
		if c.Name == ical.CompEvent && text(c, ical.PropUID) == uid {
			if p := c.Props.Get(ical.PropRecurrenceID); p != nil {
				if rt, rAll, _, err := PropTime(cal, p); err == nil && occKey(rt, rAll) == recurrenceID {
					continue
				}
			}
		}
		kept = append(kept, c)
	}
	cal.Children = kept
	return Encode(cal)
}

// ForStorage готовит календарь из письма (iTIP) к сохранению на CalDAV-сервере:
// убирает METHOD (RFC 4791 §4.1) и ставит PARTSTAT участнику self.
func ForStorage(ics []byte, self, partStat string) ([]byte, string, error) {
	cal, err := Decode(ics)
	if err != nil {
		return nil, "", err
	}
	cal.Props.Del(ical.PropMethod)
	uid := ""
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent {
			continue
		}
		if uid == "" {
			uid = text(c, ical.PropUID)
		}
		if partStat == "" {
			continue
		}
		atts := c.Props.Values(ical.PropAttendee)
		for i := range atts {
			if strings.EqualFold(strings.TrimPrefix(strings.ToLower(atts[i].Value), "mailto:"), strings.ToLower(self)) {
				atts[i].Params.Set(ical.ParamParticipationStatus, partStat)
				atts[i].Params.Del(ical.ParamRSVP)
			}
		}
		if len(atts) > 0 {
			c.Props[ical.PropAttendee] = atts
		}
	}
	if uid == "" {
		return nil, "", ErrNoEvent
	}
	out, err := Encode(cal)
	return out, uid, err
}

// MergeInvite — объект на сервере после нового письма о той же встрече:
// событие или исключение с тем же RECURRENCE-ID заменяется, остальное
// сохраняется (например, исключения серии при обновлении одного вхождения).
func MergeInvite(stored, incoming []byte) ([]byte, error) {
	sc, err := Decode(stored)
	if err != nil {
		return incoming, nil // испорченный объект заменяем целиком
	}
	ic, err := Decode(incoming)
	if err != nil {
		return nil, err
	}
	key := func(c *ical.Component) string {
		if p := c.Props.Get(ical.PropRecurrenceID); p != nil {
			if t, allDay, _, err := PropTime(ic, p); err == nil {
				return occKey(t, allDay)
			}
			return p.Value
		}
		return ""
	}
	incomingKeys := map[string]bool{}
	tzs := map[string]bool{}
	for _, c := range ic.Children {
		switch c.Name {
		case ical.CompEvent:
			incomingKeys[key(c)] = true
		case ical.CompTimezone:
			id, _ := c.Props.Text(ical.PropTimezoneID)
			tzs[id] = true
		}
	}
	var out []*ical.Component
	for _, c := range sc.Children {
		switch c.Name {
		case ical.CompEvent:
			if incomingKeys[key(c)] {
				continue
			}
		case ical.CompTimezone:
			if id, _ := c.Props.Text(ical.PropTimezoneID); tzs[id] {
				continue
			}
		}
		out = append(out, c)
	}
	sc.Children = append(out, ic.Children...)
	return Encode(sc)
}
