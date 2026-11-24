package mail

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
	"github.com/teambition/rrule-go"
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

// propTime — момент из DTSTART/DTEND. Часовой пояс берётся из базы tzdata по TZID,
// а если там такого нет (Outlook пишет «Russian Standard Time») — из VTIMEZONE письма.
func propTime(cal *ical.Calendar, p *ical.Prop) (t time.Time, allDay, floating bool, err error) {
	v := strings.TrimSpace(p.Value)
	if p.ValueType() == ical.ValueDate || len(v) == 8 {
		t, err = time.ParseInLocation("20060102", v, time.UTC)
		return t, true, false, err
	}
	if strings.HasSuffix(v, "Z") {
		t, err = time.ParseInLocation("20060102T150405Z", v, time.UTC)
		return t, false, false, err
	}
	wall, err := time.ParseInLocation("20060102T150405", v, time.UTC)
	if err != nil {
		return time.Time{}, false, false, err
	}
	tzid := strings.TrimPrefix(strings.Trim(p.Params.Get(ical.PropTimezoneID), `"`), "/")
	if tzid == "" {
		return wall, false, true, nil
	}
	if loc, err := time.LoadLocation(tzid); err == nil && tzid != "Local" {
		return inLocation(wall, loc), false, false, nil
	}
	for _, c := range cal.Children {
		if c.Name != ical.CompTimezone {
			continue
		}
		if id, _ := c.Props.Text(ical.PropTimezoneID); strings.TrimPrefix(id, "/") == tzid {
			if off, ok := vtimezoneOffset(c, wall); ok {
				return wall.Add(-time.Duration(off) * time.Second).In(time.FixedZone(tzid, off)), false, false, nil
			}
		}
	}
	return wall, false, true, nil // пояс неизвестен — показываем время как написано
}

func inLocation(wall time.Time, loc *time.Location) time.Time {
	return time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, loc)
}

// vtimezoneOffset — смещение (в секундах) VTIMEZONE для местного времени wall:
// TZOFFSETTO последнего перехода STANDARD/DAYLIGHT, случившегося не позже wall.
// Время переходов сравнивается как местное (все — в UTC без сдвига).
func vtimezoneOffset(tz *ical.Component, wall time.Time) (int, bool) {
	var best time.Time
	bestOff, earliestFrom := 0, 0
	var earliest time.Time
	found, any := false, false
	for _, c := range tz.Children {
		if c.Name != ical.CompTimezoneStandard && c.Name != ical.CompTimezoneDaylight {
			continue
		}
		to, ok1 := parseUTCOffset(c.Props.Get(ical.PropTimezoneOffsetTo))
		from, ok2 := parseUTCOffset(c.Props.Get(ical.PropTimezoneOffsetFrom))
		ds := c.Props.Get(ical.PropDateTimeStart)
		if !ok1 || ds == nil {
			continue
		}
		start, err := time.ParseInLocation("20060102T150405", strings.TrimSuffix(ds.Value, "Z"), time.UTC)
		if err != nil {
			continue
		}
		if !any || start.Before(earliest) {
			earliest, earliestFrom, any = start, to, true
			if ok2 {
				earliestFrom = from
			}
		}
		onset := time.Time{}
		if opt, err := c.Props.RecurrenceRule(); err == nil && opt != nil {
			opt.Dtstart = start
			// Outlook пишет DTSTART в 1601 году, а rrule-go перебирает повторения
			// с ограничением и до наших лет не доходит. Годовое правило с BYMONTH
			// не зависит от года начала — переносим его поближе.
			if opt.Freq == rrule.YEARLY && opt.Count == 0 && start.Year() < wall.Year()-2 {
				opt.Dtstart = time.Date(wall.Year()-2, 1, 1, start.Hour(), start.Minute(), start.Second(), 0, time.UTC)
				if len(opt.Bymonth) == 0 {
					opt.Bymonth = []int{int(start.Month())}
				}
				if len(opt.Bymonthday) == 0 && len(opt.Byweekday) == 0 && len(opt.Byyearday) == 0 {
					opt.Bymonthday = []int{start.Day()}
				}
			}
			if !opt.Until.IsZero() {
				opt.Until = opt.Until.Add(time.Duration(to) * time.Second) // UNTIL — в UTC
			}
			if r, err := rrule.NewRRule(*opt); err == nil {
				onset = r.Before(wall, true)
			}
		} else if !start.After(wall) {
			onset = start
		}
		for _, rd := range c.Props.Values(ical.PropRecurrenceDates) {
			for _, s := range strings.Split(rd.Value, ",") {
				if t, err := time.ParseInLocation("20060102T150405", s, time.UTC); err == nil && !t.After(wall) && t.After(onset) {
					onset = t
				}
			}
		}
		if !onset.IsZero() && (!found || onset.After(best)) {
			best, bestOff, found = onset, to, true
		}
	}
	if found {
		return bestOff, true
	}
	return earliestFrom, any
}

func parseUTCOffset(p *ical.Prop) (int, bool) {
	if p == nil {
		return 0, false
	}
	v := strings.TrimSpace(p.Value)
	if len(v) != 5 && len(v) != 7 || (v[0] != '+' && v[0] != '-') {
		return 0, false
	}
	h, err1 := strconv.Atoi(v[1:3])
	m, err2 := strconv.Atoi(v[3:5])
	s := 0
	var err3 error
	if len(v) == 7 {
		s, err3 = strconv.Atoi(v[5:7])
	}
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	off := h*3600 + m*60 + s
	if v[0] == '-' {
		off = -off
	}
	return off, true
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
}

var replyPrefix = map[string]string{PartAccepted: "Принято", PartTentative: "Под вопросом", PartDeclined: "Отклонено"}
var replyVerb = map[string]string{PartAccepted: "принимает", PartTentative: "под вопросом принимает", PartDeclined: "отклоняет"}

// BuildInviteReply собирает письмо организатору с text/calendar; method=REPLY.
// Возвращает письмо и адрес организатора (получатель конверта).
func BuildInviteReply(r InviteReply) ([]byte, string, error) {
	if replyPrefix[r.PartStat] == "" {
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
	mh.SetSubject(replyPrefix[r.PartStat] + ": " + summary)
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
	body := fmt.Sprintf("%s %s приглашение «%s».\r\n", who, replyVerb[r.PartStat], summary)
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
