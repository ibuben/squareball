package sieve

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Модель фильтров, которую показывает форма. Скрипт хранится в формате
// Roundcube managesieve: каждое правило — "# rule:[Имя]" + if { ... }.

type Condition struct {
	Field  string `json:"field"`            // all | from | to | cc | to_or_cc | subject | header | body | size
	Header string `json:"header,omitempty"` // для field = header
	Op     string `json:"op"`               // contains | not_contains | is | not_is | matches | not_matches | exists | not_exists | over | under
	Value  string `json:"value,omitempty"`  // для size — "100K", "2M"
}

type Action struct {
	Type  string `json:"type"`            // fileinto | copyto | redirect | redirect_copy | discard | reject | seen | flag | keep
	Value string `json:"value,omitempty"` // папка, адрес или текст отказа
}

type Rule struct {
	Name       string      `json:"name"`
	Enabled    bool        `json:"enabled"`
	Match      string      `json:"match"` // all | any
	Conditions []Condition `json:"conditions"`
	Actions    []Action    `json:"actions"`
	Stop       bool        `json:"stop"`
}

type Vacation struct {
	Enabled   bool     `json:"enabled"`
	Subject   string   `json:"subject"`
	Body      string   `json:"body"`
	Days      int      `json:"days"`
	Addresses []string `json:"addresses,omitempty"` // дополнительные адреса ящика (алиасы)
	Start     string   `json:"start,omitempty"`     // YYYY-MM-DD, включительно
	End       string   `json:"end,omitempty"`
}

type Forward struct {
	Enabled  bool   `json:"enabled"`
	To       string `json:"to"`
	KeepCopy bool   `json:"keep_copy"`
}

type Ruleset struct {
	Rules    []Rule   `json:"rules"`
	Vacation Vacation `json:"vacation"`
	Forward  Forward  `json:"forward"`
}

// ErrUnsupported — в скрипте есть конструкции, которые форма не умеет
// показать без потерь; такой скрипт редактируется как текст.
var ErrUnsupported = errors.New("script uses constructs not supported by the rule editor")

func unsupported(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, fmt.Sprintf(f, a...))
}

const (
	vacationName = "Автоответ"
	forwardName  = "Пересылка"
)

var ruleMarker = regexp.MustCompile(`^\s*rule:\[(.*)\]\s*$`)

// --- скрипт → модель ------------------------------------------------------------

func FromScript(src string) (*Ruleset, error) {
	rs := &Ruleset{Rules: []Rule{}, Vacation: Vacation{Days: 7}}
	if strings.TrimSpace(src) == "" {
		return rs, nil
	}
	cmds, err := Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	for _, c := range cmds {
		switch c.Name {
		case "require":
			continue
		case "if":
			name := ""
			for _, cm := range c.Comments {
				if m := ruleMarker.FindStringSubmatch(cm); m != nil {
					name = m[1]
				}
			}
			if len(c.Tests) != 1 || !c.HasBlock {
				return nil, unsupported("malformed if")
			}
			enabled, test, err := unwrapDisabled(c.Tests[0])
			if err != nil {
				return nil, err
			}
			if v, ok, err := asVacation(test, c.Block); err != nil {
				return nil, err
			} else if ok {
				if rs.Vacation.Enabled || rs.Vacation.Body != "" {
					return nil, unsupported("more than one vacation rule")
				}
				v.Enabled = enabled
				rs.Vacation = v
				continue
			}
			if f, ok := asForward(test, c.Block); ok && rs.Forward.To == "" {
				f.Enabled = enabled
				rs.Forward = f
				continue
			}
			r, err := asRule(test, c.Block)
			if err != nil {
				return nil, err
			}
			r.Name, r.Enabled = name, enabled
			rs.Rules = append(rs.Rules, r)
		default:
			return nil, unsupported("top-level command %q", c.Name)
		}
	}
	return rs, nil
}

// unwrapDisabled: "allof (false, T)" (наш формат) и "false # T" (Roundcube).
func unwrapDisabled(t Test) (bool, Test, error) {
	if t.Name == "allof" && len(t.Tests) == 2 && t.Tests[0].Name == "false" {
		return false, t.Tests[1], nil
	}
	if t.Name == "false" {
		body := strings.TrimSpace(t.Trailing)
		if body == "" {
			return false, Test{Name: "true"}, nil
		}
		cmds, err := Parse("if " + body + " {}")
		if err != nil || len(cmds) != 1 || len(cmds[0].Tests) != 1 {
			return false, t, unsupported("disabled rule with unreadable test")
		}
		return false, cmds[0].Tests[0], nil
	}
	return true, t, nil
}

