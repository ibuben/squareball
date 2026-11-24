// Package panel — адаптер интеграции с панелью хостинга: вход в почту из
// панели без пароля (SSO) и служебный API, через который панель завершает
// сессии и удаляет данные ящика.
//
// Вход: панель подписывает короткоживущий одноразовый токен общим секретом
// (HMAC-SHA256) и отправляет браузер на /sso/panel?token=… (или POST формой
// с полем token). Ядро входит в IMAP мастер-учёткой (imap.impersonation),
// пароль ящика панели знать не нужно. Формат токена — docs/integration.md.
//
//	imap:
//	  impersonation:
//	    enabled: true
//	    master_user: webmail-master
//	    master_password_file: /etc/webmail/master.pass
//	    allowed_sources: [panel]
//	    apply_to: [smtp, sieve]
//	adapters:
//	  panel:
//	    enabled: true
//	    secret_file: /etc/webmail/panel-sso.key   # 0600, не короче 32 байт
//	    max_ttl: 2m                 # дольше токен жить не может, сколько бы ни просила панель
//	    domains: [example.com]      # пусто — любые домены (с учётом login.allowed_domains)
//	    error_url: https://panel.example.com/mail?sso=failed  # куда вернуть при плохом токене
//	    api:
//	      token_file: /etc/webmail/panel-api.token  # Bearer; без него API выключен
//	      allowed_ips: [127.0.0.1/32, ::1/128]
package panel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ibuben/squareball/ext"
)

// Source — имя источника сессий: в аудите и в imap.impersonation.allowed_sources.
const Source = "panel"

const (
	tokenPrefix = "v1."
	maxToken    = 4096
	clockSkew   = 30 * time.Second
	maxReplay   = 100_000 // защита памяти кэша одноразовых токенов
)

type APIConfig struct {
	TokenFile  string   `yaml:"token_file"`
	AllowedIPs []string `yaml:"allowed_ips"`
}

type Config struct {
	Enabled    bool          `yaml:"enabled"`
	SecretFile string        `yaml:"secret_file"`
	MaxTTL     time.Duration `yaml:"max_ttl"`
	Domains    []string      `yaml:"domains"`
	ErrorURL   string        `yaml:"error_url"`
	API        APIConfig     `yaml:"api"`
}

type Adapter struct {
	cfg      Config
	core     ext.Core
	secret   []byte
	apiToken []byte
	apiNets  []*net.IPNet
	now      func() time.Time

	mu   sync.Mutex
	seen map[string]time.Time // jti → exp: токен одноразовый
}

func New() *Adapter { return &Adapter{now: time.Now, seen: map[string]time.Time{}} }

func (a *Adapter) Name() string { return "panel" }

func (a *Adapter) Init(_ context.Context, node ext.ConfigNode, core ext.Core) error {
	if err := node.Decode(&a.cfg); err != nil {
		return err
	}
	if err := a.configure(); err != nil {
		return err
	}
	a.core = core
	core.Handle("GET /sso/panel", http.HandlerFunc(a.handleSSO))
	core.Handle("POST /sso/panel", http.HandlerFunc(a.handleSSO))
	if a.apiToken != nil {
		core.Handle("POST /panel/v1/sessions/end", a.api(a.handleEndSessions))
		core.Handle("POST /panel/v1/mailboxes/delete", a.api(a.handleDeleteMailbox))
	}
	core.Logger().Info("panel adapter", "sso", "/sso/panel", "api", a.apiToken != nil, "domains", a.cfg.Domains)
	return nil
}

func (a *Adapter) configure() error {
	c := &a.cfg
	if c.MaxTTL == 0 {
		c.MaxTTL = 2 * time.Minute
	}
	if c.MaxTTL < 10*time.Second || c.MaxTTL > 10*time.Minute {
		return errors.New("max_ttl: must be 10s..10m")
	}
	if c.SecretFile == "" {
		return errors.New("secret_file: required")
	}
	var err error
	if a.secret, err = readSecret(c.SecretFile); err != nil {
		return fmt.Errorf("secret_file: %w", err)
	}
	if len(a.secret) < 32 {
		return errors.New("secret_file: secret must be at least 32 bytes")
	}
	if c.ErrorURL != "" {
		u, err := url.Parse(c.ErrorURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errors.New("error_url: must be an absolute http(s) URL")
		}
	}
	for i, d := range c.Domains {
		c.Domains[i] = strings.ToLower(strings.TrimSpace(d))
	}
	if c.API.TokenFile != "" {
		if a.apiToken, err = readSecret(c.API.TokenFile); err != nil {
			return fmt.Errorf("api.token_file: %w", err)
		}
		if len(a.apiToken) < 32 {
			return errors.New("api.token_file: token must be at least 32 bytes")
		}
		ips := c.API.AllowedIPs
		if len(ips) == 0 {
			ips = []string{"127.0.0.1/32", "::1/128"}
		}
		for _, s := range ips {
			_, n, err := net.ParseCIDR(s)
			if err != nil {
				return fmt.Errorf("api.allowed_ips: %q: %w", s, err)
			}
			a.apiNets = append(a.apiNets, n)
		}
	}
	return nil
}

