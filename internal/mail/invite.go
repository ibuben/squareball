package mail

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"

	"github.com/ibuben/squareball/internal/calendar"
	"github.com/ibuben/squareball/internal/locale"
)

// Invite — событие из части text/calendar письма (iTIP, RFC 5546): приглашение,
// отмена, ответ участника или просто опубликованное событие.
type Invite struct {
	Part         int        `json:"part"`             // индекс вложения .ics
	Method       string     `json:"method,omitempty"` // REQUEST, CANCEL, REPLY, PUBLISH, COUNTER…
	UID          string     `json:"uid"`
	Sequence     int        `json:"sequence,omitempty"`
	RecurrenceID string     `json:"recurrence_id,omitempty"` // ответ или отмена касается одного повторения
	Summary      string     `json:"summary"`
	Location     string     `json:"location,omitempty"`
	URL          string     `json:"url,omitempty"`
	Start        time.Time  `json:"start"`
	End          time.Time  `json:"end"`
	AllDay       bool       `json:"all_day,omitempty"`  // даты без времени, End — следующий день после последнего
	Floating     bool       `json:"floating,omitempty"` // время без часового пояса: показывать как есть
	Recurring    bool       `json:"recurring,omitempty"`
	Status       string     `json:"status,omitempty"` // STATUS события: CANCELLED, TENTATIVE…
	Organizer    *Attendee  `json:"organizer,omitempty"`
	Attendees    []Attendee `json:"attendees"`
	Description  string     `json:"-"`
}

type Attendee struct {
	Name     string `json:"name,omitempty"`
	Email    string `json:"email"`
	Role     string `json:"role,omitempty"`     // REQ-PARTICIPANT, OPT-PARTICIPANT, CHAIR…
	PartStat string `json:"partstat,omitempty"` // NEEDS-ACTION, ACCEPTED, TENTATIVE, DECLINED, DELEGATED
}

// Ответы на приглашение (PARTSTAT).
const (
	PartAccepted  = "ACCEPTED"
	PartTentative = "TENTATIVE"
	PartDeclined  = "DECLINED"
)

var errNoEvent = errors.New("ical: no VEVENT")

// isCalendarPart — часть письма с iCalendar: text/calendar или .ics-вложение.
func isCalendarPart(ctype, filename string) bool {
	return ctype == "text/calendar" || ctype == "application/ics" ||
		(strings.HasSuffix(strings.ToLower(filename), ".ics") && (ctype == "application/octet-stream" || ctype == "text/plain"))
}

func decodeCalendar(data []byte) (*ical.Calendar, error) {
	cal, err := ical.NewDecoder(bytes.NewReader(data)).Decode()
	if err != nil {
		return nil, err
	}
	return cal, nil
}

// mainEvent — VEVENT, о котором письмо: без RECURRENCE-ID (вся серия), иначе первый.
func mainEvent(cal *ical.Calendar) *ical.Component {
	var first *ical.Component
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent {
			continue
		}
		if c.Props.Get(ical.PropRecurrenceID) == nil {
			return c
		}
		if first == nil {
			first = c
		}
	}
	return first
}

