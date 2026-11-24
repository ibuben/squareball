package mail

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"

	"github.com/ibuben/squareball/internal/calendar"
)

func TestBuildInviteMail(t *testing.T) {
	tash, _ := time.LoadLocation("Asia/Tashkent")
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	obj, err := calendar.Build(nil, calendar.Event{Summary: "Планёрка", Location: "Офис", Description: "Повестка",
		Start: time.Date(2026, 12, 1, 10, 0, 0, 0, tash), End: time.Date(2026, 12, 1, 11, 0, 0, 0, tash), TZ: "Asia/Tashkent",
		Organizer: &calendar.Person{Name: "Alice", Email: "alice@test.local"}, Attendees: []calendar.Person{{Name: "Bob", Email: "bob@test.local"}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := calendar.Request(obj, now)
	raw, err := BuildInviteMail(InviteMail{ICS: req, From: Address{Name: "Alice", Email: "alice@test.local"},
		To: []Address{{Name: "Bob", Email: "bob@test.local"}}, Lang: "ru"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if subj, _ := (&gomail.Header{Header: e.Header}).Subject(); subj != "Приглашение: Планёрка" {
		t.Errorf("subject %q", subj)
	}
	var text, cal, att string
	_ = e.Walk(func(_ []int, p *message.Entity, _ error) error {
		ct, params, _ := p.Header.ContentType()
		if strings.HasPrefix(ct, "multipart/") {
			return nil
		}
		b, _ := io.ReadAll(p.Body)
		switch {
		case ct == "text/plain":
			text = string(b)
		case ct == "text/calendar" && params["method"] == "REQUEST":
			cal = string(b)
		case ct == "application/ics":
			att = string(b)
		}
		return nil
	})
	if !strings.Contains(text, "01.12.2026 10:00–11:00 (Asia/Tashkent)") || !strings.Contains(text, "Где: Офис") ||
		!strings.Contains(text, "Bob <bob@test.local>") || !strings.Contains(text, "Повестка") {
		t.Errorf("text:\n%s", text)
	}
	if cal == "" || cal != att {
		t.Fatalf("calendar parts: %q / %q", cal, att)
	}
	inv, err := ParseInvite([]byte(cal))
	if err != nil || inv.Method != "REQUEST" || inv.Organizer == nil || len(inv.Attendees) != 1 {
		t.Fatalf("invite: %+v %v", inv, err)
	}

	cancel, _ := calendar.Cancel(obj, "", nil, true, now)
	raw, err = BuildInviteMail(InviteMail{ICS: cancel, From: Address{Email: "alice@test.local"}, To: []Address{{Email: "bob@test.local"}}})
	if err != nil {
		t.Fatal(err)
	}
	e, _ = message.Read(bytes.NewReader(raw))
	if subj, _ := (&gomail.Header{Header: e.Header}).Subject(); subj != "Cancelled: Планёрка" {
		t.Errorf("cancel subject %q", subj)
	}
	if _, err := BuildInviteMail(InviteMail{ICS: req, From: Address{Email: "alice@test.local"}}); err == nil {
		t.Error("no recipients accepted")
	}
}
