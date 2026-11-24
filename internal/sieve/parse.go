package sieve

import (
	"fmt"
	"strconv"
	"strings"
)

// Синтаксический разбор Sieve (RFC 5228, раздел 8): токены → дерево команд.
// Семантику не проверяем — это делает сервер (CHECKSCRIPT); здесь важно
// получить дерево, чтобы показать правила формой, и не потерять ничего.

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tTag
	tString
	tNumber
	tComment
	tPunct // [ ] ( ) { } ; ,
)

type token struct {
	kind tokKind
	text string // для строк — уже раскодированное значение
	num  int64
	line int
}

type lexer struct {
	src  string
	pos  int
	line int
}

func lex(src string) ([]token, error) {
	l := &lexer{src: src, line: 1}
	var out []token
	for {
		t, err := l.next()
		if err != nil {
			return nil, err
		}
		out = append(out, t)
		if t.kind == tEOF {
			return out, nil
		}
	}
}

func (l *lexer) errf(f string, a ...any) error {
	return fmt.Errorf("sieve line %d: %s", l.line, fmt.Sprintf(f, a...))
}

func (l *lexer) next() (token, error) {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '\n' {
			l.line++
			l.pos++
		} else if c == ' ' || c == '\t' || c == '\r' {
			l.pos++
		} else {
			break
		}
	}
	if l.pos >= len(l.src) {
		return token{kind: tEOF, line: l.line}, nil
	}
	start, line := l.pos, l.line
	c := l.src[l.pos]
	switch {
	case c == '#':
		end := strings.IndexByte(l.src[l.pos:], '\n')
		if end < 0 {
			end = len(l.src) - l.pos
		}
		text := strings.TrimRight(l.src[l.pos+1:l.pos+end], "\r")
		l.pos += end
		return token{kind: tComment, text: text, line: line}, nil
	case c == '/' && strings.HasPrefix(l.src[l.pos:], "/*"):
		end := strings.Index(l.src[l.pos+2:], "*/")
		if end < 0 {
			return token{}, l.errf("unterminated comment")
		}
		text := l.src[l.pos+2 : l.pos+2+end]
		l.line += strings.Count(text, "\n")
		l.pos += end + 4
		return token{kind: tComment, text: text, line: line}, nil
	case c == '"':
		var b strings.Builder
		l.pos++
		for {
			if l.pos >= len(l.src) {
				return token{}, l.errf("unterminated string")
			}
			ch := l.src[l.pos]
			if ch == '"' {
				l.pos++
				break
			}
			if ch == '\\' && l.pos+1 < len(l.src) {
				l.pos++
				ch = l.src[l.pos]
			}
			if ch == '\n' {
				l.line++
			}
			b.WriteByte(ch)
			l.pos++
		}
		return token{kind: tString, text: b.String(), line: line}, nil
	case c == ':':
		l.pos++
		w := l.word()
		if w == "" {
			return token{}, l.errf("empty tag")
		}
		return token{kind: tTag, text: strings.ToLower(w), line: line}, nil
	case c >= '0' && c <= '9':
		for l.pos < len(l.src) && l.src[l.pos] >= '0' && l.src[l.pos] <= '9' {
			l.pos++
		}
		n, _ := strconv.ParseInt(l.src[start:l.pos], 10, 64)
		if l.pos < len(l.src) {
			switch l.src[l.pos] {
			case 'K', 'k':
				n <<= 10
				l.pos++
			case 'M', 'm':
				n <<= 20
				l.pos++
			case 'G', 'g':
				n <<= 30
				l.pos++
			}
		}
		return token{kind: tNumber, num: n, text: l.src[start:l.pos], line: line}, nil
	case strings.ContainsRune("[](){};,", rune(c)):
		l.pos++
		return token{kind: tPunct, text: string(c), line: line}, nil
	}
	w := l.word()
	if w == "" {
		return token{}, l.errf("unexpected character %q", c)
	}
	if strings.EqualFold(w, "text") && l.pos < len(l.src) && l.src[l.pos] == ':' {
		return l.multiline(line)
	}
	return token{kind: tIdent, text: strings.ToLower(w), line: line}, nil
}

func (l *lexer) word() string {
	start := l.pos
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			l.pos++
			continue
		}
		break
	}
	return l.src[start:l.pos]
}

// multiline разбирает text: ... CRLF.CRLF с «точечной набивкой».
func (l *lexer) multiline(line int) (token, error) {
	l.pos++ // ':'
	nl := strings.IndexByte(l.src[l.pos:], '\n')
	if nl < 0 {
		return token{}, l.errf("bad multi-line string")
	}
	l.pos += nl + 1
	l.line++
	var lines []string
	for {
		nl := strings.IndexByte(l.src[l.pos:], '\n')
		if nl < 0 {
			return token{}, l.errf("unterminated multi-line string")
		}
		s := strings.TrimRight(l.src[l.pos:l.pos+nl], "\r")
		l.pos += nl + 1
		l.line++
		if s == "." {
			break
		}
		lines = append(lines, strings.TrimPrefix(s, "."))
	}
	return token{kind: tString, text: strings.Join(lines, "\n"), line: line}, nil
}

// --- дерево ----------------------------------------------------------------------

type ArgKind int