// readSecret читает секрет из файла с правами не шире 0600.
func readSecret(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s: permissions %o are too open, want 0600", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(string(b))), nil
}

// --- токен входа ------------------------------------------------------------------

// Claims — содержимое токена входа.
type Claims struct {
	Sub string `json:"sub"` // ящик local@domain
	Exp int64  `json:"exp"` // unix-время, до которого токен действителен
	Jti string `json:"jti"` // случайный идентификатор: второй раз токен не примут
	// Необязательные: язык интерфейса ("en", "ru") и тема ("light", "dark", "system"),
	// чтобы почта открылась как панель. Заменяют выбор, сохранённый в браузере.
	// Неизвестные значения не ошибка — клиент их пропустит.
	Lang  string `json:"lang,omitempty"`
	Theme string `json:"theme,omitempty"`
}

var (
	errMalformed = errors.New("malformed token")
	errSignature = errors.New("bad signature")
	errExpired   = errors.New("token expired")
	errTooLong   = errors.New("token lifetime exceeds max_ttl")
	errReplay    = errors.New("token already used")
	errDomain    = errors.New("mailbox domain is not allowed")
)

// Sign создаёт токен: v1.<base64url(JSON)>.<base64url(HMAC-SHA256(secret, "v1."+payload))>.
// Панели на других языках повторяют то же самое; здесь — для тестов и примеров.
func Sign(secret []byte, c Claims) string {
	body, _ := json.Marshal(c)
	payload := tokenPrefix + base64.RawURLEncoding.EncodeToString(body)
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac(secret, payload))
}

func mac(secret []byte, payload string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	return h.Sum(nil)
}

