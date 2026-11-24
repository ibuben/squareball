// Package spell — серверная проверка орфографии через Hunspell.
//
// Hunspell запускается в режиме ispell-конвейера (-a) на каждый запрос:
// словари грузятся ~0,1 с, а клиент кэширует ответы и спрашивает только
// новые слова. Команда вызывается без оболочки; каждое слово идёт отдельной
// строкой с префиксом "^", поэтому текст не может стать командой конвейера.
package spell

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxWords       = 1000 // слов в одном запросе
	MaxWordLen     = 64   // символов в слове
	maxSuggestions = 6
	maxParallel    = 4 // одновременных процессов hunspell
)

var ErrDictionary = errors.New("unknown dictionary")

// Checker проверяет слова по словарям из списка, разрешённого конфигом.
type Checker struct {
	command string
	dicts   []string
	timeout time.Duration
	sem     chan struct{}
}

var dictName = regexp.MustCompile(`^[A-Za-z]{2,3}(_[A-Za-z0-9]{2,8})*$`)

// ValidDictName — имя словаря вида ru_RU, en_US, de_DE_frami.
func ValidDictName(s string) bool { return dictName.MatchString(s) }

// New проверяет, что команда и словари работают, и возвращает проверяльщик
// только с рабочими словарями. Нерабочие перечисляются в ошибке (не фатальной,
// если хоть один словарь работает).
func New(command string, dicts []string, timeout time.Duration) (*Checker, error) {
	c := &Checker{command: command, timeout: timeout, sem: make(chan struct{}, maxParallel)}
	var bad []string
	for _, d := range dicts {
		if !ValidDictName(d) {
			bad = append(bad, d)
			continue
		}
		if _, err := c.run(context.Background(), []string{d}, []string{"test"}); err != nil {
			bad = append(bad, fmt.Sprintf("%s (%v)", d, err))
			continue
		}
		c.dicts = append(c.dicts, d)
	}
	var err error
	if len(bad) > 0 {
		err = fmt.Errorf("spellcheck: unavailable dictionaries: %s", strings.Join(bad, ", "))
	}
	if len(c.dicts) == 0 {
		return nil, err
	}
	return c, err
}

// Dictionaries — рабочие словари в порядке из конфига.
func (c *Checker) Dictionaries() []string { return append([]string(nil), c.dicts...) }

// Check возвращает ошибочные слова с вариантами исправления (возможно, пустыми).
// dicts — подмножество Dictionaries(); пусто — все сразу (смешанный текст).
func (c *Checker) Check(ctx context.Context, dicts []string, words []string) (map[string][]string, error) {
	if len(dicts) == 0 {
		dicts = c.dicts
	}
	for _, d := range dicts {
		if !contains(c.dicts, d) {
			return nil, ErrDictionary
		}
	}
	words = CleanWords(words)
	if len(words) == 0 {
		return map[string][]string{}, nil
	}
	return c.run(ctx, dicts, words)
}

func (c *Checker) run(ctx context.Context, dicts, words []string) (map[string][]string, error) {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var in bytes.Buffer
	for _, w := range words {
		in.WriteString("^")
		in.WriteString(w)
		in.WriteString("\n")
	}
	cmd := exec.CommandContext(ctx, c.command, "-a", "-i", "utf-8", "-d", strings.Join(dicts, ","))
	cmd.Stdin = &in
	cmd.Env = []string{"LC_ALL=C.UTF-8"}
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("hunspell: %w: %s", err, msg)
	}
	// Ненайденный словарь hunspell печатает в stderr, но код выхода 0.
	if strings.Contains(stderr.String(), "Can't open affix or dictionary") {
		return nil, fmt.Errorf("hunspell: %s", strings.TrimSpace(stderr.String()))
	}
	return ParsePipe(out.Bytes(), words)
}

// ParsePipe разбирает вывод "hunspell -a": после строки-заголовка на каждую
// входную строку идут строки результатов и пустая строка. Слово с дефисом
// hunspell может проверить по частям — тогда слово ошибочно, если ошибочна
// хоть одна часть.
func ParsePipe(out []byte, words []string) (map[string][]string, error) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	if !sc.Scan() || !strings.HasPrefix(sc.Text(), "@(#)") {
		return nil, errors.New("hunspell: unexpected banner")
	}
	res := map[string][]string{}
	i := 0
	var sugg []string
	bad := false
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if i >= len(words) {
				return nil, errors.New("hunspell: extra output")
			}
			if bad {
				if sugg == nil {
					sugg = []string{}
				}
				res[words[i]] = sugg
			}
			i, sugg, bad = i+1, nil, false
			continue
		}
		switch line[0] {
		case '&': // & слово N смещение: в1, в2
			bad = true
			if k := strings.Index(line, ": "); k >= 0 && len(sugg) < maxSuggestions {
				for _, s := range strings.Split(line[k+2:], ", ") {
					if s = strings.TrimSpace(s); s != "" && len(sugg) < maxSuggestions {
						sugg = append(sugg, s)
					}
				}
			}
		case '#':
			bad = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if i != len(words) {
		return nil, fmt.Errorf("hunspell: got %d results for %d words", i, len(words))
	}
	return res, nil
}

// CleanWords оставляет уникальные слова, которые есть смысл проверять: из букв
// (и дефиса или апострофа внутри), без цифр, не длиннее MaxWordLen и не больше MaxWords.
func CleanWords(words []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range words {
		if seen[w] || !IsWord(w) {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) == MaxWords {
			break
		}
	}
	return out
}

// IsWord — проверяемое слово: буквы и метки, дефис и апостроф только внутри.
func IsWord(w string) bool {
	if w == "" || !utf8.ValidString(w) || utf8.RuneCountInString(w) > MaxWordLen {
		return false
	}
	letters := 0
	prevSep := true
	for _, r := range w {
		switch {
		case unicode.IsLetter(r):
			letters++
			prevSep = false
		case unicode.Is(unicode.M, r):
			prevSep = false
		case r == '-' || r == '\'' || r == '’' || r == 'ʼ' || r == 'ʻ' || r == '‘':
			if prevSep {
				return false
			}
			prevSep = true
		default:
			return false
		}
	}
	return letters > 0 && !prevSep
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
