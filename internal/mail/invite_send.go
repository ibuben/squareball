package mail

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
)

// InviteMail — письмо организатора участникам: METHOD:REQUEST (приглашение
// или изменение встречи) или METHOD:CANCEL (отмена). Устроено как у Google
// Calendar, чтобы его понимали Outlook, Gmail и Thunderbird: текст и
// text/calendar в multipart/alternative и тот же календарь вложением invite.ics.
type InviteMail struct {
	ICS      []byte // iTIP с METHOD (calendar.Request / calendar.Cancel)
	From     Address
	To       []Address
	Hostname string
	Lang     string // "ru" или английский по умолчанию
}

var inviteSubject = map[string]map[string]string{
	"en": {"new": "Invitation", "update": "Updated invitation", "cancel": "Cancelled"},
	"ru": {"new": "Приглашение", "update": "Изменено приглашение", "cancel": "Отменено"},
}

var inviteLabels = map[string]map[string]string{
	"en": {"when": "When", "where": "Where", "organizer": "Organizer", "attendees": "Attendees",
		"cancel": "This meeting has been cancelled.", "cancelOne": "This occurrence of the meeting has been cancelled.",
		"untitled": "(untitled)"},
	"ru": {"when": "Когда", "where": "Где", "organizer": "Организатор", "attendees": "Участники",
		"cancel": "Встреча отменена.", "cancelOne": "Это повторение встречи отменено.",
		"untitled": "(без названия)"},
}

func personLine(a Attendee) string {
	if a.Name != "" {
		return a.Name + " <" + a.Email + ">"
	}
	return a.Email
}

// inviteWhen — время встречи для текста письма, в поясе организатора.
func inviteWhen(inv *Invite) string {
	if inv.AllDay {
		end := inv.End.AddDate(0, 0, -1)
		if !end.After(inv.Start) {
			return inv.Start.Format("02.01.2006")
		}
		return inv.Start.Format("02.01.2006") + " — " + end.Format("02.01.2006")
	}
	s := inv.Start.Format("02.01.2006 15:04")
	switch {
	case inv.End.Equal(inv.Start):
	case inv.End.Format("20060102") == inv.Start.Format("20060102"):
		s += "–" + inv.End.Format("15:04")
	default:
		s += " — " + inv.End.Format("02.01.2006 15:04")
	}
	if !inv.Floating {
		if name := inv.Start.Location().String(); name != "" && name != "Local" {
			s += " (" + name + ")"
		}
	}
	return s
}

// BuildInviteMail собирает письмо участникам встречи.
func BuildInviteMail(m InviteMail) ([]byte, error) {
	if len(m.To) == 0 {
		return nil, errors.New("invite: no recipients")
	}
	lang := m.Lang
	if inviteSubject[lang] == nil {
		lang = "en"
	}
	inv, err := ParseInvite(m.ICS)
	if err != nil {
		return nil, err
	}
	method := strings.ToLower(inv.Method)
	if method != "request" && method != "cancel" {
		return nil, fmt.Errorf("invite: method %q", inv.Method)
	}
	L := inviteLabels[lang]
	summary := inv.Summary
	if summary == "" {
		summary = L["untitled"]
	}
	kind := "new"
	switch {
	case method == "cancel":
		kind = "cancel"
	case inv.Sequence > 0:
		kind = "update"
	}

	var body strings.Builder
	body.WriteString(summary + "\r\n\r\n")
	if kind == "cancel" {
		if inv.RecurrenceID != "" {
			body.WriteString(L["cancelOne"] + "\r\n\r\n")
		} else {
			body.WriteString(L["cancel"] + "\r\n\r\n")
		}
	}
	fmt.Fprintf(&body, "%s: %s\r\n", L["when"], inviteWhen(inv))
	if inv.Location != "" {
		fmt.Fprintf(&body, "%s: %s\r\n", L["where"], inv.Location)
	}
	if inv.Organizer != nil {
		fmt.Fprintf(&body, "%s: %s\r\n", L["organizer"], personLine(*inv.Organizer))
	}
	if len(inv.Attendees) > 0 && kind != "cancel" {
		names := make([]string, 0, len(inv.Attendees))
		for _, a := range inv.Attendees {
			names = append(names, personLine(a))
		}
		fmt.Fprintf(&body, "%s: %s\r\n", L["attendees"], strings.Join(names, ", "))
	}
	if d := strings.TrimSpace(inv.Description); d != "" && kind != "cancel" {
		body.WriteString("\r\n" + strings.ReplaceAll(strings.ReplaceAll(d, "\r\n", "\n"), "\n", "\r\n") + "\r\n")
	}

	var h message.Header
	mh := gomail.Header{Header: h}
	mh.SetDate(time.Now())
	mh.SetAddressList("From", toMail([]Address{m.From}))
	mh.SetAddressList("To", toMail(m.To))
	mh.SetSubject(inviteSubject[lang][kind] + ": " + summary)
	host := m.Hostname
	if host == "" {
		_, host, _ = strings.Cut(m.From.Email, "@")
	}
	if err := mh.GenerateMessageIDWithHostname(host); err != nil {
		return nil, err
	}
	mh.Set("MIME-Version", "1.0")
	mh.SetContentType("multipart/mixed", nil)

	var buf bytes.Buffer
	w, err := message.CreateWriter(&buf, mh.Header)
	if err != nil {
		return nil, err
	}
	var ah message.Header
	ah.SetContentType("multipart/alternative", nil)
	alt, err := w.CreatePart(ah)
	if err != nil {
		return nil, err
	}
	if err := writeLeaf(alt, textHeader("text/plain"), []byte(body.String())); err != nil {
		return nil, err
	}
	var ch message.Header
	ch.SetContentType("text/calendar", map[string]string{"charset": "utf-8", "method": strings.ToUpper(method)})
	ch.Set("Content-Transfer-Encoding", "base64")
	if err := writeLeaf(alt, ch, m.ICS); err != nil {
		return nil, err
	}
	if err := alt.Close(); err != nil {
		return nil, err
	}
	var fh message.Header
	fh.SetContentType("application/ics", map[string]string{"name": "invite.ics"})
	fh.SetContentDisposition("attachment", map[string]string{"filename": "invite.ics"})
	fh.Set("Content-Transfer-Encoding", "base64")
	if err := writeLeaf(w, fh, m.ICS); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
