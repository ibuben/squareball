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