// verify проверяет подпись, срок, домен и одноразовость токена.
func (a *Adapter) verify(token string) (Claims, error) {
	var c Claims
	if len(token) > maxToken || !strings.HasPrefix(token, tokenPrefix) {
		return c, errMalformed
	}
	i := strings.LastIndexByte(token, '.')
	payload, sig := token[:i], token[i+1:]
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(want, mac(a.secret, payload)) {
		return c, errSignature
	}
	body, err := base64.RawURLEncoding.DecodeString(payload[len(tokenPrefix):])
	if err != nil || json.Unmarshal(body, &c) != nil {
		return c, errMalformed
	}
	c.Sub = strings.ToLower(strings.TrimSpace(c.Sub))
	local, domain, ok := strings.Cut(c.Sub, "@")
	if !ok || local == "" || domain == "" || len(c.Jti) < 16 || len(c.Jti) > 128 {
		return c, errMalformed
	}
	now := a.now()
	exp := time.Unix(c.Exp, 0)
	if !now.Before(exp.Add(clockSkew)) {
		return c, errExpired
	}
	if exp.Sub(now) > a.cfg.MaxTTL+clockSkew {
		return c, errTooLong
	}
	if !a.domainAllowed(domain) {
		return c, errDomain
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, e := range a.seen {
		if now.After(e.Add(clockSkew)) {
			delete(a.seen, k)
		}
	}
	if _, dup := a.seen[c.Jti]; dup {
		return c, errReplay
	}
	if len(a.seen) >= maxReplay {
		return c, errors.New("too many pending tokens")
	}
	a.seen[c.Jti] = exp
	return c, nil
}

func (a *Adapter) domainAllowed(domain string) bool {
	if len(a.cfg.Domains) == 0 {
		return true
	}
	for _, d := range a.cfg.Domains {
		if d == domain {
			return true
		}
	}
	return false
}

func (a *Adapter) handleSSO(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer") // токен в адресе не должен утечь дальше
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	token := r.FormValue("token")
	ip := a.core.ClientIP(r)
	c, err := a.verify(token)
	if err != nil {
		a.core.Logger().Warn("panel sso rejected", "audit", true, "reason", err.Error(), "mailbox", c.Sub, "ip", ip)
		a.fail(w, r, http.StatusForbidden, "bad_link")
		return
	}
	err = a.core.StartSession(w, r, ext.SessionGrant{Mailbox: c.Sub, AuthMode: ext.AuthImpersonation, Source: Source})
	if err != nil {
		a.core.Logger().Error("panel sso: start session", "err", err, "mailbox", c.Sub, "ip", ip)
		a.fail(w, r, http.StatusBadGateway, "unavailable")
		return
	}
	http.Redirect(w, r, a.core.BasePath(r)+landing(c), http.StatusSeeOther)
}

// prefValue — короткое значение из латиницы и дефиса ("ru", "pt-br", "dark"); иное отбрасывается.
var prefValue = regexp.MustCompile(`^[a-z]{2,8}(-[a-z0-9]{1,8})?$`)

// landing — куда отправить браузер после входа: язык и тему из токена клиент
// применит и уберёт из адреса (?lang=…&theme=…).
func landing(c Claims) string {
	q := url.Values{}
	if l := strings.ToLower(strings.TrimSpace(c.Lang)); prefValue.MatchString(l) {
		q.Set("lang", l)
	}
	if th := strings.ToLower(strings.TrimSpace(c.Theme)); prefValue.MatchString(th) {
		q.Set("theme", th)
	}
	if len(q) == 0 {
		return "/"
	}
	return "/?" + q.Encode()
}

// failTexts — страница ошибки входа по языку пользователя (core.Lang).
var failTexts = map[string]map[string]string{
	"en": {
		"title":       "Mail sign-in",
		"bad_link":    "The sign-in link is invalid or has expired. Open your mail from the control panel again.",
		"unavailable": "Could not open the mailbox. Try again later or contact support.",
		"password":    "Sign in with password",
	},
	"ru": {
		"title":       "Вход в почту",
		"bad_link":    "Ссылка для входа недействительна или устарела. Откройте почту из панели ещё раз.",
		"unavailable": "Не удалось открыть почтовый ящик. Попробуйте позже или обратитесь в поддержку.",
		"password":    "Войти по паролю",
	},
}

func (a *Adapter) fail(w http.ResponseWriter, r *http.Request, status int, reason string) {
	if a.cfg.ErrorURL != "" {
		http.Redirect(w, r, a.cfg.ErrorURL, http.StatusSeeOther)
		return
	}
	lang := a.core.Lang(r)
	tx, ok := failTexts[lang]
	if !ok {
		lang, tx = "en", failTexts["en"]
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html lang="%s"><meta charset="utf-8"><meta name="viewport" content="width=device-width">`+
		`<title>%s</title><body style="font:16px system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 16px">`+
		`<p>%s</p><p><a href="%s/">%s</a></p></body>`,
		lang, html.EscapeString(tx["title"]), html.EscapeString(tx[reason]), html.EscapeString(a.core.BasePath(r)), html.EscapeString(tx["password"]))
}

// --- служебный API ----------------------------------------------------------------

func (a *Adapter) api(h func(w http.ResponseWriter, r *http.Request, mailbox string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ip := a.core.ClientIP(r)
		if !a.ipAllowed(ip) {
			a.core.Logger().Warn("panel api: address not allowed", "audit", true, "ip", ip, "path", r.URL.Path)
			apiErr(w, http.StatusForbidden, "forbidden")
			return
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), a.apiToken) != 1 {
			a.core.Logger().Warn("panel api: bad token", "audit", true, "ip", ip, "path", r.URL.Path)
			apiErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var in struct {
			Mailbox string `json:"mailbox"`
		}
		dec := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			apiErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		mb := strings.ToLower(strings.TrimSpace(in.Mailbox))
		local, domain, found := strings.Cut(mb, "@")
		if !found || local == "" || domain == "" {
			apiErr(w, http.StatusBadRequest, "mailbox must be local@domain")
			return
		}
		if !a.domainAllowed(domain) {
			apiErr(w, http.StatusForbidden, "domain not allowed")
			return
		}
		h(w, r, mb)
	})
}

func (a *Adapter) ipAllowed(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	for _, n := range a.apiNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (a *Adapter) handleEndSessions(w http.ResponseWriter, r *http.Request, mailbox string) {
	n := a.core.EndSessions(mailbox)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ended": n})
}

func (a *Adapter) handleDeleteMailbox(w http.ResponseWriter, r *http.Request, mailbox string) {
	if err := a.core.DeleteMailboxData(r.Context(), mailbox); err != nil {
		a.core.Logger().Error("panel api: delete mailbox data", "err", err, "mailbox", mailbox)
		apiErr(w, http.StatusInternalServerError, "delete failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": msg})
}
