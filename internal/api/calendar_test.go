package api

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/calendar"
)

// memCal — календарь в памяти вместо CalDAV-сервера.
type memCal struct {
	mu   sync.Mutex
	objs map[string]ext.RemoteObject
	n    int
	acc  ext.ContactAccount
}

func (m *memCal) CalendarPresets(string) []ext.CalendarPreset {
	return []ext.CalendarPreset{{ID: "p", Name: "P", Account: ext.ContactAccount{URL: "http://127.0.0.1/u/", Password: ext.PasswordSession}}}
}
func (m *memCal) DiscoverCalendars(_ context.Context, acc ext.ContactAccount) ([]ext.RemoteCalendar, error) {
	m.acc = acc
	return []ext.RemoteCalendar{{URL: "http://127.0.0.1/u/cal/", Name: "Календарь"}}, nil
}
func (m *memCal) Query(_ context.Context, _ ext.ContactAccount, _ string, from, to time.Time) ([]ext.RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ext.RemoteObject
	for _, o := range m.objs {
		if occ, _ := calendar.Expand(o.ICS, from, to, time.UTC); len(occ) > 0 {
			out = append(out, o)
		}
	}
	return out, nil
}
func (m *memCal) GetObject(_ context.Context, _ ext.ContactAccount, _, href string) (ext.RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[href]
	if !ok {
		return o, ext.ErrContactNotFound
	}
	return o, nil
}
func (m *memCal) FindUID(_ context.Context, _ ext.ContactAccount, _, uid string) (ext.RemoteObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.objs {
		if e, err := calendar.FormEvent(o.ICS); err == nil && e.UID == uid {
			return o, nil
		}
	}
	return ext.RemoteObject{}, ext.ErrContactNotFound
}
func (m *memCal) PutObject(_ context.Context, _ ext.ContactAccount, _, href, etag string, ics []byte) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if href == "" {
		href = "/u/cal/" + strconv.Itoa(len(m.objs)+1) + ".ics"
	} else if etag != "" && m.objs[href].ETag != etag {
		return "", "", ext.ErrContactConflict
	}
	m.n++
	m.objs[href] = ext.RemoteObject{Href: href, ETag: "e" + strconv.Itoa(m.n), ICS: ics}
	return href, m.objs[href].ETag, nil
}
func (m *memCal) DeleteObject(_ context.Context, _ ext.ContactAccount, _, href, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objs[href]; !ok {
		return ext.ErrContactNotFound
	}
	delete(m.objs, href)
	return nil
}

func TestCalendarAPI(t *testing.T) {
	srv := newServer(t, testConfig(t))
	src := &memCal{objs: map[string]ext.RemoteObject{}}
	srv.SetCalendarSource(src)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	sess, token, err := srv.sessions.Create("bob@example.com", "pw", ext.AuthPassword, "", "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(ts.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: srv.cfg.Session.CookieName, Value: token, Path: "/"}})
	c := &client{t: t, base: ts.URL, hc: &http.Client{Jar: jar}, csrf: sess.CSRF}

	st, body := c.do("GET", "/api/v1/calendars", nil)
	cals, _ := body["calendars"].([]any)
	if st != 200 || body["enabled"] != true || len(cals) != 1 || src.acc.Password != "pw" {
		t.Fatalf("calendars: %d %v (password %q)", st, body, src.acc.Password)
	}
	cal := cals[0].(map[string]any)["id"].(string)
	if strings.Contains(cal, "127.0.0.1") {
		t.Fatal("calendar id leaks URL")
	}

	ev := map[string]any{"summary": "Обед", "start": "2026-12-01T12:00:00+03:00", "end": "2026-12-01T13:00:00+03:00",
		"tz": "Europe/Moscow", "rrule": "FREQ=DAILY;COUNT=3"}
	st, body = c.do("POST", "/api/v1/calendar/events", map[string]any{"cal": cal, "event": ev})
	if st != 200 || body["href"] == "" {
		t.Fatalf("create: %d %v", st, body)
	}
	href, etag := body["href"].(string), body["etag"].(string)

	q := "/api/v1/calendar/events?tz=Europe/Moscow&from=" + url.QueryEscape("2026-12-01T00:00:00+03:00") + "&to=" + url.QueryEscape("2026-12-08T00:00:00+03:00")
	st, body = c.do("GET", q, nil)
	events, _ := body["events"].([]any)
	if st != 200 || len(events) != 3 {
		t.Fatalf("events: %d %v", st, body)
	}
	second := events[1].(map[string]any)
	if second["start"] != "2026-12-02T12:00:00+03:00" || second["recurrence_id"] == "" || second["cal"] != cal {
		t.Fatalf("occurrence: %v", second)
	}

	st, body = c.do("GET", "/api/v1/calendar/event?cal="+cal+"&href="+url.QueryEscape(href), nil)
	form, _ := body["event"].(map[string]any)
	if st != 200 || form["summary"] != "Обед" || form["tz"] != "Europe/Moscow" || form["rrule"] != "FREQ=DAILY;COUNT=3" {
		t.Fatalf("get: %d %v", st, body)
	}
	form["summary"] = "Обед с командой"
	st, body = c.do("POST", "/api/v1/calendar/events", map[string]any{"cal": cal, "href": href, "etag": "stale", "event": form})
	if st != 409 {
		t.Fatalf("stale etag: %d %v", st, body)
	}
	st, body = c.do("POST", "/api/v1/calendar/events", map[string]any{"cal": cal, "href": href, "etag": etag, "event": form})
	if st != 200 {
		t.Fatalf("update: %d %v", st, body)
	}

	st, _ = c.do("POST", "/api/v1/calendar/events/delete", map[string]any{"cal": cal, "href": href, "uid": form["uid"], "recurrence_id": second["recurrence_id"]})
	if st != 200 {
		t.Fatalf("delete occurrence: %d", st)
	}
	_, body = c.do("GET", q, nil)
	if events, _ = body["events"].([]any); len(events) != 2 || events[0].(map[string]any)["summary"] != "Обед с командой" {
		t.Fatalf("after occurrence delete: %v", body)
	}
	st, _ = c.do("POST", "/api/v1/calendar/events/delete", map[string]any{"cal": cal, "href": href})
	if _, body = c.do("GET", q, nil); st != 200 || len(body["events"].([]any)) != 0 {
		t.Fatalf("after delete: %d %v", st, body)
	}

	for _, bad := range []string{
		"/api/v1/calendar/events?from=x&to=y",
		"/api/v1/calendar/events?from=2026-01-01T00:00:00Z&to=2028-01-01T00:00:00Z",
	} {
		if st, _ := c.do("GET", bad, nil); st != 400 {
			t.Fatalf("%s: %d", bad, st)
		}
	}
	if st, _ := c.do("POST", "/api/v1/calendar/events", map[string]any{"cal": "nope", "event": ev}); st != 404 {
		t.Fatalf("unknown calendar: %d", st)
	}
}