const (
	ArgTag ArgKind = iota
	ArgString
	ArgStrings
	ArgNumber
)

type Arg struct {
	Kind ArgKind
	Tag  string
	Strs []string // для ArgString — один элемент
	Num  int64
}

type Test struct {
	Name  string
	Args  []Arg
	Tests []Test // anyof/allof/not
	// Trailing — комментарий в той же строке после теста (формат Roundcube
	// для отключённых правил: "if false # allof (...)").
	Trailing string
}

type Command struct {
	Name     string
	Comments []string // комментарии непосредственно перед командой
	Args     []Arg
	Tests    []Test
	Block    []Command
	HasBlock bool
}

type parser struct {
	toks []token
	pos  int
}

// Parse разбирает скрипт в последовательность команд верхнего уровня.
func Parse(src string) ([]Command, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	cmds, err := p.commands()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, p.errf("unexpected %q", p.peek().text)
	}
	return cmds, nil
}

func (p *parser) peek() token {
	for p.toks[p.pos].kind == tComment {
		p.pos++
	}
	return p.toks[p.pos]
}

func (p *parser) take() token { t := p.peek(); p.pos++; return t }

func (p *parser) errf(f string, a ...any) error {
	return fmt.Errorf("sieve line %d: %s", p.toks[p.pos].line, fmt.Sprintf(f, a...))
}

func (p *parser) punct(s string) bool {
	t := p.peek()
	if t.kind == tPunct && t.text == s {
		p.pos++
		return true
	}
	return false
}

// leadingComments собирает комментарии перед следующей значимой лексемой.
func (p *parser) leadingComments() []string {
	var out []string
	for p.toks[p.pos].kind == tComment {
		out = append(out, p.toks[p.pos].text)
		p.pos++
	}
	return out
}

func (p *parser) commands() ([]Command, error) {
	var out []Command
	for {
		comments := p.leadingComments()
		t := p.toks[p.pos]
		if t.kind == tEOF || (t.kind == tPunct && t.text == "}") {
			return out, nil
		}
		if t.kind != tIdent {
			return nil, p.errf("expected command, got %q", t.text)
		}
		p.pos++
		cmd := Command{Name: t.text, Comments: comments}
		args, tests, err := p.arguments(t.line)
		if err != nil {
			return nil, err
		}
		cmd.Args, cmd.Tests = args, tests
		switch {
		case p.punct(";"):
		case p.punct("{"):
			cmd.HasBlock = true
			if cmd.Block, err = p.commands(); err != nil {
				return nil, err
			}
			if !p.punct("}") {
				return nil, p.errf("expected }")
			}
		default:
			return nil, p.errf("expected ; or { after %s", cmd.Name)
		}
		out = append(out, cmd)
	}
}

// arguments: argument* [test | "(" test-list ")"]
func (p *parser) arguments(line int) ([]Arg, []Test, error) {
	var args []Arg
	for {
		t := p.peek()
		switch {
		case t.kind == tTag:
			p.pos++
			args = append(args, Arg{Kind: ArgTag, Tag: t.text})
			continue
		case t.kind == tString:
			p.pos++
			args = append(args, Arg{Kind: ArgString, Strs: []string{t.text}})
			continue
		case t.kind == tNumber:
			p.pos++
			args = append(args, Arg{Kind: ArgNumber, Num: t.num})
			continue
		case t.kind == tPunct && t.text == "[":
			p.pos++
			var list []string
			for {
				s := p.take()
				if s.kind != tString {
					return nil, nil, p.errf("expected string in list")
				}
				list = append(list, s.text)
				if p.punct("]") {
					break
				}
				if !p.punct(",") {
					return nil, nil, p.errf("expected , or ]")
				}
			}
			args = append(args, Arg{Kind: ArgStrings, Strs: list})
			continue
		}
		break
	}
	t := p.peek()
	if t.kind == tPunct && t.text == "(" {
		p.pos++
		var tests []Test
		for {
			tt, err := p.test()
			if err != nil {
				return nil, nil, err
			}
			tests = append(tests, tt)
			if p.punct(")") {
				return args, tests, nil
			}
			if !p.punct(",") {
				return nil, nil, p.errf("expected , or )")
			}
		}
	}
	if t.kind == tIdent {
		tt, err := p.test()
		if err != nil {
			return nil, nil, err
		}
		return args, []Test{tt}, nil
	}
	return args, nil, nil
}

func (p *parser) test() (Test, error) {
	t := p.take()
	if t.kind != tIdent {
		return Test{}, p.errf("expected test, got %q", t.text)
	}
	args, tests, err := p.arguments(t.line)
	if err != nil {
		return Test{}, err
	}
	tt := Test{Name: t.text, Args: args, Tests: tests}
	// Комментарий в той же строке сразу после теста. peek() при заглядывании
	// вперёд уже мог перескочить его, поэтому ищем и назад, и вперёд.
	for i := p.pos - 1; i >= 0 && p.toks[i].kind == tComment; i-- {
		if p.toks[i].line == t.line {
			tt.Trailing = p.toks[i].text
		}
	}
	if c := p.toks[p.pos]; tt.Trailing == "" && c.kind == tComment && c.line == t.line {
		tt.Trailing = c.text
	}
	return tt, nil
}
