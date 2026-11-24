// Package sieve — клиент ManageSieve (RFC 5804) и модель правил фильтрации.
package sieve

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ibuben/squareball/internal/config"
)

// ServerError — ответ NO от сервера; Message обычно стоит показать пользователю
// (например, ошибку синтаксиса из CHECKSCRIPT).
type ServerError struct{ Message string }

func (e *ServerError) Error() string { return "managesieve: " + e.Message }

var ErrAuth = errors.New("managesieve: authentication failed")

type Script struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

type Client struct {
	conn net.Conn
	r    *bufio.Reader
	caps map[string]string
}

// Creds: обычный вход или impersonation (authzid = ящик, authcid = мастер).
type Creds struct {
	Username, Password       string
	MasterUser, MasterSecret string
}

func Dial(cfg *config.Endpoint, cr Creds) (*Client, error) {
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).Dial("tcp", cfg.Addr())
	if err != nil {
		return nil, fmt.Errorf("managesieve connect: %w", err)
	}
	c := &Client{conn: conn, r: bufio.NewReader(conn)}
	fail := func(err error) (*Client, error) { conn.Close(); return nil, err }
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := c.readCaps(); err != nil {
		return fail(err)
	}
	if cfg.Security == "starttls" {
		if _, ok := c.caps["STARTTLS"]; !ok {
			return fail(errors.New("managesieve: server does not offer STARTTLS"))
		}
		if _, err := c.cmd("STARTTLS"); err != nil {
			return fail(err)
		}
		tc := tls.Client(conn, &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: !cfg.TLSVerify, MinVersion: tls.VersionTLS12})
		if err := tc.Handshake(); err != nil {
			return fail(fmt.Errorf("managesieve tls: %w", err))
		}
		c.conn, c.r = tc, bufio.NewReader(tc)
		_ = c.conn.SetDeadline(time.Now().Add(30 * time.Second))
		if err := c.readCaps(); err != nil { // после STARTTLS сервер повторяет возможности
			return fail(err)
		}
	}
	authzid, authcid, pass := "", cr.Username, cr.Password
	if cr.MasterUser != "" {
		authzid, authcid, pass = cr.Username, cr.MasterUser, cr.MasterSecret
	}
	ir := base64.StdEncoding.EncodeToString([]byte(authzid + "\x00" + authcid + "\x00" + pass))
	if _, err := c.cmd(`AUTHENTICATE "PLAIN" ` + quote(ir)); err != nil {
		var se *ServerError
		if errors.As(err, &se) {
			return fail(ErrAuth)
		}
		return fail(err)
	}
	return c, nil
}

// Extensions — список расширений Sieve, объявленных сервером.
func (c *Client) Extensions() map[string]bool {
	out := map[string]bool{}
	for _, e := range strings.Fields(c.caps["SIEVE"]) {
		out[strings.ToLower(e)] = true
	}
	return out
}

func (c *Client) Close() error {
	_ = c.conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = c.cmd("LOGOUT")
	return c.conn.Close()
}

func (c *Client) ListScripts() ([]Script, error) {
	lines, err := c.cmd("LISTSCRIPTS")
	if err != nil {
		return nil, err
	}
	var out []Script
	for _, l := range lines {
		if len(l) == 0 {
			continue
		}
		s := Script{Name: l[0]}
		if len(l) > 1 && strings.EqualFold(l[1], "ACTIVE") {
			s.Active = true
		}
		out = append(out, s)
	}
	return out, nil
}

func (c *Client) GetScript(name string) (string, error) {
	lines, err := c.cmd("GETSCRIPT " + quote(name))
	if err != nil {
		return "", err
	}
	if len(lines) == 0 || len(lines[0]) == 0 {
		return "", nil
	}
	return lines[0][0], nil
}

func (c *Client) CheckScript(script string) error {
	_, err := c.cmd("CHECKSCRIPT " + literal(script))
	return err
}

