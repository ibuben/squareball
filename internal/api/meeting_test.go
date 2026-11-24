package api

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/config"
	"github.com/ibuben/squareball/internal/mail"
)

type sentMail struct {
	from  string
	rcpts []string
	raw   string
}

func TestCalendarMeeting(t *testing.T) {
	srv := newServer(t, testConfig(t))
	src := &memCal{objs: map[string]ext.RemoteObject{}}
	srv.SetCalendarSource(src)
	var mu sync.Mutex
	var sent []sentMail
	srv.sendMail = func(_ *config.SMTP, cr mail.Creds, from string, rcpts []string, raw []byte, _ bool) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, sentMail{from, rcpts, string(raw)})
		return nil
	}
	take := func() []sentMail {
		mu.Lock()
		defer mu.Unlock()
		out := sent
		sent = nil
		return out
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	sess, token, err := srv.sessions.Create("alice@example.com", "pw", ext.AuthPassword, "", "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(ts.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: srv.cfg.Session.CookieName, Value: token, Path: "/"}})
	c := &client{t: t, base: ts.URL, hc: &http.Client{Jar: jar}, csrf: sess.CSRF}
	_, body := c.do("GET", "/api/v1/calendars", nil)
	cal := body["calendars"].([]any)[0].(map[string]any)["id"].(string)

	ev := map[string]any{"summary": "Планёрка", "start": "2026-12-01T10:00:00+05:00", "end": "2026-12-01T11:00:00+05:00",
		"tz": "Asia/Tashkent", "attendees": []map[string]string{{"name": "Bob", "email": "bob@example.com"}, {"email": "alice@example.com"}}}
	st, body := c.do("POST", "/api/v1/calendar/events", map[string]any{"cal": cal, "event": ev, "notify": true})
	if st != 200 || body["sent"] != float64(1) || body["notify_error"] != nil {
		t.Fatalf("create: %d %v", st, body)
	}
	href := body["href"].(string)
	m := take()
	if len(m) != 1 || m[0].from != "alice@example.com" || strings.Join(m[0].rcpts, ",") != "bob@example.com" ||
		!strings.Contains(m[0].raw, "method=REQUEST") {
		t.Fatalf("request mail: %+v", m)
	}

	st, body = c.do("GET", "/api/v1/calendar/event?cal="+cal+"&href="+url.QueryEscape(href), nil)
	form := body["event"].(map[string]any)
	org, _ := form["organizer"].(map[string]any)
	atts, _ := form["attendees"].([]any)
	if st != 200 || org["email"] != "alice@example.com" || len(atts) != 1 {
		t.Fatalf("form: %v", form)
	}

	// Состав изменился: Bob получает CANCEL, Carol — REQUEST.
	form["attendees"] = []map[string]string{{"email": "carol@example.com"}}
	form["organizer"] = map[string]string{"email": "mallory@example.com"} // от клиента не принимается
	st, body = c.do("POST", "/api/v1/calendar/events", map[string]any{"cal": cal, "href": href, "etag": body["etag"], "event": form, "notify": true})
	if st != 200 || body["sent"] != float64(2) {
		t.Fatalf("update: %d %v", st, body)
	}
	m = take()
	if len(m) != 2 || m[0].rcpts[0] != "carol@example.com" || m[1].rcpts[0] != "bob@example.com" || !strings.Contains(m[1].raw, "method=CANCEL") {
		t.Fatalf("update mails: %+v", m)
	}
	if strings.Contains(string(src.objs[href].ICS), "mallory") {
		t.Fatal("organizer taken from client")
	}

	// Без notify писем нет.
	_, body = c.do("GET", "/api/v1/calendar/event?cal="+cal+"&href="+url.QueryEscape(href), nil)
	st, _ = c.do("POST", "/api/v1/calendar/events", map[string]any{"cal": cal, "href": href, "etag": body["etag"], "event": body["event"]})
	if st != 200 || len(take()) != 0 {
		t.Fatalf("save without notify sent mail")
	}

	// Удаление с notify — отмена Carol.
	_, body = c.do("GET", "/api/v1/calendar/event?cal="+cal+"&href="+url.QueryEscape(href), nil)
	st, body = c.do("POST", "/api/v1/calendar/events/delete", map[string]any{"cal": cal, "href": href, "etag": body["etag"], "notify": true})
	m = take()
	if st != 200 || len(m) != 1 || m[0].rcpts[0] != "carol@example.com" || !strings.Contains(m[0].raw, "method=CANCEL") {
		t.Fatalf("delete: %d %v %+v", st, body, m)
	}

	// Чужая встреча: участников не меняем и ничего не рассылаем.
	foreign := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:f1@x\r\nDTSTAMP:20261125T090000Z\r\n" +
		"DTSTART:20261201T050000Z\r\nDTEND:20261201T060000Z\r\nSUMMARY:Чужая\r\nORGANIZER:mailto:zed@other.org\r\n" +
		"ATTENDEE;PARTSTAT=ACCEPTED:mailto:alice@example.com\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	src.objs["/u/cal/f.ics"] = ext.RemoteObject{Href: "/u/cal/f.ics", ETag: "f", ICS: []byte(foreign)}
	_, body = c.do("GET", "/api/v1/calendar/event?cal="+cal+"&href="+url.QueryEscape("/u/cal/f.ics"), nil)
	form = body["event"].(map[string]any)
	form["attendees"] = []map[string]string{{"email": "evil@example.com"}}
	st, body = c.do("POST", "/api/v1/calendar/events", map[string]any{"cal": cal, "href": "/u/cal/f.ics", "etag": "f", "event": form, "notify": true})
	if st != 200 || len(take()) != 0 || strings.Contains(string(src.objs["/u/cal/f.ics"].ICS), "evil") {
		t.Fatalf("foreign meeting: %d %v", st, body)
	}
}
