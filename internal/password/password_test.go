package password

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
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

	"golang.org/x/crypto/bcrypt"

	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/config"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestPolicy(t *testing.T) {
	c := config.Defaults().Password
	p := FromConfig(&c, &ext.PasswordPolicy{MinLength: 8, MaxLength: 64, ForbidChars: ":"})
	if p.MinLength != 10 || p.MaxLength != 64 || p.ForbidChars != ":" {
		t.Fatalf("merge: %+v", p)
	}
	cases := map[string]string{
		"Short1!":                  "too_short",
		strings.Repeat("Aa1!", 17): "too_long",
		"alllowercase":             "classes",
		"Пароль-Надёжный1":         "",
		"Tab\there-Pass1":          "chars",
		"With:Colon-11":            "chars",
		"Bob-Strong-Pass1":         "mailbox",
		"Quite-Strong-Pass1":       "",
	}
	for pw, want := range cases {
		err := p.Check("bob@test.local", pw)
		var v *Violation
		got := ""
		if errors.As(err, &v) {
			got = v.Reason
			if !errors.Is(err, ext.ErrWeakPassword) {
				t.Errorf("%q: not ErrWeakPassword", pw)
			}
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", pw, got, want)
		}
	}
}

func TestBreached(t *testing.T) {
	sum := sha1.Sum([]byte("password1"))
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) != 6 {
			t.Errorf("path %s leaks more than the prefix", r.URL.Path)
		}
		fmt.Fprintf(w, "0000000000000000000000000000000000A:0\r\n%s:42\r\n", h[5:])
	}))
	defer srv.Close()
	old := PwnedURL
	PwnedURL = srv.URL + "/"
	defer func() { PwnedURL = old }()
	if ok, err := Breached(context.Background(), srv.Client(), "password1"); err != nil || !ok {
		t.Fatalf("leaked password: %v %v", ok, err)
	}
	if ok, _ := Breached(context.Background(), srv.Client(), "not-in-list"); ok {
		t.Fatal("false positive")
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "helper.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecDriver(t *testing.T) {
	dir := t.TempDir()
	got := filepath.Join(dir, "stdin.json")
	req := ext.PasswordChange{Mailbox: "bob@test.local", Current: "old", New: "new-secret", RemoteIP: "192.0.2.1"}
	for code, want := range map[int]error{0: nil, 2: ext.ErrWrongPassword, 3: ext.ErrWeakPassword, 4: ext.ErrRateLimited, 1: ext.ErrUnavailable} {
		script := writeScript(t, fmt.Sprintf("cat > %s\necho \"argv: $*\" >&2\nexit %d\n", got, code))
		e := &Exec{cfg: config.PasswordExec{Command: []string{script, "--flag"}, Timeout: 5 * time.Second}, log: discard}
		err := e.Change(context.Background(), req)
		if (want == nil) != (err == nil) || (want != nil && !errors.Is(err, want)) {
			t.Errorf("exit %d: %v", code, err)
		}
		if code == 1 && !strings.Contains(err.Error(), "argv: --flag") {
			t.Errorf("stderr not reported: %v", err)
		}
	}
	var r Request
	b, _ := os.ReadFile(got)
	if json.Unmarshal(b, &r) != nil || r.User != "bob@test.local" || r.Current != "old" || r.Password != "new-secret" || r.Remote != "192.0.2.1" {
		t.Fatalf("stdin: %s", b)
	}
	// Пароли не попадают ни в argv, ни в окружение.
	script := writeScript(t, "env; echo \"$@\"; exit 1\n")
	err := (&Exec{cfg: config.PasswordExec{Command: []string{script}, Timeout: 5 * time.Second}}).Change(context.Background(), req)
	if strings.Contains(err.Error(), "new-secret") || strings.Contains(err.Error(), "old") && strings.Contains(err.Error(), "current") {
		t.Fatalf("password leaked: %v", err)
	}
	// Тайм-аут.
	script = writeScript(t, "sleep 5\n")
	start := time.Now()
	err = (&Exec{cfg: config.PasswordExec{Command: []string{script}, Timeout: 200 * time.Millisecond}}).Change(context.Background(), req)
	if !errors.Is(err, ext.ErrUnavailable) || time.Since(start) > 4*time.Second {
		t.Fatalf("timeout: %v after %v", err, time.Since(start))
	}
}