func (c *Client) PutScript(name, script string) error {
	_, err := c.cmd("PUTSCRIPT " + quote(name) + " " + literal(script))
	return err
}

func (c *Client) SetActive(name string) error {
	_, err := c.cmd("SETACTIVE " + quote(name))
	return err
}

// --- протокол ----------------------------------------------------------------

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// literal — неблокирующий литерал {n+}: сервер не ждёт подтверждения.
func literal(s string) string {
	return "{" + strconv.Itoa(len(s)) + "+}\r\n" + s
}

func (c *Client) readCaps() error {
	lines, err := c.readResponse()
	if err != nil {
		return err
	}
	c.caps = map[string]string{}
	for _, l := range lines {
		if len(l) == 0 {
			continue
		}
		v := ""
		if len(l) > 1 {
			v = l[1]
		}
		c.caps[strings.ToUpper(l[0])] = v
	}
	return nil
}

func (c *Client) cmd(line string) ([][]string, error) {
	_ = c.conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.WriteString(c.conn, line+"\r\n"); err != nil {
		return nil, err
	}
	return c.readResponse()
}

// readResponse читает строки данных до OK/NO/BYE.
func (c *Client) readResponse() ([][]string, error) {
	var data [][]string
	for {
		toks, err := c.readTokens()
		if err != nil {
			return nil, err
		}
		if len(toks) > 0 {
			switch strings.ToUpper(toks[0]) {
			case "OK":
				return data, nil
			case "NO", "BYE":
				return nil, &ServerError{Message: responseText(toks[1:])}
			}
		}
		data = append(data, toks)
	}
}

// responseText — человекочитаемый текст после кода ответа "(CODE)".
func responseText(toks []string) string {
	for _, t := range toks {
		if !strings.HasPrefix(t, "(") {
			return strings.TrimSpace(t)
		}
	}
	return strings.Join(toks, " ")
}

// readTokens разбирает одну логическую строку: атомы, "строки", литералы {n}
// (содержимое литерала может занимать несколько физических строк), коды (...).
func (c *Client) readTokens() ([]string, error) {
	var toks []string
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		i := 0
		for i < len(line) {
			switch ch := line[i]; {
			case ch == ' ':
				i++
			case ch == '"':
				var b strings.Builder
				i++
				for i < len(line) && line[i] != '"' {
					if line[i] == '\\' && i+1 < len(line) {
						i++
					}
					b.WriteByte(line[i])
					i++
				}
				i++
				toks = append(toks, b.String())
			case ch == '(':
				j := strings.IndexByte(line[i:], ')')
				if j < 0 {
					j = len(line) - i - 1
				}
				toks = append(toks, line[i:i+j+1])
				i += j + 1
			case ch == '{' && strings.HasSuffix(line, "}"):
				n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(line[i+1:], "}"), "+"))
				if err != nil || n < 0 || n > 1<<20 {
					return nil, fmt.Errorf("managesieve: bad literal %q", line[i:])
				}
				buf := make([]byte, n)
				if _, err := io.ReadFull(c.r, buf); err != nil {
					return nil, err
				}
				toks = append(toks, string(buf))
				i = len(line)
				goto next // остаток строки после литерала читаем следующей итерацией
			default:
				j := strings.IndexByte(line[i:], ' ')
				if j < 0 {
					j = len(line) - i
				}
				toks = append(toks, line[i:i+j])
				i += j
			}
		}
		return toks, nil
	next:
		// После литерала логическая строка продолжается до CRLF (обычно пустого).
		rest, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if rest = strings.TrimRight(rest, "\r\n"); rest == "" {
			return toks, nil
		}
		more, err := tokenizeRest(rest)
		if err != nil {
			return nil, err
		}
		return append(toks, more...), nil
	}
}

func tokenizeRest(s string) ([]string, error) {
	c := &Client{r: bufio.NewReader(strings.NewReader(s + "\r\n"))}
	return c.readTokens()
}