func asRule(test Test, block []Command) (Rule, error) {
	r := Rule{Match: "all", Conditions: []Condition{}, Actions: []Action{}}
	switch test.Name {
	case "true":
		r.Conditions = append(r.Conditions, Condition{Field: "all"})
	case "anyof", "allof":
		if test.Name == "anyof" {
			r.Match = "any"
		}
		for _, t := range test.Tests {
			c, err := asCondition(t)
			if err != nil {
				return r, err
			}
			r.Conditions = append(r.Conditions, c)
		}
	default:
		c, err := asCondition(test)
		if err != nil {
			return r, err
		}
		r.Conditions = append(r.Conditions, c)
	}
	for i, cmd := range block {
		a, stop, err := asAction(cmd)
		if err != nil {
			return r, err
		}
		if stop {
			if i != len(block)-1 {
				return r, unsupported("stop in the middle of a rule")
			}
			r.Stop = true
			continue
		}
		r.Actions = append(r.Actions, a)
	}
	return r, nil
}

// matchArgs разбирает аргументы header/address/body: тип сравнения и строки.
func matchArgs(args []Arg) (op string, strs [][]string, err error) {
	op = "is"
	for _, a := range args {
		switch a.Kind {
		case ArgTag:
			switch a.Tag {
			case "contains", "is", "matches":
				op = a.Tag
			case "all", "localpart", "domain", "text", "comparator":
				if a.Tag == "localpart" || a.Tag == "domain" {
					return "", nil, unsupported("address part :%s", a.Tag)
				}
			default:
				return "", nil, unsupported("match type :%s", a.Tag)
			}
		case ArgString, ArgStrings:
			strs = append(strs, a.Strs)
		default:
			return "", nil, unsupported("unexpected number argument")
		}
	}
	// :comparator "i;ascii-casemap" — его строка попала в strs первой.
	if len(strs) == 3 {
		strs = strs[1:]
	}
	return op, strs, nil
}

func fieldFor(headers []string) (field, header string, err error) {
	if len(headers) == 2 {
		a, b := strings.ToLower(headers[0]), strings.ToLower(headers[1])
		if (a == "to" && b == "cc") || (a == "cc" && b == "to") {
			return "to_or_cc", "", nil
		}
	}
	if len(headers) != 1 {
		return "", "", unsupported("several headers in one test")
	}
	switch h := strings.ToLower(headers[0]); h {
	case "from", "to", "cc", "subject":
		return h, "", nil
	default:
		return "header", headers[0], nil
	}
}

func asCondition(t Test) (Condition, error) {
	neg := false
	if t.Name == "not" {
		if len(t.Tests) != 1 {
			return Condition{}, unsupported("malformed not")
		}
		neg, t = true, t.Tests[0]
	}
	var c Condition
	switch t.Name {
	case "header", "address":
		op, strs, err := matchArgs(t.Args)
		if err != nil {
			return c, err
		}
		if len(strs) != 2 || len(strs[1]) != 1 {
			return c, unsupported("several values in one condition")
		}
		if c.Field, c.Header, err = fieldFor(strs[0]); err != nil {
			return c, err
		}
		c.Op, c.Value = op, strs[1][0]
	case "body":
		op, strs, err := matchArgs(t.Args)
		if err != nil {
			return c, err
		}
		if len(strs) != 1 || len(strs[0]) != 1 {
			return c, unsupported("body test with several values")
		}
		c.Field, c.Op, c.Value = "body", op, strs[0][0]
	case "exists":
		if len(t.Args) != 1 || len(t.Args[0].Strs) != 1 {
			return c, unsupported("exists with several headers")
		}
		c.Field, c.Op = "header", "exists"
		c.Header = t.Args[0].Strs[0]
		if neg {
			c.Op, neg = "not_exists", false
		}
		return c, nil
	case "size":
		if neg || len(t.Args) != 2 || t.Args[0].Kind != ArgTag || t.Args[1].Kind != ArgNumber {
			return c, unsupported("size test")
		}
		c.Field, c.Op, c.Value = "size", t.Args[0].Tag, sizeString(t.Args[1].Num)
		if c.Op != "over" && c.Op != "under" {
			return c, unsupported("size :%s", c.Op)
		}
		return c, nil
	default:
		return c, unsupported("test %q", t.Name)
	}
	if neg {
		c.Op = "not_" + c.Op
	}
	return c, nil
}

