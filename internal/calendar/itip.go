package calendar

// Участники встречи и сообщения iTIP (RFC 5546), которые организатор
// рассылает участникам: REQUEST — приглашение или изменение, CANCEL — отмена.
// Сами письма собирает пакет mail, здесь — только iCalendar.

import (
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

// MaxAttendees — участников одного события.
const MaxAttendees = 100

// Person — организатор или участник события.
type Person struct {
	Name     string `json:"name,omitempty"`
	Email    string `json:"email"`
	PartStat string `json:"partstat,omitempty"` // NEEDS-ACTION, ACCEPTED, TENTATIVE, DECLINED, DELEGATED
	Role     string `json:"role,omitempty"`     // REQ-PARTICIPANT, OPT-PARTICIPANT, CHAIR…
}

// Address — адрес из значения CAL-ADDRESS ("mailto:…"); пусто, если это не почта.
func Address(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 7 && strings.EqualFold(v[:7], "mailto:") {
		v = v[7:]
	}
	if !validEmail(v) {
		return ""
	}
	return v
}

func validEmail(v string) bool {
	at := strings.LastIndexByte(v, '@')
	return at > 0 && at < len(v)-1 && len(v) <= 254 && !strings.ContainsAny(v, " <>,;:\"\\()[]\t\r\n")
}

// normPeople проверяет адреса участников и убирает повторы.
func normPeople(in []Person) ([]Person, error) {
	if len(in) > MaxAttendees {
		return nil, ErrInvalid
	}
	out := make([]Person, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		p.Email = strings.TrimSpace(p.Email)
		p.Name = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(p.Name, "\r", ""), "\n", " "))
		if !validEmail(p.Email) || len(p.Name) > 200 {
			return nil, ErrInvalid
		}
		if k := strings.ToLower(p.Email); !seen[k] {
			seen[k] = true
			out = append(out, Person{Name: p.Name, Email: p.Email, Role: strings.ToUpper(p.Role)})
		}
	}
	return out, nil
}

func personOf(p *ical.Prop) Person {
	ps := strings.ToUpper(p.Params.Get(ical.ParamParticipationStatus))
	if ps == "" && p.Name == ical.PropAttendee {
		ps = "NEEDS-ACTION"
	}
	return Person{
		Email:    Address(p.Value),
		Name:     strings.Trim(p.Params.Get(ical.ParamCommonName), `"`),
		Role:     strings.ToUpper(p.Params.Get(ical.ParamRole)),
		PartStat: ps,
	}
}

// people — организатор и участники VEVENT (без адресов не-почты).
func people(ev *ical.Component) (*Person, []Person) {
	var org *Person
	if p := ev.Props.Get(ical.PropOrganizer); p != nil {
		if o := personOf(p); o.Email != "" {
			org = &o
		}
	}
	var out []Person
	for _, p := range ev.Props.Values(ical.PropAttendee) {
		if a := personOf(&p); a.Email != "" {
			out = append(out, a)
		}
	}
	return org, out
}

// setPeople заменяет участников VEVENT списком из формы. У тех, кто уже был,
// сохраняются ответ и прочие параметры; новые получают NEEDS-ACTION и RSVP.
// ORGANIZER ставится, когда участники появляются, и убирается вместе с ними.
func setPeople(ev *ical.Component, org *Person, list []Person) {
	old := map[string]ical.Prop{}
	for _, p := range ev.Props.Values(ical.PropAttendee) {
		old[strings.ToLower(Address(p.Value))] = p
	}
	orgEmail := ""
	if org != nil {
		orgEmail = strings.ToLower(org.Email)
	}
	var props []ical.Prop
	for _, a := range list {
		k := strings.ToLower(a.Email)
		if k == orgEmail {
			continue // организатор — не участник своей встречи
		}
		p, ok := old[k]
		if !ok {
			p = *ical.NewProp(ical.PropAttendee)
			p.Value = "mailto:" + a.Email
			role := a.Role
			if role != "OPT-PARTICIPANT" {
				role = "REQ-PARTICIPANT"
			}
			p.Params.Set(ical.ParamRole, role)
			p.Params.Set(ical.ParamParticipationStatus, "NEEDS-ACTION")
			p.Params.Set(ical.ParamRSVP, "TRUE")
		}
		if a.Name != "" {
			p.Params.Set(ical.ParamCommonName, a.Name)
		}
		props = append(props, p)
	}
	if len(props) == 0 {
		ev.Props.Del(ical.PropAttendee)
		if p := ev.Props.Get(ical.PropOrganizer); p != nil && (orgEmail == "" || strings.EqualFold(Address(p.Value), orgEmail)) {
			ev.Props.Del(ical.PropOrganizer)
		}
		return
	}
	ev.Props[ical.PropAttendee] = props
	if ev.Props.Get(ical.PropOrganizer) == nil && org != nil {
		p := ical.NewProp(ical.PropOrganizer)
		p.Value = "mailto:" + org.Email
		if org.Name != "" {
			p.Params.Set(ical.ParamCommonName, org.Name)
		}
		ev.Props.Set(p)
	}
}

