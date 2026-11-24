// Package caldav — адаптер календарей по протоколу CalDAV (RFC 4791):
// Radicale, Nextcloud, SOGo, Baïkal и другие.
//
// Адаптер реализует только протокол (ext.CalendarSource); ядро ничего не
// кэширует и запрашивает события за нужный диапазон. Включается секцией:
//
//	adapters:
//	  caldav:
//	    enabled: true
//	    timeout: 30s
//	    presets:                       # календари, которые администратор подключает всем
//	      - id: mailbox
//	        name: Календарь
//	        url: http://127.0.0.1:5232/%u/   # %u — ящик, %l — имя до @, %d — домен
//	        username: "%u"
//	        password: session                # пароль ящика из сессии, или password_file
//	        readonly: false
//	        domains: []                      # пусто — для всех доменов
package caldav

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"

	"github.com/ibuben/squareball/adapters/internal/dav"
	"github.com/ibuben/squareball/ext"
)

const (
	maxObject    = 1 << 20 // один объект календаря
	maxCalendars = 50
	maxObjects   = 5000 // объектов в ответе на запрос диапазона
)

type Config struct {
	Enabled              bool          `yaml:"enabled"`
	AllowPrivateNetworks bool          `yaml:"allow_private_networks"`
	Timeout              time.Duration `yaml:"timeout"`
	Presets              []dav.Preset  `yaml:"presets"`
}

type Adapter struct {
	cfg Config
	hc  dav.Clients
}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Name() string { return "caldav" }

func (a *Adapter) Init(_ context.Context, node ext.ConfigNode, core ext.Core) error {
	cfg := Config{Timeout: 30 * time.Second}
	if err := node.Decode(&cfg); err != nil {
		return err
	}
	if cfg.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if err := dav.CheckPresets(cfg.Presets); err != nil {
		return err
	}
	a.cfg = cfg
	a.hc = dav.NewClients(cfg.Timeout, cfg.AllowPrivateNetworks, "webmail-caldav")
	core.SetCalendarSource(a)
	core.Logger().Info("caldav", "presets", len(cfg.Presets))
	return nil
}

// --- ext.CalendarSource ------------------------------------------------------------

func (a *Adapter) CalendarPresets(mailbox string) []ext.CalendarPreset {
	var out []ext.CalendarPreset
	for _, p := range a.cfg.Presets {
		if acc, ok := p.Account(mailbox); ok {
			out = append(out, ext.CalendarPreset{ID: p.ID, Name: p.Name, ReadOnly: p.ReadOnly, Account: acc})
		}
	}
	return out
}

func (a *Adapter) client(acc ext.ContactAccount, endpoint string) (*caldav.Client, error) {
	return caldav.NewClient(a.hc.For(acc), endpoint)
}

// DiscoverCalendars: адрес может указывать на сервер, принципала, набор
// календарей или сам календарь.
func (a *Adapter) DiscoverCalendars(ctx context.Context, acc ext.ContactAccount) ([]ext.RemoteCalendar, error) {
	base, err := url.Parse(acc.URL)
	if err != nil {
		return nil, ext.ErrContactNotFound
	}
	c, err := a.client(acc, acc.URL)
	if err != nil {
		return nil, err
	}
	var cals []caldav.Calendar
	var firstErr error
	try := func(p string) bool {
		list, err := c.FindCalendars(ctx, p)
		if err != nil {
			if firstErr == nil || errors.Is(err, ext.ErrContactAuth) {
				firstErr = err
			}
			return false
		}
		cals = withEvents(list)
		return len(cals) > 0
	}
	if !try(base.Path) && !errors.Is(firstErr, ext.ErrContactAuth) {
		for _, start := range []string{base.Path, "/.well-known/caldav", "/"} {
			ok, err := a.viaPrincipal(ctx, acc, base, start, try)
			if ok {
				break
			}
			if errors.Is(err, ext.ErrContactAuth) || errors.Is(err, ext.ErrContactForbidden) {
				firstErr = err
				break
			}
		}
	}
	if len(cals) == 0 {
		if firstErr != nil {
			return nil, dav.MapErr(firstErr)
		}
		return nil, ext.ErrContactNotFound
	}
	out := make([]ext.RemoteCalendar, 0, len(cals))
	for _, cl := range cals {
		if len(out) == maxCalendars {
			break
		}
		u := base.ResolveReference(&url.URL{Path: cl.Path})
		name := strings.TrimSpace(cl.Name)
		if name == "" {
			name = path.Base(strings.TrimSuffix(cl.Path, "/"))
		}
		out = append(out, ext.RemoteCalendar{URL: u.String(), Name: name})
	}
	return out, nil
}

// withEvents оставляет календари, в которых могут быть события (без набора
// компонентов — любые).
func withEvents(list []caldav.Calendar) []caldav.Calendar {
	var out []caldav.Calendar
	for _, c := range list {
		if len(c.SupportedComponentSet) == 0 || dav.ContainsFold(c.SupportedComponentSet, ical.CompEvent) {
			out = append(out, c)
		}
	}
	return out
}

func (a *Adapter) viaPrincipal(ctx context.Context, acc ext.ContactAccount, base *url.URL, start string, try func(string) bool) (bool, error) {
	c, err := a.client(acc, base.ResolveReference(&url.URL{Path: start}).String())
	if err != nil {
		return false, err
	}
	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil || principal == "" {
		return false, err
	}
	home, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil || home == "" {
		return false, err
	}
	return try(home), nil
}

