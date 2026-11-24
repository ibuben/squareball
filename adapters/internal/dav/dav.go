// Package dav — общее для адаптеров CardDAV и CalDAV: пресеты из конфига,
// HTTP-клиент с защитой от обращений во внутренние сети, Basic-авторизация
// с ограничением размера ответа, PUT/DELETE с переводом кодов в ошибки ext.
package dav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/emersion/go-webdav"

	"github.com/ibuben/squareball/ext"
)

// MaxResponse — предел ответа сервера, байт.
const MaxResponse = 64 << 20

// Preset — коллекции, которые администратор подключает всем ящикам.
type Preset struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	URL          string   `yaml:"url"`
	Username     string   `yaml:"username"`
	Password     string   `yaml:"password"` // "session" — пароль ящика
	PasswordFile string   `yaml:"password_file"`
	ReadOnly     bool     `yaml:"readonly"`
	Domains      []string `yaml:"domains"`
	// Только CalDAV. Collection — URL указывает на набор календарей: поиск
	// через принципала не нужен, пустой набор — не ошибка.
	Collection bool `yaml:"collection"`
	// Writers — кому можно писать в календарь: путь коллекции без крайних
	// "/" → ящики ("*" — всем, кто его видит). Календари не из списка — только
	// чтение. Пусто — права определяет сервер.
	Writers map[string][]string `yaml:"writers"`

	Secret string `yaml:"-"` // прочитан из password_file
}

// CheckPresets проверяет пресеты и читает password_file.
func CheckPresets(ps []Preset) error {
	ids := map[string]bool{}
	for i := range ps {
		p := &ps[i]
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
			p.Secret = strings.TrimRight(string(b), "\r\n")
		default:
			return fmt.Errorf("presets[%d]: set password: session or password_file", i)
		}
		if p.Name == "" {
			p.Name = p.ID
		}
	}
	return nil
}

// Account — учётка пресета для ящика; ok == false, если пресет не для его домена.
func (p Preset) Account(mailbox string) (acc ext.ContactAccount, ok bool) {
	local, domain, _ := strings.Cut(mailbox, "@")
	if len(p.Domains) > 0 && !ContainsFold(p.Domains, domain) {
		return acc, false
	}
	r := strings.NewReplacer("%u", mailbox, "%l", local, "%d", domain)
	// В URL подставляем с экранированием: адрес ящика — часть пути.
	ru := strings.NewReplacer("%u", url.PathEscape(mailbox), "%l", url.PathEscape(local), "%d", url.PathEscape(domain))
	pw := p.Secret
	if p.Password == "session" {
		pw = ext.PasswordSession
	}
	return ext.ContactAccount{URL: ru.Replace(p.URL), Username: r.Replace(p.Username), Password: pw, Trusted: true, Collection: p.Collection}, true
}

// CanWrite — можно ли ящику писать в календарь по адресу calURL; nil — если
// пресет не ограничивает запись.
func (p Preset) CanWrite(mailbox string) func(calURL string) bool {
	if p.Writers == nil {
		return nil
	}
	return func(calURL string) bool {
		u, err := url.Parse(calURL)
		if err != nil {
			return false
		}
		for _, m := range p.Writers[strings.Trim(u.Path, "/")] {
			if m == "*" || strings.EqualFold(m, mailbox) {
				return true
			}
		}
		return false
	}
}

// --- HTTP -------------------------------------------------------------------------

// Clients — два HTTP-клиента: для пресетов (адреса из конфига) и для учёток
// пользователей (без внутренних сетей, если администратор не разрешил).
type Clients struct {
	Trusted, Public *http.Client
	Agent           string
}

func NewClients(timeout time.Duration, allowPrivate bool, agent string) Clients {
	return Clients{Trusted: NewHTTPClient(timeout, true), Public: NewHTTPClient(timeout, allowPrivate), Agent: agent}
}

// For — клиент с авторизацией учётки.
func (c Clients) For(acc ext.ContactAccount) *AuthClient {
	hc := c.Public
	if acc.Trusted {
		hc = c.Trusted
	}
	return &AuthClient{C: hc, User: acc.Username, Pass: acc.Password, Agent: c.Agent}
}

// Do выполняет PUT/DELETE и переводит коды ответа в ошибки ext.
func (c Clients) Do(ctx context.Context, acc ext.ContactAccount, method, u string, h http.Header, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = h
	resp, err := c.For(acc).Do(req)
	if err != nil {
		return nil, MapErr(err)
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
	return nil, fmt.Errorf("%s: %s %s: %s", c.Agent, method, u, resp.Status)
}

// AuthClient добавляет Basic-авторизацию, ограничивает размер ответа
// и превращает 401 в ext.ErrContactAuth.
type AuthClient struct {
	C          *http.Client
	User, Pass string
	Agent      string
}

func (c *AuthClient) Do(req *http.Request) (*http.Response, error) {
	if c.User != "" || c.Pass != "" {
		req.SetBasicAuth(c.User, c.Pass)
	}
	req.Header.Set("User-Agent", c.Agent)
	resp, err := c.C.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, ext.ErrContactAuth
	}
	resp.Body = &limitedBody{rc: resp.Body, n: MaxResponse}
	return resp, nil
}

var _ webdav.HTTPClient = (*AuthClient)(nil)

type limitedBody struct {
	rc io.ReadCloser
	n  int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errors.New("dav: response too large")
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

func NewHTTPClient(timeout time.Duration, allowPrivate bool) *http.Client {
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
			if ip == nil || !PublicIP(ip) {
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

func PublicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() ||
		// CGNAT 100.64/10 и прочие служебные диапазоны
		cgnat.Contains(ip))
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func MapErr(err error) error {
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

func NormETag(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "W/")
	return strings.Trim(s, `"`)
}

func ContainsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
