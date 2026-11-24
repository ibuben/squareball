package panel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ibuben/squareball/ext"
)

var secret = bytes.Repeat([]byte("k"), 32)

type fakeCore struct {
	mux      *http.ServeMux
	grants   []ext.SessionGrant
	startErr error
	ended    []string
	deleted  []string
}

func (f *fakeCore) Handle(p string, h http.Handler) { f.mux.Handle(p, h) }
func (f *fakeCore) StartSession(w http.ResponseWriter, r *http.Request, g ext.SessionGrant) error {
	f.grants = append(f.grants, g)
	return f.startErr
}
func (f *fakeCore) Lang(r *http.Request) string { return "en" }

func (f *fakeCore) ClientIP(r *http.Request) string {
	h, _, _ := strings.Cut(r.RemoteAddr, ":")
	return h
}
func (f *fakeCore) Logger() *slog.Logger                           { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func (f *fakeCore) SetPasswordChanger(string, ext.PasswordChanger) {}
func (f *fakeCore) SetBrandProvider(ext.BrandProvider)             {}
func (f *fakeCore) SetContactSource(ext.ContactSource)             {}
func (f *fakeCore) EndSessions(mb string) int                      { f.ended = append(f.ended, mb); return 2 }
func (f *fakeCore) DeleteMailboxData(_ context.Context, mb string) error {
	f.deleted = append(f.deleted, mb)
	return nil
}

type node struct{ cfg Config }

func (n node) Decode(v any) error { *(v.(*Config)) = n.cfg; return nil }

func writeSecret(t *testing.T, name string, b []byte) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func setup(t *testing.T, cfg Config) (*Adapter, *fakeCore) {
	t.Helper()
	if cfg.SecretFile == "" {
		cfg.SecretFile = writeSecret(t, "sso.key", secret)
	}
	a := New()
	f := &fakeCore{mux: http.NewServeMux()}
	if err := a.Init(context.Background(), node{cfg}, f); err != nil {
		t.Fatal(err)
	}
	return a, f
}

func claims(sub string, ttl time.Duration, jti string) Claims {
	return Claims{Sub: sub, Exp: time.Now().Add(ttl).Unix(), Jti: jti}
}

func TestVerify(t *testing.T) {
	a, _ := setup(t, Config{Domains: []string{"Test.local"}})
	good := Sign(secret, claims("Bob@Test.Local", time.Minute, "jti-000000000001"))
	if c, err := a.verify(good); err != nil || c.Sub != "bob@test.local" {
		t.Fatalf("good token: %v %+v", err, c)
	}
	for name, c := range map[string]struct {
		token string
		want  error
	}{
		"replay":      {good, errReplay},
		"other key":   {Sign(bytes.Repeat([]byte("x"), 32), claims("bob@test.local", time.Minute, "jti-000000000002")), errSignature},
		"tampered":    {strings.Replace(Sign(secret, claims("bob@test.local", time.Minute, "jti-000000000003")), "v1.e", "v1.f", 1), errSignature},
		"expired":     {Sign(secret, claims("bob@test.local", -time.Minute, "jti-000000000004")), errExpired},
		"too long":    {Sign(secret, claims("bob@test.local", time.Hour, "jti-000000000005")), errTooLong},
		"domain":      {Sign(secret, claims("bob@evil.example", time.Minute, "jti-000000000006")), errDomain},
		"short jti":   {Sign(secret, claims("bob@test.local", time.Minute, "x")), errMalformed},
		"no domain":   {Sign(secret, claims("bob", time.Minute, "jti-000000000007")), errMalformed},
		"no prefix":   {strings.TrimPrefix(Sign(secret, claims("bob@test.local", time.Minute, "jti-000000000008")), "v1."), errMalformed},
		"empty":       {"", errMalformed},
		"garbage sig": {"v1.abc.!!!", errSignature},
	} {
		if _, err := a.verify(c.token); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", name, err, c.want)
		}
	}
	// Небольшой разброс часов панели и почты не мешает.
	if _, err := a.verify(Sign(secret, claims("bob@test.local", -10*time.Second, "jti-000000000009"))); err != nil {
		t.Errorf("clock skew: %v", err)
	}
}

func TestSSOHandler(t *testing.T) {
	_, f := setup(t, Config{})
	tok := Sign(secret, claims("bob@test.local", time.Minute, "jti-sso-000000001"))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, httptest.NewRequest("GET", "/sso/panel?token="+tok, nil))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("sso: %d %s", w.Code, w.Header().Get("Location"))
	}
	if len(f.grants) != 1 || f.grants[0] != (ext.SessionGrant{Mailbox: "bob@test.local", AuthMode: ext.AuthImpersonation, Source: Source}) {
		t.Errorf("grants %+v", f.grants)
	}
	// Повтор той же ссылки — отказ со страницей ошибки.
	w = httptest.NewRecorder()
	f.mux.ServeHTTP(w, httptest.NewRequest("GET", "/sso/panel?token="+tok, nil))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "invalid or has expired") || len(f.grants) != 1 {
		t.Errorf("replay: %d %s", w.Code, w.Body)
	}
	// POST формой.
	tok = Sign(secret, claims("bob@test.local", time.Minute, "jti-sso-000000002"))
	r := httptest.NewRequest("POST", "/sso/panel", strings.NewReader("token="+tok))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || len(f.grants) != 2 {
		t.Errorf("post: %d", w.Code)
	}
	// Ядро не смогло открыть ящик.
	f.startErr = errors.New("imap down")
	tok = Sign(secret, claims("bob@test.local", time.Minute, "jti-sso-000000003"))
	w = httptest.NewRecorder()
	f.mux.ServeHTTP(w, httptest.NewRequest("GET", "/sso/panel?token="+tok, nil))
	if w.Code != http.StatusBadGateway {
		t.Errorf("start error: %d", w.Code)
	}
}

