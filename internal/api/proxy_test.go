package api

import (
	"net/http/httptest"
	"testing"
)

func TestBasePathOnlyFromTrustedProxy(t *testing.T) {
	s := newServer(t, testConfig(t))
	for _, tc := range []struct{ remote, prefix, want string }{
		{"127.0.0.1:5000", "/webmail", "/webmail"},
		{"127.0.0.1:5000", "/webmail/", "/webmail"},
		{"127.0.0.1:5000", "/a/b", "/a/b"},
		{"127.0.0.1:5000", "", ""},
		{"127.0.0.1:5000", "/../x", ""},
		{"127.0.0.1:5000", "//evil.example", ""},
		{"127.0.0.1:5000", "/x\"><script>", ""},
		{"127.0.0.1:5000", "webmail", ""},
		{"198.51.100.9:5000", "/webmail", ""},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Forwarded-Prefix", tc.prefix)
		if got := s.BasePath(r); got != tc.want {
			t.Errorf("%s %q: got %q, want %q", tc.remote, tc.prefix, got, tc.want)
		}
	}
}

func TestSameOriginMultiHost(t *testing.T) {
	c := testConfig(t)
	c.Server.PublicURL = "https://mail.example.com"
	req := func(remote, host, origin string) bool {
		s := newServer(t, c)
		r := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
		r.RemoteAddr, r.Host = remote, host
		r.Header.Set("Origin", origin)
		r.Header.Set("X-Forwarded-Proto", "https")
		return s.sameOrigin(r)
	}
	if !req("127.0.0.1:1", "mail.example.com", "https://mail.example.com") {
		t.Error("public_url rejected")
	}
	if req("127.0.0.1:1", "webmail.example.org", "https://webmail.example.org") {
		t.Error("other host accepted without multi_host")
	}
	c.Server.MultiHost = true
	if !req("127.0.0.1:1", "webmail.example.org", "https://webmail.example.org") {
		t.Error("request host rejected with multi_host")
	}
	if !req("127.0.0.1:1", "panel.example.org:8889", "https://panel.example.org:8889") {
		t.Error("host with port rejected")
	}
	if req("127.0.0.1:1", "webmail.example.org", "https://evil.example") {
		t.Error("foreign origin accepted")
	}
	if req("127.0.0.1:1", "webmail.example.org", "http://webmail.example.org") {
		t.Error("scheme mismatch accepted")
	}
	if req("198.51.100.9:1", "webmail.example.org", "https://webmail.example.org") {
		t.Error("untrusted peer accepted")
	}
}
