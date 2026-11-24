package caldav

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ibuben/squareball/adapters/internal/dav"
	"github.com/ibuben/squareball/ext"
)

func TestPresets(t *testing.T) {
	a := &Adapter{cfg: Config{Presets: []dav.Preset{
		{ID: "mailbox", Name: "Календарь", URL: "http://127.0.0.1:5232/%u/", Username: "%u", Password: "session"},
		{ID: "corp", URL: "https://dav.corp/%d/", Username: "svc", Secret: "s3", Domains: []string{"corp.test"}},
	}}}
	got := a.CalendarPresets("bob@example.com")
	if len(got) != 1 || got[0].Account.URL != "http://127.0.0.1:5232/bob@example.com/" || got[0].Account.Password != ext.PasswordSession || !got[0].Account.Trusted {
		t.Fatalf("%+v", got)
	}
	if got := a.CalendarPresets("x@corp.test"); len(got) != 2 || got[1].Account.Password != "s3" {
		t.Fatalf("%+v", got)
	}
}

func TestSharedPreset(t *testing.T) {
	a := &Adapter{cfg: Config{Presets: []dav.Preset{{ID: "shared", URL: "http://127.0.0.1:5232/domain/%d/", Username: "%u", Password: "session",
		Collection: true, Domains: []string{"ex.uz"},
		Writers: map[string][]string{"domain/ex.uz/team": {"Bob@ex.uz"}, "domain/ex.uz/all": {"*"}, "domain/ex.uz/ro": {}}}}}}
	got := a.CalendarPresets("bob@ex.uz")
	if len(got) != 1 || !got[0].Account.Collection || got[0].Account.URL != "http://127.0.0.1:5232/domain/ex.uz/" || got[0].CanWrite == nil {
		t.Fatalf("%+v", got)
	}
	w := got[0].CanWrite
	for u, ok := range map[string]bool{
		"http://127.0.0.1:5232/domain/ex.uz/team/": true, "http://127.0.0.1:5232/domain/ex.uz/all/": true,
		"http://127.0.0.1:5232/domain/ex.uz/ro/": false, "http://127.0.0.1:5232/domain/ex.uz/other/": false,
	} {
		if w(u) != ok {
			t.Errorf("bob %s: %v", u, !ok)
		}
	}
	if a.CalendarPresets("eve@ex.uz")[0].CanWrite("http://127.0.0.1:5232/domain/ex.uz/team/") {
		t.Error("eve may not write team")
	}
	if a.CalendarPresets("x@other.uz") != nil {
		t.Error("other domain")
	}
	plain := (&Adapter{cfg: Config{Presets: []dav.Preset{{ID: "m", URL: "http://h/%u/", Password: "session"}}}}).CalendarPresets("a@b")
	if plain[0].CanWrite != nil || plain[0].Account.Collection {
		t.Error("plain preset must not limit writes")
	}
}

func TestInside(t *testing.T) {
	for href, ok := range map[string]bool{
		"/u/calendar/a.ics": true, "/u/calendar/": false, "/u/other/a.ics": false,
		"/u/calendar/../other/a.ics": false, "/u/calendar/x/a.ics": false,
	} {
		if inside("/u/calendar/", href) != ok {
			t.Errorf("inside(%q) != %v", href, ok)
		}
	}
}

// С Radicale стенда (deploy/devstand): alice / Test12345!. Другой сервер или
// ящик: WEBMAIL_IT_CALDAV_URL, _USER, _PASS.
func TestRadicale(t *testing.T) {
	if os.Getenv("WEBMAIL_IT") != "1" {
		t.Skip("set WEBMAIL_IT=1 with Radicale on 127.0.0.1:5232")
	}
	env := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	ctx := context.Background()
	a := &Adapter{hc: dav.Clients{Public: dav.NewHTTPClient(10*time.Second, true), Trusted: dav.NewHTTPClient(10*time.Second, true), Agent: "test"}}
	acc := ext.ContactAccount{URL: env("WEBMAIL_IT_CALDAV_URL", "http://127.0.0.1:5232/"), Username: env("WEBMAIL_IT_CALDAV_USER", "alice"), Password: env("WEBMAIL_IT_CALDAV_PASS", "Test12345!")}
	if _, err := a.DiscoverCalendars(ctx, ext.ContactAccount{URL: acc.URL, Username: acc.Username, Password: "bad"}); !errors.Is(err, ext.ErrContactAuth) {
		t.Fatalf("bad password: %v", err)
	}
	cals, err := a.DiscoverCalendars(ctx, acc)
	if err != nil || len(cals) == 0 {
		t.Fatalf("discover: %+v %v", cals, err)
	}
	cal := cals[0].URL
	uid := "it-" + time.Now().Format("150405.000000") + "@webmail"
	ics := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//t//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260101T000000Z\r\nSUMMARY:Тест\r\nDTSTART:20300105T090000Z\r\nDTEND:20300105T100000Z\r\nRRULE:FREQ=WEEKLY;COUNT=4\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	href, _, err := a.PutObject(ctx, acc, cal, "", "", ics)
	if err != nil || href == "" {
		t.Fatalf("put: %q %v", href, err)
	}
	defer func() { _ = a.DeleteObject(ctx, acc, cal, href, "") }()

	// Третье вхождение (19 января) попадает в диапазон, хотя DTSTART раньше.
	objs, err := a.Query(ctx, acc, cal, time.Date(2030, 1, 18, 0, 0, 0, 0, time.UTC), time.Date(2030, 1, 20, 0, 0, 0, 0, time.UTC))
	if err != nil || len(objs) != 1 || objs[0].Href != href || !strings.Contains(string(objs[0].ICS), "Тест") {
		t.Fatalf("query: %+v %v", objs, err)
	}
	if objs, _ := a.Query(ctx, acc, cal, time.Date(2030, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2030, 3, 2, 0, 0, 0, 0, time.UTC)); len(objs) != 0 {
		t.Fatalf("query outside: %+v", objs)
	}
	o, err := a.FindUID(ctx, acc, cal, uid)
	if err != nil || o.Href != href || o.ETag == "" {
		t.Fatalf("find uid: %+v %v", o, err)
	}
	if _, err := a.FindUID(ctx, acc, cal, "nope-"+uid); !errors.Is(err, ext.ErrContactNotFound) {
		t.Fatalf("find missing uid: %v", err)
	}
	g, err := a.GetObject(ctx, acc, cal, href)
	if err != nil || g.ETag != o.ETag {
		t.Fatalf("get: %+v %v", g, err)
	}
	if _, _, err := a.PutObject(ctx, acc, cal, href, "stale", ics); !errors.Is(err, ext.ErrContactConflict) {
		t.Fatalf("stale etag: %v", err)
	}
	if _, _, err := a.PutObject(ctx, acc, cal, href, o.ETag, []byte(strings.Replace(string(ics), "Тест", "Тест 2", 1))); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := a.DeleteObject(ctx, acc, cal, href, ""); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := a.DeleteObject(ctx, acc, cal, href, ""); !errors.Is(err, ext.ErrContactNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}