// ParseInvite разбирает iCalendar. Ошибка — если это не календарь или в нём нет событий.
func ParseInvite(data []byte) (*Invite, error) {
	cal, err := decodeCalendar(data)
	if err != nil {
		return nil, err
	}
	ev := mainEvent(cal)
	if ev == nil {
		return nil, errNoEvent
	}
	inv := &Invite{Attendees: []Attendee{}}
	if p := cal.Props.Get(ical.PropMethod); p != nil {
		inv.Method = strings.ToUpper(strings.TrimSpace(p.Value))
	}
	text := func(name string) string {
		s, _ := ev.Props.Text(name)
		return strings.TrimSpace(s)
	}
	inv.UID = text(ical.PropUID)
	inv.Summary = text(ical.PropSummary)
	inv.Location = text(ical.PropLocation)
	inv.Description = text(ical.PropDescription)
	inv.Status = strings.ToUpper(text(ical.PropStatus))
	if p := ev.Props.Get(ical.PropSequence); p != nil {
		inv.Sequence, _ = p.Int()
	}
	if u := text(ical.PropURL); strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		inv.URL = u
	}
	inv.Recurring = ev.Props.Get(ical.PropRecurrenceRule) != nil || ev.Props.Get(ical.PropRecurrenceDates) != nil
	if p := ev.Props.Get(ical.PropRecurrenceID); p != nil {
		inv.RecurrenceID = p.Value
	}

	start := ev.Props.Get(ical.PropDateTimeStart)
	if start == nil {
		return nil, errors.New("ical: VEVENT without DTSTART")
	}
	inv.Start, inv.AllDay, inv.Floating, err = propTime(cal, start)
	if err != nil {
		return nil, err
	}
	switch {
	case ev.Props.Get(ical.PropDateTimeEnd) != nil:
		inv.End, _, _, err = propTime(cal, ev.Props.Get(ical.PropDateTimeEnd))
		if err != nil {
			inv.End = time.Time{}
		}
	case ev.Props.Get(ical.PropDuration) != nil:
		if d, err := ev.Props.Get(ical.PropDuration).Duration(); err == nil {
			inv.End = inv.Start.Add(d)
		}
	}
	if inv.End.Before(inv.Start) || inv.End.IsZero() {
		inv.End = inv.Start
		if inv.AllDay {
			inv.End = inv.Start.AddDate(0, 0, 1)
		}
	}

	if p := ev.Props.Get(ical.PropOrganizer); p != nil {
		a := attendeeOf(p)
		if a.Email != "" {
			inv.Organizer = &a
		}
	}
	for _, p := range ev.Props.Values(ical.PropAttendee) {
		if a := attendeeOf(&p); a.Email != "" {
			inv.Attendees = append(inv.Attendees, a)
		}
	}
	return inv, nil
}

func calAddress(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 7 && strings.EqualFold(v[:7], "mailto:") {
		v = v[7:]
	}
	if !strings.Contains(v, "@") || strings.ContainsAny(v, " <>,;\"\r\n") {
		return ""
	}
	return v
}

func attendeeOf(p *ical.Prop) Attendee {
	a := Attendee{
		Email:    calAddress(p.Value),
		Name:     strings.Trim(p.Params.Get(ical.ParamCommonName), `"`),
		Role:     strings.ToUpper(p.Params.Get(ical.ParamRole)),
		PartStat: strings.ToUpper(p.Params.Get(ical.ParamParticipationStatus)),
	}
	if a.PartStat == "" && p.Name == ical.PropAttendee {
		a.PartStat = "NEEDS-ACTION"
	}
	return a
}

// propTime — момент из DTSTART/DTEND; см. calendar.PropTime.
func propTime(cal *ical.Calendar, p *ical.Prop) (time.Time, bool, bool, error) {
	return calendar.PropTime(cal, p)
}

// InviteReply — ответ участника на приглашение (METHOD:REPLY).
type InviteReply struct {
	ICS       []byte  // календарь из исходного письма
	From      Address // участник, который отвечает
	PartStat  string  // ACCEPTED, TENTATIVE, DECLINED
	Comment   string
	Subject   string // тема исходного письма — если в событии нет SUMMARY
	MessageID string // исходного письма, для In-Reply-To
	Hostname  string
	Lang      string // язык темы и текста: код языка (internal/locale)
}