func TestHTTPDriver(t *testing.T) {
	status := http.StatusOK
	var got Request
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		io.WriteString(w, `{"error":"x"}`)
	}))
	defer srv.Close()
	tok := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tok, []byte("s3cret\n"), 0o600)
	h, err := NewHTTP(config.PasswordHTTP{URL: srv.URL, TokenFile: tok, Timeout: 2 * time.Second, TLSVerify: true}, discard)
	if err != nil {
		t.Fatal(err)
	}
	req := ext.PasswordChange{Mailbox: "bob@test.local", Current: "old", New: "new"}
	for st, want := range map[int]error{200: nil, 403: ext.ErrWrongPassword, 400: ext.ErrWeakPassword, 422: ext.ErrWeakPassword,
		429: ext.ErrRateLimited, 401: ext.ErrUnavailable, 500: ext.ErrUnavailable, 302: ext.ErrUnavailable} {
		status = st
		err := h.Change(context.Background(), req)
		if (want == nil) != (err == nil) || (want != nil && !errors.Is(err, want)) {
			t.Errorf("HTTP %d: %v", st, err)
		}
	}
	if auth != "Bearer s3cret" || got.User != "bob@test.local" || got.Password != "new" || got.Current != "old" {
		t.Fatalf("request: %q %+v", auth, got)
	}
	os.Chmod(tok, 0o644)
	if _, err := NewHTTP(config.PasswordHTTP{URL: srv.URL, TokenFile: tok}, discard); err == nil {
		t.Fatal("world-readable token accepted")
	}
}

func TestPasswdFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users")
	old, _ := bcrypt.GenerateFromPassword([]byte("old-pass"), bcrypt.MinCost)
	content := "# comment\nalice@test.local:{PLAIN}x::::::\nBob@test.local:{BLF-CRYPT}" + string(old) + ":5000:5000::/home/bob::userdb_quota_rule=*:storage=1G\n"
	os.WriteFile(path, []byte(content), 0o640)
	run := func(verify bool, req Request) error {
		b, _ := json.Marshal(req)
		return (&PasswdFile{Path: path, VerifyCurrent: verify, Cost: bcrypt.MinCost}).Run(strings.NewReader(string(b)))
	}
	if err := run(true, Request{User: "bob@test.local", Current: "wrong", Password: "new-pass"}); ExitCode(err) != ExitWrongCurrent {
		t.Fatalf("wrong current: %v", err)
	}
	if err := run(true, Request{User: "bob@test.local", Current: "old-pass", Password: "new-pass"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	lines := strings.Split(string(b), "\n")
	if lines[0] != "# comment" || lines[1] != "alice@test.local:{PLAIN}x::::::" {
		t.Fatalf("other lines changed:\n%s", b)
	}
	f := strings.SplitN(lines[2], ":", 3)
	if f[0] != "Bob@test.local" || f[2] != "5000:5000::/home/bob::userdb_quota_rule=*:storage=1G" ||
		bcrypt.CompareHashAndPassword([]byte(strings.TrimPrefix(f[1], "{BLF-CRYPT}")), []byte("new-pass")) != nil {
		t.Fatalf("bob line: %q", lines[2])
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	// Две смены подряд в одну секунду: mtime всё равно растёт (Dovecot смотрит на него).
	st1, _ := os.Stat(path)
	if err := run(false, Request{User: "bob@test.local", Password: "third-pass"}); err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(path)
	if !st2.ModTime().Truncate(time.Second).After(st1.ModTime().Truncate(time.Second)) {
		t.Fatalf("mtime did not advance: %v -> %v", st1.ModTime(), st2.ModTime())
	}
	if err := run(false, Request{User: "carol@test.local", Password: "x"}); err == nil || ExitCode(err) != 1 {
		t.Fatalf("unknown user: %v", err)
	}
	if err := run(false, Request{User: "bob@test.local", Password: strings.Repeat("x", 73)}); ExitCode(err) != ExitWeak {
		t.Fatalf("73 bytes: %v", err)
	}
	if err := run(false, Request{User: "bob@test.local:x", Password: "y"}); err == nil {
		t.Fatal("colon in user accepted")
	}
}
