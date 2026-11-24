package api

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ibuben/squareball/internal/mail"
)

func itLogin(t *testing.T, base, user string) *client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: base, hc: &http.Client{Jar: jar}}
	st, me := c.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": "Test12345!"})
	if st != 200 {
		t.Fatalf("login %s: %d %v", user, st, me)
	}
	c.csrf = me["csrf"].(string)
	return c
}

func waitMessage(c *client, folder, q string) map[string]any {
	for i := 0; i < 50; i++ {
		_, page := c.do("GET", "/api/v1/messages?folder="+url.QueryEscape(folder)+"&q="+url.QueryEscape(q), nil)
		if list, _ := page["messages"].([]any); len(list) > 0 {
			return list[0].(map[string]any)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// Alice приглашает Bob; Bob видит карточку и принимает — Alice получает REPLY.
func TestIntegrationInvite(t *testing.T) {
	if os.Getenv("WEBMAIL_IT") != "1" {
		t.Skip("set WEBMAIL_IT=1 with deploy/devstand running")
	}
	cfg := testConfig(t)
	s := newServer(t, cfg)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	tag := "IT-invite " + strconv.FormatInt(time.Now().UnixNano(), 10)
	cal := strings.Join([]string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//test//EN", "METHOD:REQUEST",
		"BEGIN:VEVENT", "UID:" + strings.ReplaceAll(tag, " ", "-") + "@test.local", "SEQUENCE:0",
		"DTSTAMP:20261124T080000Z", "DTSTART;TZID=Asia/Tashkent:20261201T100000", "DTEND;TZID=Asia/Tashkent:20261201T110000",
		"SUMMARY:" + tag, "LOCATION:Офис",
		"ORGANIZER;CN=Alice:mailto:alice@test.local",
		"ATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE;CN=Bob:mailto:bob@test.local",
		"END:VEVENT", "END:VCALENDAR", "",
	}, "\r\n")
	raw := "From: Alice <alice@test.local>\r\nTo: bob@test.local\r\nSubject: " + tag +
		"\r\nMessage-ID: <" + strconv.FormatInt(time.Now().UnixNano(), 10) + "@test.local>\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=b1\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nПриглашение\r\n" +
		"--b1\r\nContent-Type: text/calendar; charset=utf-8; method=REQUEST\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + cal +
		"--b1--\r\n"
	if err := mail.Send(&cfg.SMTP, mail.Creds{Username: "alice@test.local", Password: "Test12345!"}, "alice@test.local",
		[]string{"bob@test.local"}, []byte(raw), false); err != nil {
		t.Fatalf("send invite: %v", err)
	}

	bob := itLogin(t, ts.URL, "bob")
	sum := waitMessage(bob, "INBOX", tag)
	if sum == nil {
		t.Fatal("invite did not arrive")
	}
	uid := int(sum["uid"].(float64))
	_, view := bob.do("GET", "/api/v1/message?folder=INBOX&uid="+itoa(uid), nil)
	inv, _ := view["message"].(map[string]any)["invite"].(map[string]any)
	if inv == nil || inv["method"] != "REQUEST" || inv["summary"] != tag || inv["start"] != "2026-12-01T10:00:00+05:00" {
		t.Fatalf("invite: %v", inv)
	}
	iv, _ := view["invite"].(map[string]any)
	if iv == nil || iv["can_reply"] != true || iv["attendee"] != true || iv["self"] != "bob@test.local" || iv["replied"] != "" {
		t.Fatalf("invite view: %v", iv)
	}

	req := map[string]any{"folder": "INBOX", "uid": uid, "partstat": "accepted", "comment": "Буду"}
	if st, b := bob.do("POST", "/api/v1/messages/invite-reply", req); st != 200 || b["to"] != "alice@test.local" {
		t.Fatalf("reply: %d %v", st, b)
	}
	_, view = bob.do("GET", "/api/v1/message?folder=INBOX&uid="+itoa(uid), nil)
	if iv := view["invite"].(map[string]any); iv["replied"] != "ACCEPTED" {
		t.Errorf("replied: %v", iv)
	}
	bad := map[string]any{"folder": "INBOX", "uid": uid, "partstat": "MAYBE"}
	if st, _ := bob.do("POST", "/api/v1/messages/invite-reply", bad); st != 400 {
		t.Errorf("bad partstat: %d", st)
	}

	alice := itLogin(t, ts.URL, "alice")
	got := waitMessage(alice, "INBOX", "Accepted: "+tag)
	if got == nil {
		t.Fatal("REPLY did not arrive to organizer")
	}
	_, rv := alice.do("GET", "/api/v1/message?folder=INBOX&uid="+itoa(int(got["uid"].(float64))), nil)
	rinv, _ := rv["message"].(map[string]any)["invite"].(map[string]any)
	if rinv == nil || rinv["method"] != "REPLY" {
		t.Fatalf("reply invite: %v", rinv)
	}
	atts := rinv["attendees"].([]any)
	if len(atts) != 1 || atts[0].(map[string]any)["partstat"] != "ACCEPTED" {
		t.Errorf("reply attendees: %v", atts)
	}
	if civ := rv["invite"].(map[string]any); civ["can_reply"] != false || civ["is_organizer"] != true {
		t.Errorf("organizer view: %v", civ)
	}
}
