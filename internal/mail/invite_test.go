package mail

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
)

func ics(lines ...string) []byte {
	return []byte(strings.Join(lines, "\r\n") + "\r\n")
}

// Outlook: имя пояса Windows, которого нет в tzdata, и VTIMEZONE без переходов.
var outlookInvite = ics(
	"BEGIN:VCALENDAR", "METHOD:REQUEST", "PRODID:Microsoft Exchange Server 2010", "VERSION:2.0",
	"BEGIN:VTIMEZONE", "TZID:West Asia Standard Time",
	"BEGIN:STANDARD", "DTSTART:16010101T000000", "TZOFFSETFROM:+0500", "TZOFFSETTO:+0500", "END:STANDARD",
	"END:VTIMEZONE",
	"BEGIN:VEVENT",
	"ORGANIZER;CN=\"Alice A.\":mailto:alice@test.local",
	"ATTENDEE;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=TRUE;CN=Bob:mailto:Bob@test.local",
	"ATTENDEE;ROLE=OPT-PARTICIPANT;PARTSTAT=ACCEPTED;CN=Carol:mailto:carol@test.local",
	"DESCRIPTION:Повестка:\\n1. Итоги\\, планы",
	"UID:040000008200E00074C5B7101A82E008", "SUMMARY;LANGUAGE=ru-RU:Планёрка", "SEQUENCE:2",
	"DTSTART;TZID=West Asia Standard Time:20261201T100000", "DTEND;TZID=West Asia Standard Time:20261201T110000",
	"LOCATION:Переговорная 3", "STATUS:CONFIRMED", "DTSTAMP:20261124T080000Z",
	"END:VEVENT", "END:VCALENDAR")

func TestParseInviteOutlook(t *testing.T) {
	inv, err := ParseInvite(outlookInvite)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Method != "REQUEST" || inv.Summary != "Планёрка" || inv.Location != "Переговорная 3" || inv.Sequence != 2 {
		t.Fatalf("fields: %+v", inv)
	}
	if want := time.Date(2026, 12, 1, 5, 0, 0, 0, time.UTC); !inv.Start.Equal(want) || !inv.End.Equal(want.Add(time.Hour)) {
		t.Errorf("time %v – %v, want %v", inv.Start, inv.End, want)
	}
	if inv.AllDay || inv.Floating {
		t.Error("not all-day, not floating")
	}
	if inv.Organizer == nil || inv.Organizer.Email != "alice@test.local" || inv.Organizer.Name != "Alice A." {
		t.Errorf("organizer %+v", inv.Organizer)
	}
	if len(inv.Attendees) != 2 || inv.Attendees[0].PartStat != "NEEDS-ACTION" || inv.Attendees[1].Role != "OPT-PARTICIPANT" {
		t.Errorf("attendees %+v", inv.Attendees)
	}
	if inv.Description != "Повестка:\n1. Итоги, планы" {
		t.Errorf("description %q", inv.Description)
	}
}

func TestParseInviteTimezones(t *testing.T) {
	// Свой TZID с переходами на летнее время: смещение зависит от даты.
	vtz := []string{
		"BEGIN:VTIMEZONE", "TZID:Custom Berlin",
		"BEGIN:STANDARD", "DTSTART:16011028T030000", "RRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=10",
		"TZOFFSETFROM:+0200", "TZOFFSETTO:+0100", "END:STANDARD",
		"BEGIN:DAYLIGHT", "DTSTART:16010325T020000", "RRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=3",
		"TZOFFSETFROM:+0100", "TZOFFSETTO:+0200", "END:DAYLIGHT",
		"END:VTIMEZONE",
	}
	event := func(dtstart string) []byte {
		lines := append([]string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:x"}, vtz...)
		return ics(append(lines, "BEGIN:VEVENT", "UID:1", "DTSTAMP:20260101T000000Z", "SUMMARY:x", dtstart,
			"END:VEVENT", "END:VCALENDAR")...)
	}
	for _, c := range []struct {
		dtstart  string
		want     time.Time
		allDay   bool
		floating bool
	}{
		{"DTSTART;TZID=Custom Berlin:20260715T120000", time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC), false, false},
		{"DTSTART;TZID=Custom Berlin:20260115T120000", time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC), false, false},
		{"DTSTART;TZID=Asia/Tashkent:20260115T120000", time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC), false, false},
		{"DTSTART:20260115T120000Z", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC), false, false},
		{"DTSTART;VALUE=DATE:20260115", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), true, false},
		{"DTSTART:20260115T120000", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC), false, true},
		{"DTSTART;TZID=Nowhere/Unknown:20260115T120000", time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC), false, true},
	} {
		inv, err := ParseInvite(event(c.dtstart))
		if err != nil {
			t.Errorf("%s: %v", c.dtstart, err)
			continue
		}
		if !inv.Start.Equal(c.want) || inv.AllDay != c.allDay || inv.Floating != c.floating {
			t.Errorf("%s: got %v all-day=%v floating=%v", c.dtstart, inv.Start.UTC(), inv.AllDay, inv.Floating)
		}
		if c.allDay && !inv.End.Equal(c.want.AddDate(0, 0, 1)) {
			t.Errorf("%s: all-day end %v", c.dtstart, inv.End)
		}
	}
	if _, err := ParseInvite([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nEND:VCALENDAR\r\n")); err == nil {
		t.Error("calendar without events must fail")
	}
}

