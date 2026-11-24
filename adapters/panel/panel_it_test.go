package panel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ibuben/squareball/adapters/panel"
	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/api"
	"github.com/ibuben/squareball/internal/config"
	"github.com/ibuben/squareball/internal/store"
)

type node struct{ cfg panel.Config }

func (n node) Decode(v any) error { *(v.(*panel.Config)) = n.cfg; return nil }

// recorder — драйвер смены пароля, который только запоминает запросы.
type recorder struct{ got []ext.PasswordChange }

func (r *recorder) Capabilities() ext.PasswordCaps {
	return ext.PasswordCaps{SSOWithoutCurrent: true}
}
func (r *recorder) Change(_ context.Context, c ext.PasswordChange) error {
	r.got = append(r.got, c)
	return nil
}

type browser struct {
	t    *testing.T
	base string
	hc   *http.Client
	csrf string
}

func (b *browser) do(method, path, auth string, body any) (int, map[string]any) {
	b.t.Helper()
	var rd io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rd = bytes.NewReader(buf)
	}
	req, _ := http.NewRequest(method, b.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if b.csrf != "" {
		req.Header.Set("X-CSRF-Token", b.csrf)
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	res, err := b.hc.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func newBrowser(t *testing.T, base string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: base, hc: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

func secretFile(t *testing.T, name string, b []byte) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Вход из панели мастер-учёткой на стенде: почта, отправка, смена пароля
// без текущего, завершение сессий и удаление данных через служебный API.
func TestIntegrationPanelSSO(t *testing.T) {
	if os.Getenv("WEBMAIL_IT") != "1" {
		t.Skip("set WEBMAIL_IT=1 with deploy/devstand running")
	}
	master, _ := filepath.Abs("../../deploy/devstand/master.key")
	if _, err := os.Stat(master); err != nil {
		t.Skip("deploy/devstand/master.key is missing: rerun make devstand")
	}
	cfg := config.Defaults()
	cfg.Server.PublicURL = "http://127.0.0.1"
	cfg.IMAP.Host, cfg.IMAP.Port, cfg.IMAP.Security, cfg.IMAP.TLSVerify = "127.0.0.1", 993, "tls", false
	cfg.SMTP.Host, cfg.SMTP.Port, cfg.SMTP.Security, cfg.SMTP.TLSVerify = "127.0.0.1", 587, "starttls", false
	cfg.Login.DefaultDomain = "test.local"
	cfg.IMAP.Impersonation = config.Impersonation{
		Enabled: true, Mechanism: "sasl_plain_authzid", MasterUser: "webmail-master", MasterPasswordFile: master,
		AllowedSources: []string{panel.Source}, ApplyTo: []string{"smtp", "sieve"},
	}
	cfg.Password.Driver = "rec"
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, err := api.New(api.Options{Config: cfg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		SessionSecret: bytes.Repeat([]byte("s"), 32), Store: st})
	if err != nil {
		t.Fatal(err)
	}
	pw := &recorder{}
	srv.SetPasswordChanger("rec", pw)

	key := bytes.Repeat([]byte("p"), 32)
	apiToken := strings.Repeat("a", 40)
	if err := panel.New().Init(context.Background(), node{panel.Config{
		SecretFile: secretFile(t, "sso.key", key), Domains: []string{"test.local"},
		API: panel.APIConfig{TokenFile: secretFile(t, "api.token", []byte(apiToken))},
	}}, srv); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	jti := "it-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	link := "/sso/panel?token=" + panel.Sign(key, panel.Claims{Sub: "Bob@test.local", Exp: time.Now().Add(time.Minute).Unix(), Jti: jti})
	b := newBrowser(t, ts.URL)
	if st, _ := b.do("GET", link, "", nil); st != http.StatusSeeOther {
		t.Fatalf("sso: %d", st)
	}
	st2, me := b.do("GET", "/api/v1/auth/me", "", nil)
	if st2 != 200 || me["mailbox"] != "bob@test.local" {
		t.Fatalf("me after sso: %d %v", st2, me)
	}
	b.csrf, _ = me["csrf"].(string)
	if st, _ := b.do("GET", "/api/v1/folders", "", nil); st != 200 {
		t.Fatalf("folders via master user: %d", st)
	}

	// Отправка идёт через SMTP той же мастер-учёткой (apply_to: smtp).
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("draft", `{"to":[{"email":"alice@test.local"}],"subject":"panel sso `+jti+`","text":"hi"}`)
	mw.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/send", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", b.csrf)
	if res, err := b.hc.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("send via master user: %v %v", err, res.StatusCode)
	}

	// Ссылка одноразовая.
	other := newBrowser(t, ts.URL)
	if st, _ := other.do("GET", link, "", nil); st != http.StatusForbidden {
		t.Errorf("replayed link: %d", st)
	}

	// Сразу после входа из панели пароль меняется без текущего.
	if _, info := b.do("GET", "/api/v1/account/password", "", nil); info["current_required"] != false {
		t.Errorf("password info in SSO window: %v", info)
	}
	if st, body := b.do("POST", "/api/v1/account/password", "", map[string]string{"new": "Nov-Parol-2026!"}); st != 200 {
		t.Fatalf("password change in SSO window: %d %v", st, body)
	}
	if len(pw.got) != 1 || !pw.got[0].SSOSkip || pw.got[0].Current != "" || pw.got[0].Mailbox != "bob@test.local" {
		t.Errorf("driver request: %+v", pw.got)
	}

	// Обычный вход по паролю текущий пароль спрашивает.
	pb := newBrowser(t, ts.URL)
	st3, pme := pb.do("POST", "/api/v1/auth/login", "", map[string]string{"username": "bob", "password": "Test12345!"})
	if st3 != 200 {
		t.Fatalf("password login: %d", st3)
	}
	pb.csrf, _ = pme["csrf"].(string)
	if _, info := pb.do("GET", "/api/v1/account/password", "", nil); info["current_required"] != true {
		t.Errorf("password info for password login: %v", info)
	}
	if st, _ := pb.do("POST", "/api/v1/account/password", "", map[string]string{"new": "Nov-Parol-2026!"}); st != 400 {
		t.Errorf("password login without current: %d", st)
	}

	// Панель завершает все сессии ящика.
	if st, _ := b.do("POST", "/panel/v1/sessions/end", "wrong-token-wrong-token-wrong-token", map[string]string{"mailbox": "bob@test.local"}); st != 401 {
		t.Errorf("api with wrong token: %d", st)
	}
	st4, res := b.do("POST", "/panel/v1/sessions/end", apiToken, map[string]string{"mailbox": "BOB@test.local"})
	if st4 != 200 || res["ended"] != float64(2) {
		t.Fatalf("end sessions: %d %v", st4, res)
	}
	for _, c := range []*browser{b, pb} {
		if st, _ := c.do("GET", "/api/v1/folders", "", nil); st != 401 {
			t.Errorf("session survived end: %d", st)
		}
	}
	if st, res := b.do("POST", "/panel/v1/mailboxes/delete", apiToken, map[string]string{"mailbox": "bob@test.local"}); st != 200 || res["ok"] != true {
		t.Errorf("delete mailbox data: %d %v", st, res)
	}

	// Без разрешённого источника ядро не откроет сессию даже по верному токену.
	cfg.IMAP.Impersonation.AllowedSources = []string{"other"}
	link = "/sso/panel?token=" + panel.Sign(key, panel.Claims{Sub: "bob@test.local", Exp: time.Now().Add(time.Minute).Unix(), Jti: jti + "-2"})
	if st, _ := other.do("GET", link, "", nil); st != http.StatusBadGateway {
		t.Errorf("source not allowed: %d", st)
	}
}