func allData() caldav.CalendarCompRequest {
	return caldav.CalendarCompRequest{Name: ical.CompCalendar, AllProps: true, AllComps: true}
}

func (a *Adapter) Query(ctx context.Context, acc ext.ContactAccount, cal string, from, to time.Time) ([]ext.RemoteObject, error) {
	return a.query(ctx, acc, cal, caldav.CompFilter{Name: ical.CompEvent, Start: from.UTC(), End: to.UTC()})
}

func (a *Adapter) FindUID(ctx context.Context, acc ext.ContactAccount, cal, uid string) (ext.RemoteObject, error) {
	list, err := a.query(ctx, acc, cal, caldav.CompFilter{Name: ical.CompEvent, Props: []caldav.PropFilter{{
		Name: ical.PropUID, TextMatch: &caldav.TextMatch{Text: uid},
	}}})
	if err != nil {
		return ext.RemoteObject{}, err
	}
	for _, o := range list {
		if objectUID(o.ICS) == uid { // text-match — подстрока; сверяем точно
			return o, nil
		}
	}
	return ext.RemoteObject{}, ext.ErrContactNotFound
}

func (a *Adapter) query(ctx context.Context, acc ext.ContactAccount, cal string, f caldav.CompFilter) ([]ext.RemoteObject, error) {
	c, err := a.client(acc, cal)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(cal)
	if err != nil {
		return nil, ext.ErrContactNotFound
	}
	objs, err := c.QueryCalendar(ctx, u.Path, &caldav.CalendarQuery{
		CompRequest: allData(),
		CompFilter:  caldav.CompFilter{Name: ical.CompCalendar, Comps: []caldav.CompFilter{f}},
	})
	if err != nil {
		return nil, dav.MapErr(err)
	}
	out := make([]ext.RemoteObject, 0, len(objs))
	for _, o := range objs {
		if len(out) == maxObjects {
			break
		}
		if ro, ok := toRemote(o); ok {
			out = append(out, ro)
		}
	}
	return out, nil
}

func toRemote(o caldav.CalendarObject) (ext.RemoteObject, bool) {
	if o.Data == nil {
		return ext.RemoteObject{}, false
	}
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(o.Data); err != nil || buf.Len() > maxObject {
		return ext.RemoteObject{}, false
	}
	return ext.RemoteObject{Href: o.Path, ETag: dav.NormETag(o.ETag), ICS: buf.Bytes()}, true
}

func objectUID(ics []byte) string {
	c, err := ical.NewDecoder(bytes.NewReader(ics)).Decode()
	if err != nil {
		return ""
	}
	for _, ch := range c.Children {
		if ch.Name == ical.CompEvent {
			if p := ch.Props.Get(ical.PropUID); p != nil {
				return p.Value
			}
		}
	}
	return ""
}

func (a *Adapter) GetObject(ctx context.Context, acc ext.ContactAccount, cal, href string) (ext.RemoteObject, error) {
	c, err := a.client(acc, cal)
	if err != nil {
		return ext.RemoteObject{}, err
	}
	o, err := c.GetCalendarObject(ctx, href)
	if err != nil {
		return ext.RemoteObject{}, dav.MapErr(err)
	}
	ro, ok := toRemote(*o)
	if !ok {
		return ext.RemoteObject{}, ext.ErrContactNotFound
	}
	return ro, nil
}

func (a *Adapter) PutObject(ctx context.Context, acc ext.ContactAccount, cal, href, etag string, ics []byte) (string, string, error) {
	cu, err := url.Parse(cal)
	if err != nil {
		return "", "", ext.ErrContactNotFound
	}
	h := http.Header{"Content-Type": {"text/calendar; charset=utf-8"}}
	if href == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		href = strings.TrimSuffix(cu.Path, "/") + "/" + hex.EncodeToString(b) + ".ics"
		h.Set("If-None-Match", "*")
	} else if etag != "" {
		h.Set("If-Match", `"`+etag+`"`)
	}
	if !inside(cu.Path, href) {
		return "", "", ext.ErrContactNotFound
	}
	resp, err := a.hc.Do(ctx, acc, http.MethodPut, cu.ResolveReference(&url.URL{Path: href}).String(), h, ics)
	if err != nil {
		return "", "", err
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		if lu, err := url.Parse(loc); err == nil && lu.Path != "" {
			href = lu.Path
		}
	}
	return href, dav.NormETag(resp.Header.Get("ETag")), nil
}

func (a *Adapter) DeleteObject(ctx context.Context, acc ext.ContactAccount, cal, href, etag string) error {
	cu, err := url.Parse(cal)
	if err != nil || !inside(cu.Path, href) {
		return ext.ErrContactNotFound
	}
	h := http.Header{}
	if etag != "" {
		h.Set("If-Match", `"`+etag+`"`)
	}
	_, err = a.hc.Do(ctx, acc, http.MethodDelete, cu.ResolveReference(&url.URL{Path: href}).String(), h, nil)
	return err
}

// inside — href лежит прямо в коллекции cal: href приходит из браузера,
// и через него нельзя менять объекты в других коллекциях учётки.
func inside(cal, href string) bool {
	dir := strings.TrimSuffix(cal, "/") + "/"
	rest, ok := strings.CutPrefix(href, dir)
	return ok && rest != "" && !strings.Contains(rest, "/") && rest != "." && rest != ".."
}
