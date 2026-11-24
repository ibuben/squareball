package sieve

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func sample() *Ruleset {
	return &Ruleset{
		Rules: []Rule{
			{Name: "Работа", Enabled: true, Match: "any", Stop: true,
				Conditions: []Condition{
					{Field: "from", Op: "contains", Value: "boss@example.com"},
					{Field: "subject", Op: "matches", Value: `*"срочно"*`},
				},
				Actions: []Action{{Type: "flag"}, {Type: "copyto", Value: "Работа/Важное"}}},
			{Name: "Большие", Enabled: false, Match: "all",
				Conditions: []Condition{{Field: "size", Op: "over", Value: "5M"}, {Field: "header", Header: "X-Spam", Op: "not_exists"}},
				Actions:    []Action{{Type: "fileinto", Value: "Big"}}},
			{Name: "Рассылки", Enabled: true, Match: "all",
				Conditions: []Condition{{Field: "header", Header: "List-Id", Op: "exists"}, {Field: "to_or_cc", Op: "not_contains", Value: "me@x"}},
				Actions:    []Action{{Type: "seen"}, {Type: "fileinto", Value: "Lists"}}},
			{Name: "Мусор", Enabled: true, Match: "all",
				Conditions: []Condition{{Field: "body", Op: "contains", Value: "unsubscribe\\now"}},
				Actions:    []Action{{Type: "discard"}}},
		},
		Vacation: Vacation{Enabled: true, Subject: "В отпуске", Body: "Вернусь 10 декабря.\nСрочное — коллегам.", Days: 3,
			Addresses: []string{"alias@x"}, Start: "2026-12-01", End: "2026-12-10"},
		Forward: Forward{Enabled: true, To: "backup@example.com", KeepCopy: true},
	}
}

func TestRoundTrip(t *testing.T) {
	in := sample()
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	script := in.Script("ru")
	out, err := FromScript(script)
	if err != nil {
		t.Fatalf("%v\n%s", err, script)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip differs\nin:  %+v\nout: %+v\nscript:\n%s", in, out, script)
	}
	for _, want := range []string{`require ["body", "copy", "date", "fileinto", "imap4flags", "relational", "vacation"];`,
		"# rule:[Большие]\nif allof (false, allof (size :over 5M, not exists \"X-Spam\"))",
		`header :matches "subject" "*\"срочно\"*"`, `body :text :contains "unsubscribe\\now"`} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
}

func TestForwardWithoutCopyStops(t *testing.T) {
	rs := &Ruleset{Forward: Forward{Enabled: true, To: "a@b"}}
	s := rs.Script("ru")
	if !strings.Contains(s, "redirect \"a@b\";\n\tstop;") {
		t.Fatalf("no stop after redirect:\n%s", s)
	}
	out, err := FromScript(s)
	if err != nil || out.Forward != rs.Forward {
		t.Fatalf("%+v %v", out, err)
	}
}

// Скрипт в том виде, как его пишет Roundcube managesieve.
const roundcube = `require ["fileinto","imap4flags","copy"];
# rule:[spam]
if header :contains "subject" "viagra"
{
	fileinto "Junk";
}
# rule:[boss]
if anyof (address :all :is "from" "boss@x.com", header :comparator "i;ascii-casemap" :contains "subject" "urgent")
{
	addflag "\\Flagged";
	fileinto :copy "Work";
	stop;
}
# rule:[off]
if false # header :contains "from" "noise@x.com"
{
	discard;
	stop;
}
`

func TestRoundcubeScript(t *testing.T) {
	rs, err := FromScript(roundcube)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rules) != 3 {
		t.Fatalf("rules: %+v", rs.Rules)
	}
	boss := rs.Rules[1]
	if boss.Name != "boss" || boss.Match != "any" || !boss.Stop || len(boss.Conditions) != 2 ||
		boss.Conditions[0] != (Condition{Field: "from", Op: "is", Value: "boss@x.com"}) ||
		boss.Actions[1] != (Action{Type: "copyto", Value: "Work"}) {
		t.Fatalf("boss: %+v", boss)
	}
	off := rs.Rules[2]
	if off.Enabled || off.Conditions[0].Value != "noise@x.com" {
		t.Fatalf("disabled Roundcube rule: %+v", off)
	}
}

func TestUnsupportedGoesRaw(t *testing.T) {
	for _, src := range []string{
		`require "regex"; if header :regex "subject" "^x" { discard; }`,
		`if header :contains "subject" "x" { fileinto "A"; } elsif true { keep; }`,
		`fileinto "INBOX";`,
		`if header :contains "subject" ["a", "b"] { discard; }`,
		`if header :contains "subject" "x" { if true { stop; } }`,
		`if header :contains "subject" "x" { discard`,
	} {
		if _, err := FromScript(src); !errors.Is(err, ErrUnsupported) {
			t.Errorf("want ErrUnsupported for %q, got %v", src, err)
		}
	}
}

func TestLexerEdges(t *testing.T) {
	src := "/* многострочный\nкомментарий */\nif size :over 1M { reject text:\nстрока 1\n..с точкой\n.\n; }\n"
	rs, err := FromScript(src)
	if err != nil {
		t.Fatal(err)
	}
	a := rs.Rules[0].Actions[0]
	if a.Type != "reject" || a.Value != "строка 1\n.с точкой" {
		t.Fatalf("multiline: %+v", a)
	}
}

func TestValidate(t *testing.T) {
	bad := []Ruleset{
		{Rules: []Rule{{Name: "x]", Conditions: []Condition{{Field: "all"}}, Actions: []Action{{Type: "discard"}}}}},
		{Rules: []Rule{{Name: "x", Conditions: []Condition{{Field: "subject", Op: "contains"}}, Actions: []Action{{Type: "discard"}}}}},
		{Rules: []Rule{{Name: "x", Conditions: []Condition{{Field: "size", Op: "over", Value: "lots"}}, Actions: []Action{{Type: "discard"}}}}},
		{Rules: []Rule{{Name: "x", Conditions: []Condition{{Field: "all"}}, Actions: []Action{{Type: "fileinto"}}}}},
		{Vacation: Vacation{Enabled: true, Days: 1}},
		{Forward: Forward{Enabled: true, To: "nobody"}},
	}
	for i, rs := range bad {
		if err := rs.Validate(); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
