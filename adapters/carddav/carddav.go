// Package carddav — адаптер внешних адресных книг по протоколу CardDAV
// (RFC 6352): Nextcloud, Radicale, SOGo, Baïkal, iCloud и другие.
//
// Адаптер реализует только протокол (ext.ContactSource); учётки, кэш
// карточек и синхронизацию ведёт ядро. Включается секцией конфига:
//
//	adapters:
//	  carddav:
//	    enabled: true
//	    allow_user_accounts: true      # пользователь может подключать свои серверы
//	    allow_private_networks: false  # пускать ли пользовательские учётки во внутренние сети
//	    timeout: 30s
//	    presets:                       # книги, которые администратор подключает всем
//	      - id: company
//	        name: Контакты компании
//	        url: https://dav.example.com/%u/   # %u — ящик, %l — имя до @, %d — домен
//	        username: "%u"
//	        password: session                  # пароль ящика из сессии, или password_file
//	        readonly: false
//	        domains: [example.com]             # пусто — для всех доменов
package carddav

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

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"

	"github.com/ibuben/squareball/adapters/internal/dav"
	"github.com/ibuben/squareball/ext"
)

const (
	maxCard  = 1 << 20 // одна карточка
	maxBooks = 50
)

type Preset = dav.Preset

type Config struct {
	Enabled              bool          `yaml:"enabled"`
	AllowUserAccounts    bool          `yaml:"allow_user_accounts"`
	AllowPrivateNetworks bool          `yaml:"allow_private_networks"`
	Timeout              time.Duration `yaml:"timeout"`
	Presets              []Preset      `yaml:"presets"`
}

type Adapter struct {
	cfg Config
	hc  dav.Clients
}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Name() string { return "carddav" }

func (a *Adapter) Init(_ context.Context, node ext.ConfigNode, core ext.Core) error {
	cfg := Config{AllowUserAccounts: true, Timeout: 30 * time.Second}
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
	a.hc = dav.NewClients(cfg.Timeout, cfg.AllowPrivateNetworks, "webmail-carddav")
	core.SetContactSource(a)
	core.Logger().Info("carddav", "presets", len(cfg.Presets), "user_accounts", cfg.AllowUserAccounts)
	return nil
}

// --- ext.ContactSource -------------------------------------------------------------

func (a *Adapter) AllowUserAccounts() bool { return a.cfg.AllowUserAccounts }

func (a *Adapter) Presets(mailbox string) []ext.ContactPreset {
	var out []ext.ContactPreset
	for _, p := range a.cfg.Presets {
		if acc, ok := p.Account(mailbox); ok {
			out = append(out, ext.ContactPreset{ID: p.ID, Name: p.Name, ReadOnly: p.ReadOnly, Account: acc})
		}
	}
	return out
}

func (a *Adapter) client(acc ext.ContactAccount, endpoint string) (*carddav.Client, error) {
	return carddav.NewClient(a.hc.For(acc), endpoint)
}