func sequence(ev *ical.Component) int {
	if p := ev.Props.Get(ical.PropSequence); p != nil {
		n, _ := p.Int()
		return n
	}
	return 0
}

func seqProp(n int) *ical.Prop {
	p := ical.NewProp(ical.PropSequence)
	p.Value = itoa(n)
	return p
}

// Meeting — участники встречи из объекта CalDAV (событие без RECURRENCE-ID).
func Meeting(ics []byte) (org *Person, attendees []Person, err error) {
	cal, err := Decode(ics)
	if err != nil {
		return nil, nil, err
	}
	m := master(cal, "")
	if m == nil {
		return nil, nil, ErrNoEvent
	}
	org, attendees = people(m)
	return org, attendees, nil
}

// itipCalendar — пустой календарь iTIP с METHOD и поясами из src.
func itipCalendar(src *ical.Calendar, method string) *ical.Calendar {
	out := ical.NewCalendar()
	out.Props.SetText(ical.PropProductID, "-//webmail//iTIP//EN")
	out.Props.SetText(ical.PropVersion, "2.0")
	out.Props.SetText(ical.PropMethod, method)
	for _, c := range src.Children {
		if c.Name == ical.CompTimezone {
			out.Children = append(out.Children, c)
		}
	}
	return out
}

// withoutAlarms — копия VEVENT без VALARM: напоминания организатора участникам не нужны.
func withoutAlarms(c *ical.Component) *ical.Component {
	cp := &ical.Component{Name: c.Name, Props: ical.Props{}}
	for k, v := range c.Props {
		cp.Props[k] = append([]ical.Prop(nil), v...)
	}
	for _, ch := range c.Children {
		if ch.Name != ical.CompAlarm {
			cp.Children = append(cp.Children, ch)
		}
	}
	return cp
}

// Request — METHOD:REQUEST по объекту: встреча целиком, с исключениями серии.
func Request(ics []byte, now time.Time) ([]byte, error) {
	cal, err := Decode(ics)
	if err != nil {
		return nil, err
	}
	m := master(cal, "")
	if m == nil {
		return nil, ErrNoEvent
	}
	uid := text(m, ical.PropUID)
	out := itipCalendar(cal, "REQUEST")
	for _, c := range cal.Children {
		if c.Name == ical.CompEvent && text(c, ical.PropUID) == uid {
			ev := withoutAlarms(c)
			ev.Props.SetDateTime(ical.PropDateTimeStamp, now.UTC())
			out.Children = append(out.Children, ev)
		}
	}
	return Encode(out)
}