func TestBuildInviteReply(t *testing.T) {
	raw, to, err := BuildInviteReply(InviteReply{
		ICS: outlookInvite, From: Address{Name: "Bob", Email: "bob@test.local"}, PartStat: PartAccepted,
		Comment: "Буду", MessageID: "orig@test.local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if to != "alice@test.local" {
		t.Errorf("to %q", to)
	}
	e, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if subj, _ := (&gomail.Header{Header: e.Header}).Subject(); subj != "Принято: Планёрка" {
		t.Errorf("subject %q", subj)
	}
	if e.Header.Get("In-Reply-To") != "<orig@test.local>" {
		t.Errorf("in-reply-to %q", e.Header.Get("In-Reply-To"))
	}
	var cal []byte
	mr := e.MultipartReader()
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ct, params, _ := p.Header.ContentType(); ct == "text/calendar" {
			if params["method"] != "REPLY" {
				t.Errorf("content-type method %v", params)
			}
			cal, _ = io.ReadAll(p.Body)
		}
	}
	if cal == nil {
		t.Fatal("no text/calendar part")
	}
	inv, err := ParseInvite(cal)
	if err != nil {
		t.Fatalf("reply does not parse: %v\n%s", err, cal)
	}
	if inv.Method != "REPLY" || inv.UID != "040000008200E00074C5B7101A82E008" || inv.Sequence != 2 {
		t.Errorf("reply fields %+v", inv)
	}
	if len(inv.Attendees) != 1 || !strings.EqualFold(inv.Attendees[0].Email, "bob@test.local") || inv.Attendees[0].PartStat != "ACCEPTED" {
		t.Errorf("reply attendees %+v", inv.Attendees)
	}
	s := string(cal)
	for _, want := range []string{"BEGIN:VTIMEZONE", "COMMENT:Буду", "DTSTAMP:"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "RSVP") || strings.Contains(s, "carol") || strings.Contains(s, "DESCRIPTION") {
		t.Errorf("reply must carry only own attendee without RSVP and description:\n%s", s)
	}

	// Не участник (приглашение на рассылку) — отвечает от своего имени.
	_, _, err = BuildInviteReply(InviteReply{ICS: outlookInvite, From: Address{Email: "dave@test.local"}, PartStat: PartDeclined})
	if err != nil {
		t.Error(err)
	}
	if _, _, err := BuildInviteReply(InviteReply{ICS: outlookInvite, From: Address{Email: "bob@test.local"}, PartStat: "MAYBE"}); err == nil {
		t.Error("invalid partstat must fail")
	}
}

func TestIsCalendarPart(t *testing.T) {
	for _, c := range []struct {
		ct, name string
		want     bool
	}{
		{"text/calendar", "", true}, {"application/ics", "x", true}, {"application/octet-stream", "Invite.ICS", true},
		{"application/pdf", "a.ics", false}, {"text/plain", "notes.txt", false},
	} {
		if got := isCalendarPart(c.ct, c.name); got != c.want {
			t.Errorf("%s %s: %v", c.ct, c.name, got)
		}
	}
}
