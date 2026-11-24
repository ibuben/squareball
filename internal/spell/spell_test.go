package spell

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func TestParsePipe(t *testing.T) {
	out := "@(#) International Ispell Version 3.2.06 (but really Hunspell 1.7.2)\n" +
		"*\n\n" +
		"& превет 6 1: преет, ревет, поревет, прервет, привет, прет, лишний\n\n" +
		"# абырвалг 1\n\n" +
		"*\n*\n\n" + // кто-то: две части
		"*\n& то 1 5: тот\n\n" // часть составного слова с ошибкой
	words := []string{"привет", "превет", "абырвалг", "кто-то", "что-то"}
	got, err := ParsePipe([]byte(out), words)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"превет":   {"преет", "ревет", "поревет", "прервет", "привет", "прет"},
		"абырвалг": {},
		"что-то":   {"тот"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	if _, err := ParsePipe([]byte(out), words[:2]); err == nil {
		t.Fatal("extra output must fail")
	}
	if _, err := ParsePipe([]byte("oops\n"), words); err == nil {
		t.Fatal("bad banner must fail")
	}
}

func TestIsWord(t *testing.T) {
	for w, ok := range map[string]bool{
		"привет": true, "кто-то": true, "don't": true, "o‘zbek": true, "Ёлка": true,
		"": false, "-то": false, "кто-": false, "a1": false, "^cmd": false, "foo bar": false,
		"http://x": false, "a@b": false, "ab\n": false,
	} {
		if IsWord(w) != ok {
			t.Errorf("IsWord(%q) = %v", w, !ok)
		}
	}
	if got := CleanWords([]string{"да", "да", "1", "нет", "*"}); !reflect.DeepEqual(got, []string{"да", "нет"}) {
		t.Fatalf("CleanWords: %v", got)
	}
}

// С настоящим hunspell (apt install hunspell hunspell-ru hunspell-en-us).
func TestHunspell(t *testing.T) {
	if _, err := exec.LookPath("hunspell"); err != nil {
		t.Skip("hunspell not installed")
	}
	c, err := New("hunspell", []string{"ru_RU", "en_US", "xx_NOPE", "../etc"}, 5*time.Second)
	if c == nil {
		t.Skipf("dictionaries not installed: %v", err)
	}
	if err == nil || !reflect.DeepEqual(c.Dictionaries(), []string{"ru_RU", "en_US"}) {
		t.Fatalf("broken dictionaries must be dropped: %v %v", c.Dictionaries(), err)
	}
	ctx := context.Background()
	got, err := c.Check(ctx, nil, []string{"привет", "превет", "hello", "helo", "кто-то", "^x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["превет"] == nil || got["helo"] == nil {
		t.Fatalf("all dictionaries: %#v", got)
	}
	got, _ = c.Check(ctx, []string{"ru_RU"}, []string{"привет", "hello"})
	if _, bad := got["hello"]; !bad || len(got) != 1 {
		t.Fatalf("ru_RU only: %#v", got)
	}
	if _, err := c.Check(ctx, []string{"de_DE"}, []string{"x"}); err != ErrDictionary {
		t.Fatalf("unknown dictionary: %v", err)
	}
}
