package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ibuben/squareball/ext"
)

type fakeBrand struct{ b ext.Brand }

func (f fakeBrand) Brand(context.Context, string) (ext.Brand, error) { return f.b, nil }

func getBrand(t *testing.T, base string) ext.Brand {
	t.Helper()
	res, err := http.Get(base + "/api/v1/config")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct{ Brand ext.Brand }
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Brand
}

func TestBrandFromConfigAndProvider(t *testing.T) {
	logo := filepath.Join(t.TempDir(), "logo.svg")
	svg := `<svg xmlns="http://www.w3.org/2000/svg"/>`
	if err := os.WriteFile(logo, []byte(svg), 0o644); err != nil {
		t.Fatal(err)
	}
	c := testConfig(t)
	c.UI.Brand.Name = "Почта Ромашки"
	c.UI.Brand.LogoFile = logo
	c.UI.Brand.Accent = "#0b7a5c"
	c.UI.Brand.CSSVars = map[string]string{"--radius": "4px"}
	s := newServer(t, c)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	b := getBrand(t, ts.URL)
	if b.Name != "Почта Ромашки" || b.Accent != "#0b7a5c" || !strings.HasPrefix(b.LogoURL, "/brand/logo?v=") {
		t.Fatalf("brand: %+v", b)
	}

	res, err := http.Get(ts.URL + b.LogoURL)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(data) != svg || res.Header.Get("Content-Type") != "image/svg+xml" ||
		!strings.Contains(res.Header.Get("Cache-Control"), "immutable") ||
		!strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("logo: %q %v", data, res.Header)
	}
	if res, _ := http.Get(ts.URL + "/brand/favicon"); res.StatusCode != 404 {
		t.Errorf("unset favicon: status %d", res.StatusCode)
	}

	// Провайдер переопределяет только заданные и корректные поля.
	s.SetBrandProvider(fakeBrand{ext.Brand{
		Name: "Хостинг", Accent: "red", AccentDark: "#334455", LogoDarkURL: "javascript:x",
		CSSVars: map[string]string{"--accent-soft": "#eef", "--bad": "x;y"},
	}})
	b = getBrand(t, ts.URL)
	if b.Name != "Хостинг" || b.Accent != "#0b7a5c" || b.AccentDark != "#334455" || b.LogoDarkURL != "" ||
		!strings.HasPrefix(b.LogoURL, "/brand/logo") ||
		b.CSSVars["--radius"] != "4px" || b.CSSVars["--accent-soft"] != "#eef" || b.CSSVars["--bad"] != "" {
		t.Errorf("merged brand: %+v", b)
	}
}

func TestBrandHideAbout(t *testing.T) {
	c := testConfig(t)
	s := newServer(t, c)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	if getBrand(t, ts.URL).HideAbout {
		t.Fatal("about hidden by default")
	}
	// Провайдер может скрыть окно «О программе», но не вернуть скрытое конфигом.
	s.SetBrandProvider(fakeBrand{ext.Brand{HideAbout: true}})
	if !getBrand(t, ts.URL).HideAbout {
		t.Error("provider hide_about ignored")
	}
	c.UI.Brand.HideAbout = true
	s.SetBrandProvider(fakeBrand{ext.Brand{Name: "X"}})
	if !getBrand(t, ts.URL).HideAbout {
		t.Error("config hide_about ignored")
	}
}
