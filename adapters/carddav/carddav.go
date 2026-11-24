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
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"

	"github.com/ibuben/squareball/ext"
)

const (
	maxResponse = 64 << 20 // ответ сервера, байт
	maxCard     = 1 << 20  // одна карточка
	maxBooks    = 50
)

type Preset struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	URL          string   `yaml:"url"`
	Username     string   `yaml:"username"`
	Password     string   `yaml:"password"` // "session" — пароль ящика
	PasswordFile string   `yaml:"password_file"`
	ReadOnly     bool     `yaml:"readonly"`
	Domains      []string `yaml:"domains"`

	secret string
}

type Config struct {
	Enabled              bool          `yaml:"enabled"`
	AllowUserAccounts    bool          `yaml:"allow_user_accounts"`
	AllowPrivateNetworks bool          `yaml:"allow_private_networks"`
	Timeout              time.Duration `yaml:"timeout"`
	Presets              []Preset      `yaml:"presets"`
}

type Adapter struct {
	cfg     Config
	trusted *http.Client // пресеты: адреса из конфига администратора
	public  *http.Client // учётки пользователей: без внутренних сетей
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
	ids := map[string]bool{}
	for i := range cfg.Presets {
		p := &cfg.Presets[i]
		if p.ID == "" || ids[p.ID] {
			return fmt.Errorf("presets[%d].id: required and unique", i)
		}
		ids[p.ID] = true
		if u, err := url.Parse(strings.NewReplacer("%u", "u", "%l", "l", "%d", "d").Replace(p.URL)); err != nil ||
			(u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("presets[%d].url: must be http(s)://host/...", i)
		}
		switch {
		case p.Password == "session" && p.PasswordFile == "":
		case p.Password == "" && p.PasswordFile != "":
			b, err := os.ReadFile(p.PasswordFile)
			if err != nil {
				return fmt.Errorf("presets[%d].password_file: %w", i, err)
			}
			p.secret = strings.TrimRight(string(b), "\r\n")
		default:
			return fmt.Errorf("presets[%d]: set password: session or password_file", i)
		}
		if p.Name == "" {
			p.Name = p.ID
		}
	}
	a.cfg = cfg
	a.trusted = newHTTPClient(cfg.Timeout, true)
	a.public = newHTTPClient(cfg.Timeout, cfg.AllowPrivateNetworks)
	core.SetContactSource(a)
	core.Logger().Info("carddav", "presets", len(cfg.Presets), "user_accounts", cfg.AllowUserAccounts)
	return nil
}

// --- ext.ContactSource -------------------------------------------------------------

func (a *Adapter) AllowUserAccounts() bool { return a.cfg.AllowUserAccounts }

func (a *Adapter) Presets(mailbox string) []ext.ContactPreset {
	local, domain, _ := strings.Cut(mailbox, "@")
	r := strings.NewReplacer("%u", mailbox, "%l", local, "%d", domain)
	// В URL подставляем с экранированием: адрес ящика — часть пути.
	ru := strings.NewReplacer("%u", url.PathEscape(mailbox), "%l", url.PathEscape(local), "%d", url.PathEscape(domain))
	var out []ext.ContactPreset
	for _, p := range a.cfg.Presets {
		if len(p.Domains) > 0 && !containsFold(p.Domains, domain) {
			continue
		}
		pw := p.secret
		if p.Password == "session" {
			pw = ext.PasswordSession
		}
		out = append(out, ext.ContactPreset{
			ID: p.ID, Name: p.Name, ReadOnly: p.ReadOnly,
			Account: ext.ContactAccount{URL: ru.Replace(p.URL), Username: r.Replace(p.Username), Password: pw, Trusted: true},
		})
	}
	return out
}

func (a *Adapter) client(acc ext.ContactAccount, endpoint string) (*carddav.Client, error) {
	hc := a.public
	if acc.Trusted {
		hc = a.trusted
	}
	return carddav.NewClient(&authClient{c: hc, user: acc.Username, pass: acc.Password}, endpoint)
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
	resp, err := a.do(ctx, acc, http.MethodPut, bu.ResolveReference(&url.URL{Path: href}).String(), h, card)
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
	_, err = a.do(ctx, acc, http.MethodDelete, bu.ResolveReference(&url.URL{Path: href}).String(), h, nil)
	return err
}

// do выполняет PUT/DELETE и переводит коды ответа в ошибки ext.
func (a *Adapter) do(ctx context.Context, acc ext.ContactAccount, method, u string, h http.Header, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = h
	hc := a.public
	if acc.Trusted {
		hc = a.trusted
	}
	resp, err := (&authClient{c: hc, user: acc.Username, pass: acc.Password}).Do(req)
	if err != nil {
		return nil, mapErr(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	switch {
	case resp.StatusCode/100 == 2:
		return resp, nil
	case resp.StatusCode == http.StatusPreconditionFailed:
		return nil, ext.ErrContactConflict
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusMethodNotAllowed:
		return nil, ext.ErrContactReadOnly
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, ext.ErrContactNotFound
	}
	return nil, fmt.Errorf("carddav: %s %s: %s", method, u, resp.Status)
}

// --- HTTP ---------------------------------------------------------------------------

// authClient добавляет Basic-авторизацию, ограничивает размер ответа
// и превращает 401 в ext.ErrContactAuth.
type authClient struct {
	c          *http.Client
	user, pass string
}

func (c *authClient) Do(req *http.Request) (*http.Response, error) {
	if c.user != "" || c.pass != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	req.Header.Set("User-Agent", "webmail-carddav")
	resp, err := c.c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, ext.ErrContactAuth
	}
	resp.Body = &limitedBody{rc: resp.Body, n: maxResponse}
	return resp, nil
}

type limitedBody struct {
	rc io.ReadCloser
	n  int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errors.New("carddav: response too large")
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.rc.Read(p)
	l.n -= int64(n)
	return n, err
}

func (l *limitedBody) Close() error { return l.rc.Close() }

var errPrivate = fmt.Errorf("%w: private network", ext.ErrContactForbidden)

func newHTTPClient(timeout time.Duration, allowPrivate bool) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	if !allowPrivate {
		// Проверяем адрес, к которому реально подключаемся (после DNS), —
		// так не обойти запрет через имя, указывающее во внутреннюю сеть.
		d.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || !publicIP(ip) {
				return errPrivate
			}
			return nil
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = d.DialContext
	tr.Proxy = nil
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			// Учётные данные — только на тот же хост.
			if req.URL.Host != via[0].URL.Host {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
}

func publicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() ||
		// CGNAT 100.64/10 и прочие служебные диапазоны
		cgnat.Contains(ip))
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ext.ErrContactAuth), errors.Is(err, ext.ErrContactForbidden),
		errors.Is(err, ext.ErrContactNotFound), errors.Is(err, ext.ErrContactConflict):
		return err
	}
	if strings.Contains(err.Error(), "404") {
		return fmt.Errorf("%w: %v", ext.ErrContactNotFound, err)
	}
	return err
}

func normETag(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "W/")
	return strings.Trim(s, `"`)
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

var _ webdav.HTTPClient = (*authClient)(nil)