func TestSSOErrorURL(t *testing.T) {
	_, f := setup(t, Config{ErrorURL: "https://panel.example.com/mail?sso=failed"})
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, httptest.NewRequest("GET", "/sso/panel?token=bad", nil))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "https://panel.example.com/mail?sso=failed" {
		t.Errorf("error_url: %d %s", w.Code, w.Header().Get("Location"))
	}
}

func TestAPI(t *testing.T) {
	token := strings.Repeat("t", 40)
	_, f := setup(t, Config{Domains: []string{"test.local"}, API: APIConfig{TokenFile: writeSecret(t, "api.token", []byte(token+"\n"))}})
	call := func(path, ip, auth, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.RemoteAddr = ip + ":40000"
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w
	}
	if w := call("/panel/v1/sessions/end", "127.0.0.1", token, `{"mailbox":"Bob@test.local"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"ended":2`) {
		t.Errorf("end: %d %s", w.Code, w.Body)
	}
	if w := call("/panel/v1/mailboxes/delete", "127.0.0.1", token, `{"mailbox":"bob@test.local"}`); w.Code != 200 {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if len(f.ended) != 1 || f.ended[0] != "bob@test.local" || len(f.deleted) != 1 {
		t.Errorf("calls: ended %v deleted %v", f.ended, f.deleted)
	}
	for name, c := range map[string]struct {
		ip, auth, body string
		want           int
	}{
		"foreign ip":    {"203.0.113.5", token, `{"mailbox":"bob@test.local"}`, 403},
		"no token":      {"127.0.0.1", "", `{"mailbox":"bob@test.local"}`, 401},
		"wrong token":   {"127.0.0.1", strings.Repeat("x", 40), `{"mailbox":"bob@test.local"}`, 401},
		"bad mailbox":   {"127.0.0.1", token, `{"mailbox":"bob"}`, 400},
		"unknown field": {"127.0.0.1", token, `{"mailbox":"bob@test.local","all":true}`, 400},
		"other domain":  {"127.0.0.1", token, `{"mailbox":"bob@other.example"}`, 403},
	} {
		if w := call("/panel/v1/sessions/end", c.ip, c.auth, c.body); w.Code != c.want {
			t.Errorf("%s: %d, want %d", name, w.Code, c.want)
		}
	}
	if len(f.ended) != 1 {
		t.Errorf("rejected calls reached core: %v", f.ended)
	}
}

func TestConfig(t *testing.T) {
	for name, cfg := range map[string]Config{
		"no secret":    {},
		"short secret": {SecretFile: writeSecret(t, "s", []byte("short"))},
		"bad ttl":      {SecretFile: writeSecret(t, "s2", secret), MaxTTL: time.Hour},
		"bad url":      {SecretFile: writeSecret(t, "s3", secret), ErrorURL: "/relative"},
		"bad cidr":     {SecretFile: writeSecret(t, "s4", secret), API: APIConfig{TokenFile: writeSecret(t, "t", bytes.Repeat([]byte("t"), 32)), AllowedIPs: []string{"10.0.0.1"}}},
	} {
		a := New()
		if err := a.Init(context.Background(), node{cfg}, &fakeCore{mux: http.NewServeMux()}); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	p := filepath.Join(t.TempDir(), "open.key")
	os.WriteFile(p, secret, 0o644)
	if err := New().Init(context.Background(), node{Config{SecretFile: p}}, &fakeCore{mux: http.NewServeMux()}); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Errorf("world-readable secret: %v", err)
	}
	// Без api.token_file служебного API нет.
	_, f := setup(t, Config{})
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, httptest.NewRequest("POST", "/panel/v1/sessions/end", strings.NewReader(`{}`)))
	if w.Code != http.StatusNotFound {
		t.Errorf("api without token: %d", w.Code)
	}
}

// Язык и тема из токена уходят клиенту в адресе; мусор отбрасывается.
func TestSSOLangTheme(t *testing.T) {
	_, f := setup(t, Config{})
	for i, c := range []struct{ lang, theme, want string }{
		{"ru", "dark", "/?lang=ru&theme=dark"},
		{" RU ", "", "/?lang=ru"},
		{"", "system", "/?theme=system"},
		{"pt-BR", "light", "/?lang=pt-br&theme=light"},
		{"ru&x=1", "<b>", "/"},
		{"", "", "/"},
	} {
		cl := claims("bob@test.local", time.Minute, fmt.Sprintf("jti-sso-lang-%04d", i))
		cl.Lang, cl.Theme = c.lang, c.theme
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, httptest.NewRequest("GET", "/sso/panel?token="+Sign(secret, cl), nil))
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != c.want {
			t.Errorf("lang %q theme %q: %d %s, want %s", c.lang, c.theme, w.Code, w.Header().Get("Location"), c.want)
		}
	}
}