// Cancel — METHOD:CANCEL по объекту. recurrenceID — отменено одно вхождение
// (ключ, как в Occurrence), иначе вся встреча. only — кому отмена (участники,
// которых убрали из встречи); пусто — всем участникам. bump — поднять
// SEQUENCE: встреча удаляется, и новой версии на сервере не будет.
func Cancel(ics []byte, recurrenceID string, only []string, bump bool, now time.Time) ([]byte, error) {
	cal, err := Decode(ics)
	if err != nil {
		return nil, err
	}
	m := master(cal, "")
	if m == nil {
		return nil, ErrNoEvent
	}
	ev := &ical.Component{Name: ical.CompEvent, Props: ical.Props{}}
	for _, name := range []string{ical.PropUID, ical.PropOrganizer, ical.PropSummary, ical.PropLocation} {
		if p := m.Props.Get(name); p != nil {
			ev.Props.Set(p)
		}
	}
	seq := sequence(m)
	if bump {
		seq++
	}
	ev.Props.Set(seqProp(seq))
	if recurrenceID == "" {
		for _, name := range []string{ical.PropDateTimeStart, ical.PropDateTimeEnd, ical.PropDuration, ical.PropRecurrenceRule} {
			if p := m.Props.Get(name); p != nil {
				ev.Props.Set(p)
			}
		}
	} else {
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
		ev.Props.Set(timeProp(ical.PropRecurrenceID, t, allDay, tz))
		ev.Props.Set(timeProp(ical.PropDateTimeStart, t, allDay, tz))
	}
	want := map[string]bool{}
	for _, e := range only {
		want[strings.ToLower(e)] = true
	}
	var atts []ical.Prop
	for _, p := range m.Props.Values(ical.PropAttendee) {
		if len(want) == 0 || want[strings.ToLower(Address(p.Value))] {
			atts = append(atts, p)
		}
	}
	if len(atts) == 0 && len(only) > 0 { // убранных участников в объекте уже нет
		for _, e := range only {
			p := ical.NewProp(ical.PropAttendee)
			p.Value = "mailto:" + e
			atts = append(atts, *p)
		}
	}
	if len(atts) > 0 {
		ev.Props[ical.PropAttendee] = atts
	}
	ev.Props.SetText(ical.PropStatus, "CANCELLED")
	ev.Props.SetDateTime(ical.PropDateTimeStamp, now.UTC())
	out := itipCalendar(cal, "CANCEL")
	out.Children = append(out.Children, ev)
	return Encode(out)
}

// ApplyReply записывает в объект организатора ответ участника (METHOD:REPLY):
// PARTSTAT у ATTENDEE с адресом from. Ответ на одно вхождение меняет только
// его исключение, если оно есть. changed — объект изменился и его нужно сохранить.
func ApplyReply(stored, reply []byte, from string) (out []byte, changed bool, err error) {
	sc, err := Decode(stored)
	if err != nil {
		return nil, false, err
	}
	rc, err := Decode(reply)
	if err != nil {
		return nil, false, err
	}
	from = strings.ToLower(from)
	for _, r := range rc.Children {
		if r.Name != ical.CompEvent {
			continue
		}
		var att *ical.Prop
		for _, p := range r.Props.Values(ical.PropAttendee) {
			if strings.ToLower(Address(p.Value)) == from {
				att = &p
				break
			}
		}
		if att == nil {
			continue
		}
		ps := strings.ToUpper(att.Params.Get(ical.ParamParticipationStatus))
		switch ps {
		case "ACCEPTED", "TENTATIVE", "DECLINED", "DELEGATED", "NEEDS-ACTION":
		default:
			continue
		}
		target := target(sc, rc, r)
		if target == nil {
			continue
		}
		atts := target.Props.Values(ical.PropAttendee)
		found := false
		for i := range atts {
			if strings.ToLower(Address(atts[i].Value)) != from {
				continue
			}
			found = true
			if strings.ToUpper(atts[i].Params.Get(ical.ParamParticipationStatus)) != ps {
				atts[i].Params.Set(ical.ParamParticipationStatus, ps)
				atts[i].Params.Del(ical.ParamRSVP)
				changed = true
			}
		}
		if !found { // ответил тот, кому переслали приглашение
			p := ical.NewProp(ical.PropAttendee)
			p.Value = "mailto:" + Address(att.Value)
			if cn := att.Params.Get(ical.ParamCommonName); cn != "" {
				p.Params.Set(ical.ParamCommonName, cn)
			}
			p.Params.Set(ical.ParamParticipationStatus, ps)
			atts = append(atts, *p)
			changed = true
		}
		target.Props[ical.PropAttendee] = atts
	}
	if !changed {
		return stored, false, nil
	}
	out, err = Encode(sc)
	return out, err == nil, err
}

// target — VEVENT объекта, к которому относится компонент ответа r.
func target(sc, rc *ical.Calendar, r *ical.Component) *ical.Component {
	uid := text(r, ical.PropUID)
	rp := r.Props.Get(ical.PropRecurrenceID)
	if rp == nil {
		return master(sc, uid)
	}
	rt, rAll, _, err := PropTime(rc, rp)
	if err != nil {
		return nil
	}
	want := occKey(rt, rAll)
	for _, c := range sc.Children {
		if c.Name != ical.CompEvent || text(c, ical.PropUID) != uid {
			continue
		}
		if p := c.Props.Get(ical.PropRecurrenceID); p != nil {
			if t, all, _, err := PropTime(sc, p); err == nil && occKey(t, all) == want {
				return c
			}
		}
	}
	return nil
}
