// Package api — HTTP-слой BFF: внутренний REST API для SPA и реализация ext.Core.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/config"
	"github.com/ibuben/squareball/internal/mail"
	"github.com/ibuben/squareball/internal/session"
	"github.com/ibuben/squareball/internal/store"
)

type Server struct {
	cfg      *config.Config
	log      *slog.Logger
	audit    *slog.Logger
	sessions *session.Manager
	pool     *mail.Pool
	store    *store.Store
	mux      *http.ServeMux
	trusted  []*net.IPNet
	secure   bool // public_url https → Secure cookie

	streams streamCounter

	loginIP   *limiter
	loginUser *limiter
	sendUser  *limiter

	mu        sync.RWMutex
	pwName    string
	pwChanger ext.PasswordChanger
	brand     ext.BrandProvider
	masterPw  string

	pwnedClient *http.Client // проверка пароля по базе утечек (password.policy.check_breached)
}

type Options struct {
	Config        *config.Config
	Logger        *slog.Logger
	Audit         *slog.Logger
	SessionSecret []byte
	Store         *store.Store
	Static        http.Handler // SPA
}

func New(o Options) (*Server, error) {
	cfg := o.Config
	s := &Server{
		cfg: cfg, log: o.Logger, audit: o.Audit, mux: http.NewServeMux(), store: o.Store,
		loginIP:   newLimiter(cfg.Login.RateLimit.PerIP),
		loginUser: newLimiter(cfg.Login.RateLimit.PerUser),
		sendUser:  newLimiter(cfg.SMTP.RateLimit.PerUser),

		pwnedClient: &http.Client{Timeout: 5 * time.Second},
	}
	if s.store == nil {
		return nil, errors.New("api: store is required")
	}
	if s.audit == nil {
		s.audit = s.log
	}
	u, _ := url.Parse(cfg.Server.PublicURL)
	s.secure = u.Scheme == "https"
	for _, cidr := range cfg.Server.TrustedProxies {
		_, n, _ := net.ParseCIDR(cidr)
		s.trusted = append(s.trusted, n)
	}
	s.pool = mail.NewPool(&cfg.IMAP)
	var err error
	s.sessions, err = session.NewManager(o.SessionSecret, cfg.Session.IdleTimeout, cfg.Session.AbsoluteTimeout, s.pool.Close)
	if err != nil {
		return nil, err
	}
	if cfg.IMAP.Impersonation.Enabled {
		b, err := config.ReadSecret(cfg.IMAP.Impersonation.MasterPasswordFile)
		if err != nil {
			return nil, fmt.Errorf("imap.impersonation.master_password_file: %w", err)
		}
		s.masterPw = string(b)
	}
	s.routes(o.Static)
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.securityHeaders(s.mux) }

// --- ext.Core ---------------------------------------------------------------

func (s *Server) Handle(pattern string, h http.Handler) {
	p := pattern
	if i := strings.IndexByte(p, ' '); i >= 0 {
		p = p[i+1:]
	}
	if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/assets/") {
		panic("adapter route " + pattern + " uses a reserved prefix")
	}
	s.mux.Handle(pattern, h)
}

func (s *Server) Logger() *slog.Logger { return s.log }

func (s *Server) SetPasswordChanger(name string, pc ext.PasswordChanger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pwName, s.pwChanger = name, pc
}

// PasswordEnabled сообщает, есть ли драйвер с именем из password.driver.
func (s *Server) PasswordEnabled() bool {
	_, ok := s.passwordChanger()
	return ok
}

func (s *Server) passwordChanger() (ext.PasswordChanger, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ok := s.pwChanger != nil && s.pwName == s.cfg.Password.Driver
	return s.pwChanger, ok
}

func (s *Server) SetBrandProvider(bp ext.BrandProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.brand = bp
}

func (s *Server) StartSession(w http.ResponseWriter, r *http.Request, g ext.SessionGrant) error {
	if g.AuthMode == ext.AuthImpersonation {
		im := s.cfg.IMAP.Impersonation
		if !im.Enabled || !contains(im.AllowedSources, g.Source) {
			return fmt.Errorf("impersonation is not allowed for source %q", g.Source)
		}
	}
	ip := s.ClientIP(r)
	sess, cookie, err := s.sessions.Create(g.Mailbox, "", g.AuthMode, g.Source, ip, r.UserAgent())
	if err != nil {
		return err
	}
	c, err := mail.DialIMAP(&s.cfg.IMAP, s.creds(sess))
	if err != nil {
		s.sessions.Delete(sess.ID)
		return err
	}
	s.pool.Adopt(sess.ID, c)
	s.setCookie(w, cookie)
	s.audit.Info("login", "mailbox", g.Mailbox, "source", g.Source, "mode", "impersonation", "ip", ip)
	return nil
}

// ClientIP учитывает X-Forwarded-For только от доверенных прокси.
func (s *Server) ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !s.isTrusted(ip) {
		return host
	}
	xff := r.Header.Get("X-Forwarded-For")
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		pip := net.ParseIP(p)
		if pip == nil {
			continue
		}
		if !s.isTrusted(pip) {
			return p
		}
		host = p
	}
	return host
}

