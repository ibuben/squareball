package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const minimal = `version: 1
server: {public_url: https://mail.example.com}
session: {secret_file: /etc/webmail/session.key}
imap: {host: mail.example.com}
smtp: {host: mail.example.com}
`

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMinimalConfigGetsDefaults(t *testing.T) {
	c, err := Load(write(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.IMAP.Port != 993 || c.IMAP.Security != "tls" || !c.IMAP.TLSVerify {
		t.Errorf("imap defaults: %+v", c.IMAP.Endpoint)
	}
	if c.SMTP.Port != 587 || c.SMTP.Security != "starttls" {
		t.Errorf("smtp defaults: %+v", c.SMTP.Endpoint)
	}
	if c.IMAP.Impersonation.Enabled {
		t.Error("impersonation must be off by default")
	}
	if c.Password.Driver != "none" {
		t.Errorf("password.driver = %q, want none", c.Password.Driver)
	}
	if c.Sieve.Host != "mail.example.com" {
		t.Errorf("sieve.host should default to imap.host, got %q", c.Sieve.Host)
	}
}

func TestUnknownKeyIsError(t *testing.T) {
	_, err := Load(write(t, minimal+"imap_extra: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "imap_extra") {
		t.Fatalf("want unknown key error, got %v", err)
	}
}

func TestRequiredFields(t *testing.T) {
	_, err := Load(write(t, "version: 1\n"))
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"server.public_url", "session.secret_file", "imap.host", "smtp.host"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s: %v", want, err)
		}
	}
}

func TestPlaintextOnlyForLoopback(t *testing.T) {
	t.Setenv("WEBMAIL_IMAP__SECURITY", "none")
	if _, err := Load(write(t, minimal)); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("want loopback error, got %v", err)
	}
	t.Setenv("WEBMAIL_IMAP__HOST", "127.0.0.1")
	t.Setenv("WEBMAIL_SMTP__HOST", "127.0.0.1")
	if _, err := Load(write(t, minimal)); err != nil {
		t.Fatalf("plaintext on loopback should be allowed: %v", err)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("WEBMAIL_IMAP__PORT", "143")
	t.Setenv("WEBMAIL_SESSION__IDLE_TIMEOUT", "45m")
	t.Setenv("WEBMAIL_SMTP__MAX_MESSAGE_SIZE", "10MB")
	t.Setenv("WEBMAIL_LOGIN__ALLOWED_DOMAINS", "a.test, b.test")
	t.Setenv("WEBMAIL_LOGIN__RATE_LIMIT__PER_IP", "3/1m")
	t.Setenv("WEBMAIL_SOMETHING", "ignored") // без "__" — не ключ конфига
	c, err := Load(write(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.IMAP.Port != 143 || c.Session.IdleTimeout != 45*time.Minute || c.SMTP.MaxMessageSize != 10<<20 {
		t.Errorf("overrides not applied: port=%d idle=%v size=%d", c.IMAP.Port, c.Session.IdleTimeout, c.SMTP.MaxMessageSize)
	}
	if len(c.Login.AllowedDomains) != 2 || c.Login.AllowedDomains[1] != "b.test" {
		t.Errorf("list override: %v", c.Login.AllowedDomains)
	}
	if c.Login.RateLimit.PerIP != (Rate{3, time.Minute}) {
		t.Errorf("rate override: %+v", c.Login.RateLimit.PerIP)
	}
}

func TestEnvErrors(t *testing.T) {
	t.Setenv("WEBMAIL_IMAP__PORT", "abc")
	if _, err := Load(write(t, minimal)); err == nil || !strings.Contains(err.Error(), "WEBMAIL_IMAP__PORT") {
		t.Fatalf("want parse error naming the variable, got %v", err)
	}
	os.Unsetenv("WEBMAIL_IMAP__PORT")
	t.Setenv("WEBMAIL_IMAP__NOPE", "1")
	if _, err := Load(write(t, minimal)); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("want unknown key, got %v", err)
	}
}

func TestImpersonationValidation(t *testing.T) {
	t.Setenv("WEBMAIL_IMAP__IMPERSONATION__ENABLED", "true")
	_, err := Load(write(t, minimal))
	if err == nil || !strings.Contains(err.Error(), "master_user") || !strings.Contains(err.Error(), "allowed_sources") {
		t.Fatalf("want impersonation errors, got %v", err)
	}
}

func TestParseHelpers(t *testing.T) {
	for in, want := range map[string]ByteSize{"25MB": 25 << 20, "512kb": 512 << 10, "100": 100, "1GB": 1 << 30} {
		if got, err := ParseByteSize(in); err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %d, %v", in, got, err)
		}
	}
	for in, want := range map[string]Rate{"20/10m": {20, 10 * time.Minute}, "100/h": {100, time.Hour}, "5/15m": {5, 15 * time.Minute}} {
		if got, err := ParseRate(in); err != nil || got != want {
			t.Errorf("ParseRate(%q) = %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"x/1m", "5", "5/", "0/1m"} {
		if _, err := ParseRate(bad); err == nil {
			t.Errorf("ParseRate(%q) should fail", bad)
		}
	}
}

func TestReadSecretRejectsOpenPermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s")
	os.WriteFile(p, []byte("secret\n"), 0o644)
	if _, err := ReadSecret(p); err == nil {
		t.Fatal("0644 secret must be rejected")
	}
	os.Chmod(p, 0o600)
	b, err := ReadSecret(p)
	if err != nil || string(b) != "secret" {
		t.Fatalf("got %q, %v", b, err)
	}
}

func TestPasswordDriverValidation(t *testing.T) {
	cases := map[string]string{
		"password: {driver: exec}":                                                   "password.exec.command",
		"password: {driver: exec, exec: {command: [helper]}}":                        "password.exec.command",
		"password: {driver: exec, exec: {command: [/usr/local/bin/x], timeout: 2m}}": "password.exec.timeout",
		"password: {driver: http, http: {url: 'http://panel.example.com/pw'}}":       "plain http",
		"password: {driver: http}":                                                   "password.http.url",
		"password: {driver: panel}":                                                  "password.driver",
		"password: {policy: {min_length: 4}}":                                        "min_length",
		"password: {policy: {min_length: 20, max_length: 10}}":                       "max_length",
		"password: {policy: {require_classes: 5}}":                                   "require_classes",
	}
	for body, want := range cases {
		_, err := Load(write(t, minimal+body+"\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v (want %q)", body, err, want)
		}
	}
	for _, body := range []string{
		"password: {driver: exec, exec: {command: [/usr/local/bin/webmail, passwd-file, -file, /etc/dovecot/users]}}",
		"password: {driver: http, http: {url: 'http://127.0.0.1:10041/webmail/password'}}",
		"password: {driver: http, http: {url: 'https://panel.example.com/api/pw'}}",
		"password: {driver: panel}\nadapters: {panel: {enabled: true}}",
	} {
		if _, err := Load(write(t, minimal+body+"\n")); err != nil {
			t.Errorf("%s: %v", body, err)
		}
	}
}

func TestBrandValidation(t *testing.T) {
	dir := t.TempDir()
	logo := filepath.Join(dir, "logo.svg")
	if err := os.WriteFile(logo, []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok := minimal + "ui:\n  brand:\n    name: Почта\n    logo_file: " + logo +
		"\n    favicon_url: https://cdn.example.com/f.ico\n    accent: \"#0b7a5c\"\n    accent_dark: \"#3fb\"\n" +
		"    css_vars: {\"--radius\": \"4px\"}\n"
	if _, err := Load(write(t, ok)); err != nil {
		t.Fatalf("valid brand rejected: %v", err)
	}
	for name, tc := range map[string]struct{ yaml, want string }{
		"empty name":   {"name: \"\"", "ui.brand.name"},
		"bad accent":   {"accent: green", "ui.brand.accent"},
		"both logos":   {"logo_url: /l.svg\n    logo_file: " + logo, "mutually exclusive"},
		"js url":       {"logo_url: javascript:alert(1)", "ui.brand.logo_url"},
		"protocol rel": {"favicon_url: //evil.example/f.ico", "ui.brand.favicon_url"},
		"missing file": {"logo_dark_file: " + filepath.Join(dir, "none.png"), "ui.brand.logo_dark_file"},
		"bad type":     {"logo_file: " + write(t, "x"), "unsupported type"},
		"bad var name": {"css_vars: {\"accent\": \"red\"}", "bad variable name"},
		"css inject":   {"css_vars: {\"--accent\": \"red; background: url(x)\"}", "forbidden"},
	} {
		_, err := Load(write(t, minimal+"ui:\n  brand:\n    "+tc.yaml+"\n"))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want error with %q, got %v", name, tc.want, err)
		}
	}
}
