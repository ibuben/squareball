package calendar

import (
	"strings"
	"testing"
	"time"
)

func TestMeetingAttendees(t *testing.T) {
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	org := &Person{Name: "Алиса", Email: "alice@test.local"}
	e := Event{Summary: "Планёрка", Start: time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC), End: time.Date(2026, 12, 1, 11, 0, 0, 0, time.UTC),
		Organizer: org, Attendees: []Person{{Name: "Bob", Email: "bob@test.local"}, {Email: "carol@test.local"}, {Email: "BOB@test.local"}, {Email: "alice@test.local"}}}
	ics, err := Build(nil, e, now)
	if err != nil {
		t.Fatal(err)
	}
	gotOrg, atts, err := Meeting(ics)
	if err != nil || gotOrg == nil || gotOrg.Email != "alice@test.local" || len(atts) != 2 || atts[0].PartStat != "NEEDS-ACTION" || atts[0].Name != "Bob" {
		t.Fatalf("meeting: %v %v %v\n%s", gotOrg, atts, err, ics)
	}
	if !strings.Contains(string(ics), "RSVP=TRUE") || !strings.Contains(string(ics), "SEQUENCE:0") {
		t.Fatalf("new attendee params:\n%s", ics)
	}

	// Bob ответил; затем Carol убрали, Dave добавили — ответ Bob сохраняется.
	reply := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:" + uidOf(t, ics) +
		"\r\nDTSTAMP:20261125T090000Z\r\nDTSTART:20261201T100000Z\r\nATTENDEE;PARTSTAT=ACCEPTED:mailto:bob@test.local\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	ics2, changed, err := ApplyReply(ics, []byte(reply), "Bob@test.local")
	if err != nil || !changed {
		t.Fatalf("apply reply: %v %v", changed, err)
	}
	if _, again, _ := ApplyReply(ics2, []byte(reply), "bob@test.local"); again {
		t.Fatal("same reply changed the object twice")
	}
	if _, spoof, _ := ApplyReply(ics, []byte(reply), "mallory@test.local"); spoof {
		t.Fatal("reply applied for another sender")
	}
	form, _ := FormEvent(ics2)
	form.Organizer = org
	form.Attendees = []Person{{Email: "bob@test.local"}, {Email: "dave@test.local"}}
	ics3, err := Build(ics2, form, now)
	if err != nil {
		t.Fatal(err)
	}
	_, atts, _ = Meeting(ics3)
	if len(atts) != 2 || atts[0].PartStat != "ACCEPTED" || atts[1].Email != "dave@test.local" || atts[1].PartStat != "NEEDS-ACTION" {
		t.Fatalf("after edit: %v", atts)
	}
	if !strings.Contains(string(ics3), "SEQUENCE:1") {
		t.Fatalf("sequence not bumped:\n%s", ics3)
	}

	req, err := Request(ics3, now)
	if err != nil || !strings.Contains(string(req), "METHOD:REQUEST") || strings.Contains(string(req), "VALARM") {
		t.Fatalf("request: %v\n%s", err, req)
	}
	cancel, err := Cancel(ics3, "", []string{"carol@test.local"}, false, now)
	if err != nil {
		t.Fatal(err)
	}
	cs := string(cancel)
	if !strings.Contains(cs, "METHOD:CANCEL") || !strings.Contains(cs, "STATUS:CANCELLED") ||
		!strings.Contains(cs, "carol@test.local") || strings.Contains(cs, "bob@test.local") {
		t.Fatalf("cancel for removed:\n%s", cs)
	}

	// Все участники убраны — организатор тоже.
	form.Attendees = []Person{}
	ics4, _ := Build(ics3, form, now)
	if o, a, _ := Meeting(ics4); o != nil || len(a) != 0 {
		t.Fatalf("no attendees: %v %v", o, a)
	}
	// nil — не трогать участников.
	form.Attendees = nil
	ics5, _ := Build(ics3, form, now)
	if _, a, _ := Meeting(ics5); len(a) != 2 {
		t.Fatalf("nil attendees changed: %v", a)
	}

	if _, err := Build(nil, Event{Summary: "x", Start: e.Start, End: e.End, Attendees: []Person{{Email: "not an address"}}}, now); err == nil {
		t.Fatal("bad attendee accepted")
	}
}

func TestCancelOccurrence(t *testing.T) {
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	msk, _ := time.LoadLocation("Europe/Moscow")
	e := Event{Summary: "Стендап", Start: time.Date(2026, 12, 1, 10, 0, 0, 0, msk), End: time.Date(2026, 12, 1, 10, 15, 0, 0, msk),
		TZ: "Europe/Moscow", RRule: "FREQ=DAILY;COUNT=5", Organizer: &Person{Email: "alice@test.local"}, Attendees: []Person{{Email: "bob@test.local"}}}
	ics, _ := Build(nil, e, now)
	ics, err := DeleteOccurrence(ics, uidOf(t, ics), "20261202T070000Z", now)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Cancel(ics, "20261202T070000Z", nil, false, now)
	if err != nil {
		t.Fatal(err)
	}
	s := string(c)
	if !strings.Contains(s, "RECURRENCE-ID;TZID=Europe/Moscow:20261202T100000") || strings.Contains(s, "RRULE") ||
		!strings.Contains(s, "SEQUENCE:1") || !strings.Contains(s, "BEGIN:VTIMEZONE") {
		t.Fatalf("occurrence cancel:\n%s", s)
	}
}

func uidOf(t *testing.T, ics []byte) string {
	t.Helper()
	e, err := FormEvent(ics)
	if err != nil {
		t.Fatal(err)
	}
	return e.UID
}
