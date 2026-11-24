package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ibuben/squareball/internal/locale"
)

func TestLanguagePacks(t *testing.T) {
	t.Cleanup(locale.Reset)
	if err := locale.Register(&locale.Pack{Code: "uz", Name: "O'zbekcha", DateLocale: "uz-Latn-UZ",
		UI: map[string]string{"login.title": "Kirish"}, Server: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	c := testConfig(t)
	s := newServer(t, c)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	langs := func() []map[string]any {
		res, err := http.Get(ts.URL + "/api/v1/config")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body struct{ Languages []map[string]any }
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.Languages
	}
	l := langs()
	if len(l) != 3 || l[2]["code"] != "uz" || l[2]["name"] != "O'zbekcha" || l[2]["pack"] != true || l[2]["date_locale"] != "uz-Latn-UZ" || l[1]["pack"] != nil {
		t.Errorf("languages: %v", l)
	}
	c.UI.Languages = []string{"ru", "uz", "zz"} // неизвестный пропускается
	if l := langs(); len(l) != 2 || l[0]["code"] != "ru" || l[1]["code"] != "uz" {
		t.Errorf("restricted: %v", l)
	}

	res, err := http.Get(ts.URL + "/api/v1/locales/uz")
	if err != nil {
		t.Fatal(err)
	}
	var ui map[string]string
	json.NewDecoder(res.Body).Decode(&ui)
	res.Body.Close()
	if res.StatusCode != 200 || ui["login.title"] != "Kirish" {
		t.Errorf("locale: %d %v", res.StatusCode, ui)
	}
	res, _ = http.Get(ts.URL + "/api/v1/locales/ru") // встроенный — в сборке клиента
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("builtin: %d", res.StatusCode)
	}

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "wm_lang", Value: "uz"})
	if got := s.Lang(r); got != "uz" {
		t.Errorf("Lang = %s", got)
	}
}