func sizeString(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + "M"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.FormatInt(n>>10, 10) + "K"
	}
	return strconv.FormatInt(n, 10)
}

func singleString(args []Arg) (string, bool) {
	if len(args) == 1 && args[0].Kind == ArgString {
		return args[0].Strs[0], true
	}
	return "", false
}

func asAction(cmd Command) (Action, bool, error) {
	if cmd.HasBlock || len(cmd.Tests) > 0 {
		return Action{}, false, unsupported("nested command %q", cmd.Name)
	}
	hasCopy := false
	var rest []Arg
	for _, a := range cmd.Args {
		if a.Kind == ArgTag && a.Tag == "copy" {
			hasCopy = true
			continue
		}
		rest = append(rest, a)
	}
	switch cmd.Name {
	case "stop":
		return Action{}, true, nil
	case "keep", "discard":
		return Action{Type: cmd.Name}, false, nil
	case "fileinto", "redirect":
		v, ok := singleString(rest)
		if !ok {
			return Action{}, false, unsupported("%s arguments", cmd.Name)
		}
		t := cmd.Name
		if hasCopy {
			t = map[string]string{"fileinto": "copyto", "redirect": "redirect_copy"}[t]
		}
		return Action{Type: t, Value: v}, false, nil
	case "reject", "ereject":
		v, ok := singleString(rest)
		if !ok {
			return Action{}, false, unsupported("reject arguments")
		}
		return Action{Type: "reject", Value: v}, false, nil
	case "addflag", "setflag":
		v, ok := singleString(rest)
		if !ok {
			if len(rest) == 1 && rest[0].Kind == ArgStrings && len(rest[0].Strs) == 1 {
				v, ok = rest[0].Strs[0], true
			}
		}
		switch {
		case ok && strings.EqualFold(v, `\Seen`):
			return Action{Type: "seen"}, false, nil
		case ok && strings.EqualFold(v, `\Flagged`):
			return Action{Type: "flag"}, false, nil
		}
		return Action{}, false, unsupported("flag %q", v)
	}
	return Action{}, false, unsupported("action %q", cmd.Name)
}

var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// asVacation распознаёт правило, единственное действие которого — vacation;
// условие — true или диапазон дат через currentdate.
func asVacation(test Test, block []Command) (Vacation, bool, error) {
	if len(block) != 1 || block[0].Name != "vacation" {
		return Vacation{}, false, nil
	}
	v := Vacation{Days: 7}
	switch test.Name {
	case "true":
	case "allof", "currentdate":
		tests := test.Tests
		if test.Name == "currentdate" {
			tests = []Test{test}
		}
		for _, t := range tests {
			if t.Name != "currentdate" {
				return v, false, unsupported("vacation condition %q", t.Name)
			}
			var rel, date string
			var strs []string
			for _, a := range t.Args {
				if a.Kind == ArgString || a.Kind == ArgStrings {
					strs = append(strs, a.Strs...)
				}
			}
			// [:zone "+0300"] :value "ge" "date" "2026-01-01"
			if n := len(strs); n >= 3 && strs[n-2] == "date" {
				rel, date = strs[n-3], strs[n-1]
			}
			if !isoDate.MatchString(date) {
				return v, false, unsupported("vacation date")
			}
			switch rel {
			case "ge":
				v.Start = date
			case "le":
				v.End = date
			default:
				return v, false, unsupported("vacation date relation %q", rel)
			}
		}
	default:
		return v, false, nil
	}
	args := block[0].Args
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a.Kind != ArgTag {
			if i != len(args)-1 || a.Kind != ArgString {
				return v, false, unsupported("vacation arguments")
			}
			v.Body = a.Strs[0]
			continue
		}
		if i+1 >= len(args) {
			return v, false, unsupported("vacation :%s without value", a.Tag)
		}
		val := args[i+1]
		i++
		switch a.Tag {
		case "days":
			if val.Kind != ArgNumber {
				return v, false, unsupported("vacation :days")
			}
			v.Days = int(val.Num)
		case "subject":
			if val.Kind != ArgString {
				return v, false, unsupported("vacation :subject")
			}
			v.Subject = val.Strs[0]
		case "addresses":
			v.Addresses = append(v.Addresses, val.Strs...)
		default:
			return v, false, unsupported("vacation :%s", a.Tag)
		}
	}
	return v, true, nil
}

