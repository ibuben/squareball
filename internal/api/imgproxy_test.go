package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ibuben/squareball/internal/netguard"
	"github.com/ibuben/squareball/internal/sanitize"
)

func TestImageProxy(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" {
			t.Errorf("user data leaked upstream: %v", r.Header)
		}
		switch r.URL.Path {
		case "/a.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(pngBytes)
		case "/x.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Write([]byte(`<svg onload="alert(1)"/>`))
		default:
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<script>"))
		}
	}))
	defer up.Close()
	s := newServer(t, testConfig(t))
	get := func(link string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleImageProxy(rec, httptest.NewRequest("GET", link, nil))
		return rec
	}

	// По умолчанию — только публичные адреса: httptest слушает 127.0.0.1.
	if rec := get(s.proxyImageURL(up.URL + "/a.png")); rec.Code != http.StatusForbidden {
		t.Fatalf("loopback must be refused: %d", rec.Code)
	}
	s.imgClient = netguard.Client(5*time.Second, true)
	rec := get(s.proxyImageURL(up.URL + "/a.png"))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Body.Len() != len(pngBytes) ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("png: %d %v", rec.Code, rec.Header())
	}
	if rec := get(s.proxyImageURL(up.URL + "/x.svg")); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("svg: %d", rec.Code)
	}
	if rec := get(s.proxyImageURL(up.URL + "/page")); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("html: %d", rec.Code)
	}
	forged := strings.Replace(s.proxyImageURL(up.URL+"/a.png"), "a.png", "b.png", 1)
	if rec := get(forged); rec.Code != http.StatusForbidden {
		t.Fatalf("forged signature: %d", rec.Code)
	}

	// Санитайзер переписывает внешние картинки на прокси, ответ — обратно.
	html := sanitize.Email(`<p><img src="https://track.example/p.gif?id=1&amp;x=2"><img src="//cdn.example/l.png"></p>`,
		func(string) string { return "" }, s.proxyImageURL).HTML
	if strings.Contains(html, `src="https://`) || strings.Count(html, `src="/api/v1/img?u=`) != 2 {
		t.Fatalf("not proxied: %s", html)
	}
	link := s.proxyImageURL("https://track.example/p.gif?id=1&x=2")
	if orig, ok := s.unproxyImageURL(link); !ok || orig != "https://track.example/p.gif?id=1&x=2" {
		t.Fatalf("unproxy: %q %v", orig, ok)
	}
	if _, ok := s.unproxyImageURL(strings.Replace(link, "id%3D1", "id%3D2", 1)); ok {
		t.Fatal("tampered link accepted")
	}
}

func TestMetrics(t *testing.T) {
	s := newServer(t, testConfig(t))
	h := s.Handler()
	for i := 0; i < 3; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/folders", nil))
	}
	rec := httptest.NewRecorder()
	s.MetricsHandler("v-test").ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	out := rec.Body.String()
	for _, want := range []string{
		`webmail_build_info{version="v-test"`,
		`webmail_http_requests_total{route="GET /api/v1/folders",code="401"} 3`,
		`webmail_http_request_duration_seconds_count{route="GET /api/v1/folders"} 3`,
		"webmail_sessions 0", "go_goroutines",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}
