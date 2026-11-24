package locale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndFallback(t *testing.T) {
	t.Cleanup(Reset)
	dir := t.TempDir()
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("uz.json", `{"name":"O'zbekcha","date_locale":"uz-Latn-UZ","ui":{"login.title":"Kirish"},"server":{"sieve.vacation":"Avtojavob"}}`)
	write("bad.json", `{`)
	write("xx.json", `{"ui":{}}`) // без названия
	write("readme.txt", `-`)

	codes, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "bad.json") || !strings.Contains(err.Error(), "name is required") {
		t.Errorf("errors: %v", err)
	}
	if strings.Join(codes, ",") != "uz" {
		t.Fatalf("loaded %v", codes)
	}
	if got := strings.Join(All(), ","); got != "en,ru,uz" {
		t.Errorf("All = %s", got)
	}
	if !Known("uz") || Known("xx") || !Known("ru") {
		t.Error("Known")
	}
	if Name("uz") != "O'zbekcha" || Name("ru") != "Русский" || DateLocale("uz") != "uz-Latn-UZ" {
		t.Error("Name/DateLocale")
	}
	if UI("uz")["login.title"] != "Kirish" || UI("ru") != nil {
		t.Error("UI")
	}
	// Перевод из пакета, непереведённое — английский, неизвестный язык — английский.
	if S("uz", "sieve.vacation") != "Avtojavob" || S("uz", "sieve.forward") != "Forwarding" ||
		S("ru", "sieve.forward") != "Пересылка" || S("zz", "sieve.forward") != "Forwarding" {
		t.Error("S fallback")
	}
	if _, err := Load(filepath.Join(dir, "missing")); err != nil {
		t.Errorf("missing dir: %v", err)
	}
}

// Встроенные языки переведены целиком, с теми же подстановками.
func TestBuiltinComplete(t *testing.T) {
	for lang, m := range Server {
		for k, v := range Server["en"] {
			got, ok := m[k]
			if !ok {
				t.Errorf("%s: no %s", lang, k)
			}
			if strings.Count(got, "%") != strings.Count(v, "%") {
				t.Errorf("%s: %s: verbs differ", lang, k)
			}
		}
	}
}