// Discover: адрес может указывать на сервер, принципала, набор книг или саму книгу.
func (a *Adapter) Discover(ctx context.Context, acc ext.ContactAccount) ([]ext.RemoteBook, error) {
	base, err := url.Parse(acc.URL)
	if err != nil {
		return nil, ext.ErrContactNotFound
	}
	c, err := a.client(acc, acc.URL)
	if err != nil {
		return nil, err
	}
	var books []carddav.AddressBook
	var firstErr error
	try := func(p string) bool {
		list, err := c.FindAddressBooks(ctx, p)
		if err != nil {
			if firstErr == nil || errors.Is(err, ext.ErrContactAuth) {
				firstErr = err
			}
			return false
		}
		books = list
		return len(list) > 0
	}
	// 1) сама книга или набор книг; 2) через принципала; 3) .well-known.
	if !try(base.Path) && !errors.Is(firstErr, ext.ErrContactAuth) {
		for _, start := range []string{base.Path, "/.well-known/carddav", "/"} {
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
	if len(books) == 0 {
		if firstErr != nil {
			return nil, mapErr(firstErr)
		}
		return nil, ext.ErrContactNotFound
	}
	out := make([]ext.RemoteBook, 0, len(books))
	for _, b := range books {
		if len(out) == maxBooks {
			break
		}
		u := base.ResolveReference(&url.URL{Path: b.Path})
		name := strings.TrimSpace(b.Name)
		if name == "" {
			name = path.Base(strings.TrimSuffix(b.Path, "/"))
		}
		out = append(out, ext.RemoteBook{URL: u.String(), Name: name})
	}
	return out, nil
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
	home, err := c.FindAddressBookHomeSet(ctx, principal)
	if err != nil || home == "" {
		return false, err
	}
	return try(home), nil
}

func (a *Adapter) List(ctx context.Context, acc ext.ContactAccount, book string) (map[string]string, error) {
	c, err := a.client(acc, book)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(book)
	files, err := c.ReadDir(ctx, u.Path, false)
	if err != nil {
		return nil, mapErr(err)
	}
	out := map[string]string{}
	self := strings.TrimSuffix(u.Path, "/")
	for _, f := range files {
		if f.IsDir || strings.TrimSuffix(f.Path, "/") == self {
			continue
		}
		out[f.Path] = normETag(f.ETag)
	}
	return out, nil
}

func (a *Adapter) Get(ctx context.Context, acc ext.ContactAccount, book string, hrefs []string) ([]ext.RemoteCard, error) {
	if len(hrefs) == 0 {
		return nil, nil
	}
	c, err := a.client(acc, book)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(book)
	objs, err := c.MultiGetAddressBook(ctx, u.Path, &carddav.AddressBookMultiGet{
		Paths: hrefs, DataRequest: carddav.AddressDataRequest{AllProp: true},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]ext.RemoteCard, 0, len(objs))
	for _, o := range objs {
		var buf bytes.Buffer
		if err := vcard.NewEncoder(&buf).Encode(o.Card); err != nil || buf.Len() > maxCard {
			continue
		}
		out = append(out, ext.RemoteCard{Href: o.Path, ETag: normETag(o.ETag), VCard: buf.Bytes()})
	}
	return out, nil
}

func (a *Adapter) Put(ctx context.Context, acc ext.ContactAccount, book, href, etag string, card []byte) (string, string, error) {
	bu, err := url.Parse(book)
	if err != nil {
		return "", "", ext.ErrContactNotFound
	}
	h := http.Header{"Content-Type": {"text/vcard; charset=utf-8"}}
	if href == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		href = strings.TrimSuffix(bu.Path, "/") + "/" + hex.EncodeToString(b) + ".vcf"
		h.Set("If-None-Match", "*")
	} else if etag != "" {
		h.Set("If-Match", `"`+etag+`"`)
	}
	resp, err := a.hc.Do(ctx, acc, http.MethodPut, bu.ResolveReference(&url.URL{Path: href}).String(), h, card)
	if err != nil {
		return "", "", err
	}
	newTag := normETag(resp.Header.Get("ETag"))
	if loc := resp.Header.Get("Location"); loc != "" {
		if lu, err := url.Parse(loc); err == nil && lu.Path != "" {
			href = lu.Path
		}
	}
	return href, newTag, nil
}

func (a *Adapter) Delete(ctx context.Context, acc ext.ContactAccount, book, href, etag string) error {
	bu, err := url.Parse(book)
	if err != nil {
		return ext.ErrContactNotFound
	}
	h := http.Header{}
	if etag != "" {
		h.Set("If-Match", `"`+etag+`"`)
	}
	_, err = a.hc.Do(ctx, acc, http.MethodDelete, bu.ResolveReference(&url.URL{Path: href}).String(), h, nil)
	return err
}

// Псевдонимы общих функций пакета dav.
var (
	mapErr        = dav.MapErr
	normETag      = dav.NormETag
	publicIP      = dav.PublicIP
	newHTTPClient = dav.NewHTTPClient
)
