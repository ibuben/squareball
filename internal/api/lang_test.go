package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLang(t *testing.T) {
	c := testConfig(t)
	s := newServer(t, c)
	cases := []struct {
		cookie, accept, want string
	}{
		{"", "", "en"},                  // ui.default_language
		{"ru", "", "ru"},                // выбор в интерфейсе
		{"ru", "en-US,en;q=0.9", "ru"},  // cookie важнее браузера
		{"", "ru-RU,ru;q=0.9,en", "ru"}, // язык браузера
		{"", "de-DE,fr;q=0.8", "en"},    // нет ни одного нашего
		{"xx", "fr, ru;q=0.5", "ru"},    // чужая cookie пропускается
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if tc.cookie != "" {
			r.AddCookie(&http.Cookie{Name: "wm_lang", Value: tc.cookie})
		}
		if tc.accept != "" {
			r.Header.Set("Accept-Language", tc.accept)
		}
		if got := s.Lang(r); got != tc.want {
			t.Errorf("cookie %q accept %q: %q, want %q", tc.cookie, tc.accept, got, tc.want)
		}
	}

	// Язык, выключенный в ui.languages, не выбирается; по умолчанию — ui.default_language.
	c.UI.Languages, c.UI.DefaultLanguage = []string{"ru"}, "ru"
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "wm_lang", Value: "en"})
	r.Header.Set("Accept-Language", "en")
	if got := s.Lang(r); got != "ru" {
		t.Errorf("disabled language chosen: %q", got)
	}
}