// asForward распознаёт "if true { redirect [:copy] "x"; [stop;] }".
func asForward(test Test, block []Command) (Forward, bool) {
	if test.Name != "true" || len(block) == 0 || len(block) > 2 || block[0].Name != "redirect" {
		return Forward{}, false
	}
	a, _, err := asAction(block[0])
	if err != nil {
		return Forward{}, false
	}
	if len(block) == 2 && block[1].Name != "stop" {
		return Forward{}, false
	}
	return Forward{To: a.Value, KeepCopy: a.Type == "redirect_copy"}, true
}

// --- модель → скрипт ------------------------------------------------------------

func q(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func qlist(list []string) string {
	parts := make([]string, len(list))
	for i, s := range list {
		parts[i] = q(s)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

var sizeRe = regexp.MustCompile(`^(?i)\d+[KMG]?$`)

// Validate проверяет модель до генерации; ошибки показываются в форме.
func (rs *Ruleset) Validate() error {
	for i, r := range rs.Rules {
		if strings.ContainsAny(r.Name, "[]\r\n") {
			return fmt.Errorf("rule %d: name must not contain [ ] or line breaks", i+1)
		}
		if len(r.Conditions) == 0 || len(r.Actions) == 0 && !r.Stop {
			return fmt.Errorf("rule %q: needs at least one condition and one action", r.Name)
		}
		for _, c := range r.Conditions {
			if err := c.validate(); err != nil {
				return fmt.Errorf("rule %q: %w", r.Name, err)
			}
		}
		for _, a := range r.Actions {
			switch a.Type {
			case "fileinto", "copyto", "redirect", "redirect_copy", "reject":
				if strings.TrimSpace(a.Value) == "" {
					return fmt.Errorf("rule %q: action %s needs a value", r.Name, a.Type)
				}
			case "discard", "seen", "flag", "keep":
			default:
				return fmt.Errorf("rule %q: unknown action %q", r.Name, a.Type)
			}
		}
	}
	if v := rs.Vacation; v.Enabled || v.Body != "" {
		if v.Enabled && strings.TrimSpace(v.Body) == "" {
			return errors.New("vacation: text is required")
		}
		if v.Days < 1 || v.Days > 365 {
			return errors.New("vacation: days must be 1..365")
		}
		for _, d := range []string{v.Start, v.End} {
			if d != "" && !isoDate.MatchString(d) {
				return errors.New("vacation: dates must be YYYY-MM-DD")
			}
		}
	}
	if f := rs.Forward; f.Enabled && !strings.Contains(f.To, "@") {
		return errors.New("forward: address is required")
	}
	return nil
}

func (c Condition) validate() error {
	switch c.Field {
	case "all":
		return nil
	case "size":
		if (c.Op != "over" && c.Op != "under") || !sizeRe.MatchString(c.Value) {
			return errors.New("size condition needs over/under and a size like 100K")
		}
		return nil
	case "header":
		if strings.TrimSpace(c.Header) == "" || strings.ContainsAny(c.Header, ": \r\n") {
			return errors.New("header name is invalid")
		}
	case "from", "to", "cc", "to_or_cc", "subject", "body":
	default:
		return fmt.Errorf("unknown field %q", c.Field)
	}
	switch strings.TrimPrefix(c.Op, "not_") {
	case "contains", "is", "matches":
		if c.Value == "" {
			return errors.New("condition value is empty")
		}
	case "exists":
		if c.Field != "header" {
			return errors.New("exists works only with a header")
		}
	default:
		return fmt.Errorf("unknown operator %q", c.Op)
	}
	return nil
}

func (c Condition) sieve(req map[string]bool) string {
	if c.Field == "all" {
		return "true"
	}
	if c.Field == "size" {
		return fmt.Sprintf("size :%s %s", c.Op, strings.ToUpper(c.Value))
	}
	neg := strings.HasPrefix(c.Op, "not_")
	op := strings.TrimPrefix(c.Op, "not_")
	var t string
	switch {
	case op == "exists":
		t = "exists " + q(c.Header)
	case c.Field == "body":
		req["body"] = true
		t = fmt.Sprintf("body :text :%s %s", op, q(c.Value))
	default:
		var hs []string
		switch c.Field {
		case "header":
			hs = []string{c.Header}
		case "to_or_cc":
			hs = []string{"to", "cc"}
		default:
			hs = []string{c.Field}
		}
		hdr := q(hs[0])
		if len(hs) > 1 {
			hdr = qlist(hs)
		}
		t = fmt.Sprintf("header :%s %s %s", op, hdr, q(c.Value))
	}
	if neg {
		return "not " + t
	}
	return t
}

func (a Action) sieve(req map[string]bool) string {
	switch a.Type {
	case "fileinto":
		req["fileinto"] = true
		return "fileinto " + q(a.Value) + ";"
	case "copyto":
		req["fileinto"], req["copy"] = true, true
		return "fileinto :copy " + q(a.Value) + ";"
	case "redirect":
		return "redirect " + q(a.Value) + ";"
	case "redirect_copy":
		req["copy"] = true
		return "redirect :copy " + q(a.Value) + ";"
	case "reject":
		req["reject"] = true
		return "reject " + q(a.Value) + ";"
	case "seen":
		req["imap4flags"] = true
		return `addflag "\\Seen";`
	case "flag":
		req["imap4flags"] = true
		return `addflag "\\Flagged";`
	}
	return a.Type + ";" // keep, discard
}

func writeRule(b *strings.Builder, name string, enabled bool, test string, actions []string) {
	fmt.Fprintf(b, "# rule:[%s]\n", name)
	if !enabled {
		test = "allof (false, " + test + ")"
	}
	fmt.Fprintf(b, "if %s\n{\n", test)
	for _, a := range actions {
		b.WriteString("\t" + a + "\n")
	}
	b.WriteString("}\n")
}

// Script генерирует Sieve-скрипт. Порядок: автоответ, пересылка, правила.
func (rs *Ruleset) Script() string {
	req := map[string]bool{}
	var body strings.Builder

	if v := rs.Vacation; v.Enabled || strings.TrimSpace(v.Body) != "" {
		req["vacation"] = true
		var conds []string
		if v.Start != "" {
			req["date"], req["relational"] = true, true
			conds = append(conds, fmt.Sprintf(`currentdate :value "ge" "date" %s`, q(v.Start)))
		}
		if v.End != "" {
			req["date"], req["relational"] = true, true
			conds = append(conds, fmt.Sprintf(`currentdate :value "le" "date" %s`, q(v.End)))
		}
		test := "true"
		if len(conds) == 1 {
			test = conds[0]
		} else if len(conds) > 1 {
			test = "allof (" + strings.Join(conds, ", ") + ")"
		}
		cmd := fmt.Sprintf("vacation :days %d", v.Days)
		if v.Subject != "" {
			cmd += " :subject " + q(v.Subject)
		}
		if len(v.Addresses) > 0 {
			cmd += " :addresses " + qlist(v.Addresses)
		}
		cmd += " " + q(v.Body) + ";"
		writeRule(&body, vacationName, v.Enabled, test, []string{cmd})
	}

	if f := rs.Forward; f.To != "" {
		var acts []string
		if f.KeepCopy {
			req["copy"] = true
			acts = []string{"redirect :copy " + q(f.To) + ";"}
		} else {
			acts = []string{"redirect " + q(f.To) + ";", "stop;"}
		}
		writeRule(&body, forwardName, f.Enabled, "true", acts)
	}

	for _, r := range rs.Rules {
		var tests []string
		for _, c := range r.Conditions {
			tests = append(tests, c.sieve(req))
		}
		test := tests[0]
		if len(tests) > 1 {
			kw := "allof"
			if r.Match == "any" {
				kw = "anyof"
			}
			test = kw + " (" + strings.Join(tests, ", ") + ")"
		}
		var acts []string
		for _, a := range r.Actions {
			acts = append(acts, a.sieve(req))
		}
		if r.Stop {
			acts = append(acts, "stop;")
		}
		writeRule(&body, r.Name, r.Enabled, test, acts)
	}

	var out strings.Builder
	out.WriteString("# Фильтры почты. Правила в формате Roundcube managesieve (# rule:[...]).\n")
	if len(req) > 0 {
		names := make([]string, 0, len(req))
		for k := range req {
			names = append(names, k)
		}
		sort.Strings(names)
		out.WriteString("require " + qlist(names) + ";\n")
	}
	out.WriteString(body.String())
	return out.String()
}