func (s *Server) isTrusted(ip net.IP) bool {
	for _, n := range s.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// --- общая инфраструктура ----------------------------------------------------

func (s *Server) creds(sess *session.Session) mail.Creds {
	if sess.AuthMode == ext.AuthImpersonation {
		return mail.Creds{Username: sess.Mailbox, MasterUser: s.cfg.IMAP.Impersonation.MasterUser, MasterPassword: s.masterPw}
	}
	pw, _ := sess.Password(s.sessions)
	return mail.Creds{Username: sess.Mailbox, Password: pw}
}

func (s *Server) smtpCreds(sess *session.Session) mail.Creds {
	cr := s.creds(sess)
	if cr.MasterUser != "" && !contains(s.cfg.IMAP.Impersonation.ApplyTo, "smtp") {
		return mail.Creds{} // impersonation для SMTP не разрешена конфигом
	}
	return cr
}

func (s *Server) setCookie(w http.ResponseWriter, value string) {
	sameSite := http.SameSiteLaxMode
	if len(s.cfg.Server.FrameAncestors) > 0 && s.secure {
		sameSite = http.SameSiteNoneMode // для встраивания в iframe панели
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cfg.Session.CookieName, Value: value, Path: "/", HttpOnly: true,
		Secure: s.secure, SameSite: sameSite,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cfg.Session.CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure,
	})
}

type ctxKey struct{}

func sessionFrom(ctx context.Context) *session.Session {
	sess, _ := ctx.Value(ctxKey{}).(*session.Session)
	return sess
}

// authed проверяет сессию, а для изменяющих запросов — CSRF и Origin.
func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(s.cfg.Session.CookieName)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "auth.required", "")
			return
		}
		sess, err := s.sessions.Get(c.Value)
		if err != nil {
			s.clearCookie(w)
			writeErr(w, http.StatusUnauthorized, "auth.required", "")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !sess.CheckCSRF(r.Header.Get("X-CSRF-Token")) || !s.sameOrigin(r) {
				writeErr(w, http.StatusForbidden, "csrf", "")
				return
			}
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess)))
	}
}

func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // не браузерный запрос или тот же origin в старых браузерах; CSRF-токен всё равно обязателен
	}
	pu, _ := url.Parse(s.cfg.Server.PublicURL)
	ou, err := url.Parse(origin)
	return err == nil && strings.EqualFold(ou.Host, pu.Host) && ou.Scheme == pu.Scheme
}

// withIMAP выполняет операцию на IMAP-соединении сессии и переводит ошибки в HTTP.
func (s *Server) withIMAP(w http.ResponseWriter, r *http.Request, fn func(c *imapclient.Client) error) bool {
	sess := sessionFrom(r.Context())
	err := s.pool.With(sess.ID, s.creds(sess), fn)
	if err == nil {
		return true
	}
	s.mailError(w, r, err)
	return false
}

func (s *Server) mailError(w http.ResponseWriter, r *http.Request, err error) {
	sess := sessionFrom(r.Context())
	var ie *imap.Error
	var se *mail.SendError
	switch {
	case errors.Is(err, mail.ErrAuth):
		// пароль сменили в другом месте — сессия больше не действительна
		if sess != nil {
			s.sessions.Delete(sess.ID)
		}
		s.clearCookie(w)
		writeErr(w, http.StatusUnauthorized, "auth.expired", "")
	case errors.Is(err, mail.ErrNoDrafts):
		writeErr(w, http.StatusConflict, "mail.no_drafts_folder", "")
	case errors.Is(err, mail.ErrFolderName):
		writeErr(w, http.StatusBadRequest, "folder.name", "")
	case errors.Is(err, mail.ErrFolderExists):
		writeErr(w, http.StatusConflict, "folder.exists", "")
	case errors.Is(err, mail.ErrFolderProtected):
		writeErr(w, http.StatusForbidden, "folder.protected", "")
	case errors.Is(err, mail.ErrNoArchive):
		writeErr(w, http.StatusConflict, "mail.no_archive_folder", "")
	case errors.Is(err, mail.ErrNotFound):
		writeErr(w, http.StatusNotFound, "mail.not_found", "")
	case errors.As(err, &ie):
		writeErr(w, http.StatusBadRequest, "mail.server", ie.Text)
	case errors.As(err, &se):
		writeErr(w, http.StatusBadRequest, "mail.rejected", se.Message)
	default:
		s.log.Error("mail operation failed", "err", err, "path", r.URL.Path)
		writeErr(w, http.StatusBadGateway, "mail.unavailable", "")
	}
}

func (s *Server) securityHeaders(h http.Handler) http.Handler {
	fa := "'none'"
	if len(s.cfg.Server.FrameAncestors) > 0 {
		fa = strings.Join(s.cfg.Server.FrameAncestors, " ")
	}
	// Письма показываются в iframe srcdoc, который наследует CSP страницы:
	// поэтому img-src разрешает внешние картинки (их показ решает санитайзер),
	// а style-src — inline-стили писем. Скрипты — только свои.
	csp := "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob: https: http:; font-src 'self' data:; connect-src 'self'; " +
		"frame-src 'self' about:; object-src 'none'; base-uri 'none'; form-action 'self'; " +
		"frame-ancestors " + fa
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", csp)
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		if s.secure {
			hd.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			hd.Set("Cache-Control", "no-store")
		}
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, detail string) {
	body := map[string]string{"error": code}
	if detail != "" {
		body["detail"] = detail
	}
	writeJSON(w, status, body)
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// --- лимиты ------------------------------------------------------------------

type limiter struct {
	rate config.Rate
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLimiter(r config.Rate) *limiter { return &limiter{rate: r, hits: map[string][]time.Time{}} }

// Allow регистрирует событие и сообщает, не превышен ли лимит.
func (l *limiter) Allow(key string) bool {
	if l.rate.N == 0 {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	h := l.hits[key]
	cut := 0
	for cut < len(h) && now.Sub(h[cut]) > l.rate.Period {
		cut++
	}
	h = h[cut:]
	if len(h) >= l.rate.N {
		l.hits[key] = h
		return false
	}
	l.hits[key] = append(h, now)
	if len(l.hits) > 100000 { // защита от раздувания памяти
		l.hits = map[string][]time.Time{}
	}
	return true
}

// Reset очищает счётчик (успешный вход сбрасывает неудачи пользователя).
func (l *limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}