// BuildInviteReply собирает письмо организатору с text/calendar; method=REPLY.
// Возвращает письмо и адрес организатора (получатель конверта).
func BuildInviteReply(r InviteReply) ([]byte, string, error) {
	lang := r.Lang
	ps := strings.ToLower(r.PartStat)
	if r.PartStat != PartAccepted && r.PartStat != PartTentative && r.PartStat != PartDeclined {
		return nil, "", fmt.Errorf("invalid partstat %q", r.PartStat)
	}
	cal, err := decodeCalendar(r.ICS)
	if err != nil {
		return nil, "", err
	}
	ev := mainEvent(cal)
	if ev == nil {
		return nil, "", errNoEvent
	}
	org := ev.Props.Get(ical.PropOrganizer)
	if org == nil || calAddress(org.Value) == "" {
		return nil, "", errors.New("ical: no ORGANIZER")
	}
	to := calAddress(org.Value)

	out := ical.NewCalendar()
	out.Props.SetText(ical.PropProductID, "-//webmail//iTIP//RU")
	out.Props.SetText(ical.PropVersion, "2.0")
	out.Props.SetText(ical.PropMethod, "REPLY")
	for _, c := range cal.Children {
		if c.Name == ical.CompTimezone {
			out.Children = append(out.Children, c)
		}
	}
	reply := ical.NewComponent(ical.CompEvent)
	for _, name := range []string{ical.PropUID, ical.PropSequence, ical.PropRecurrenceID, ical.PropDateTimeStart,
		ical.PropDateTimeEnd, ical.PropDuration, ical.PropSummary, ical.PropLocation, ical.PropOrganizer} {
		if p := ev.Props.Get(name); p != nil {
			reply.Props.Set(p)
		}
	}
	reply.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())

	var att *ical.Prop
	for _, p := range ev.Props.Values(ical.PropAttendee) {
		if strings.EqualFold(calAddress(p.Value), r.From.Email) {
			cp := p
			cp.Params = ical.Params{}
			for k, v := range p.Params {
				cp.Params[k] = append([]string(nil), v...)
			}
			att = &cp
			break
		}
	}
	if att == nil { // приглашение пришло на рассылку или переслано — отвечаем от себя
		att = ical.NewProp(ical.PropAttendee)
		att.Value = "mailto:" + r.From.Email
		if r.From.Name != "" {
			att.Params.Set(ical.ParamCommonName, r.From.Name)
		}
	}
	att.Params.Set(ical.ParamParticipationStatus, r.PartStat)
	att.Params.Del(ical.ParamRSVP)
	reply.Props.Set(att)
	if c := strings.TrimSpace(r.Comment); c != "" {
		reply.Props.SetText(ical.PropComment, c)
	}
	out.Children = append(out.Children, reply)

	var ics bytes.Buffer
	if err := ical.NewEncoder(&ics).Encode(out); err != nil {
		return nil, "", err
	}

	summary, _ := ev.Props.Text(ical.PropSummary)
	if summary == "" {
		summary = r.Subject
	}
	var h message.Header
	mh := gomail.Header{Header: h}
	mh.SetDate(time.Now())
	mh.SetAddressList("From", toMail([]Address{r.From}))
	orgName := strings.Trim(org.Params.Get(ical.ParamCommonName), `"`)
	mh.SetAddressList("To", []*gomail.Address{{Name: orgName, Address: to}})
	mh.SetSubject(locale.S(lang, "invite.reply."+ps) + ": " + summary)
	host := r.Hostname
	if host == "" {
		_, host, _ = strings.Cut(r.From.Email, "@")
	}
	if err := mh.GenerateMessageIDWithHostname(host); err != nil {
		return nil, "", err
	}
	if r.MessageID != "" {
		mh.SetMsgIDList("In-Reply-To", []string{r.MessageID})
		mh.SetMsgIDList("References", []string{r.MessageID})
	}
	mh.Set("MIME-Version", "1.0")
	mh.SetContentType("multipart/alternative", nil)

	var buf bytes.Buffer
	w, err := message.CreateWriter(&buf, mh.Header)
	if err != nil {
		return nil, "", err
	}
	who := r.From.Email
	if r.From.Name != "" {
		who = r.From.Name + " <" + r.From.Email + ">"
	}
	body := fmt.Sprintf(locale.S(lang, "invite.reply.body."+ps), who, summary)
	if c := strings.TrimSpace(r.Comment); c != "" {
		body += "\r\n" + c + "\r\n"
	}
	if err := writeLeaf(w, textHeader("text/plain"), []byte(body)); err != nil {
		return nil, "", err
	}
	var ch message.Header
	ch.SetContentType("text/calendar", map[string]string{"charset": "utf-8", "method": "REPLY"})
	ch.Set("Content-Transfer-Encoding", "base64")
	if err := writeLeaf(w, ch, ics.Bytes()); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), to, nil
}
