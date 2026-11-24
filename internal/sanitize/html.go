// Package sanitize очищает HTML входящих писем.
//
// Первый слой — bluemonday (белый список тегов, атрибутов и CSS-свойств),
// второй — переписывание картинок: cid: → ссылка на часть письма, внешние
// картинки убираются, пока пользователь их не разрешит. Третий слой —
// на фронтенде: письмо показывается в iframe sandbox без скриптов и с CSP.
package sanitize

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

var policy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowElements("center", "font", "span", "div", "section", "header", "footer", "hr")
	p.AllowAttrs("align", "valign", "bgcolor", "width", "height", "border",
		"cellpadding", "cellspacing", "colspan", "rowspan", "dir").Globally()
	p.AllowAttrs("color", "face", "size").OnElements("font")
	// Маркер цитаты исходного письма в черновиках и отправленных (см. ComposeWindow).
	p.AllowAttrs("data-wm-quote", "data-wm-signature").Matching(regexp.MustCompile(`^1$`)).OnElements("div")
	p.AllowAttrs("type").Matching(regexp.MustCompile(`^cite$`)).OnElements("blockquote")
	p.AllowStyles(
		"color", "background-color", "font-family", "font-size", "font-weight", "font-style",
		"text-align", "text-decoration", "text-transform", "line-height", "letter-spacing",
		"vertical-align", "white-space", "word-break", "direction",
		"margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
		"padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
		"border", "border-top", "border-right", "border-bottom", "border-left",
		"border-color", "border-style", "border-width", "border-collapse", "border-radius",
		"width", "max-width", "min-width", "height", "max-height", "display", "float", "clear",
	).Globally()
	p.AllowURLSchemes("http", "https", "mailto", "cid", "data")
	p.AllowDataURIImages()
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	return p
}()

// KeepRemote оставляет адреса внешних картинок как есть (подписи и шаблоны:
// получателю нужны настоящие адреса).
func KeepRemote(u string) string { return u }

type Result struct {
	HTML          string `json:"html"`
	BlockedImages int    `json:"blocked_images"`
}

// Email санитизирует HTML письма. cidURL переводит Content-ID в URL части
// (пустая строка — картинку убрать). remote решает судьбу внешних картинок:
// nil — заблокировать (data-blocked-src), иначе — заменить адрес на то, что
// вернёт функция (например, ссылку на прокси картинок).
func Email(src string, cidURL func(cid string) string, remote func(u string) string) Result {
	clean := policy.Sanitize(src)
	doc, err := html.Parse(strings.NewReader(clean))
	if err != nil {
		return Result{HTML: clean}
	}
	blocked := 0
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "img" {
			for i, a := range n.Attr {
				if a.Key != "src" {
					continue
				}
				v := strings.TrimSpace(a.Val)
				lower := strings.ToLower(v)
				switch {
				case strings.HasPrefix(lower, "cid:"):
					n.Attr[i].Val = cidURL(strings.Trim(v[4:], "<>"))
				case strings.HasPrefix(lower, "data:image/"):
				case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "//"):
					if strings.HasPrefix(v, "//") {
						v = "https:" + v
					}
					if remote == nil {
						n.Attr[i].Key = "data-blocked-src"
						n.Attr[i].Val = v
						blocked++
					} else {
						n.Attr[i].Val = remote(v)
					}
				default:
					n.Attr[i].Val = ""
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	var buf bytes.Buffer
	body := findBody(doc)
	if body == nil {
		body = doc
	}
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		_ = html.Render(&buf, c)
	}
	return Result{HTML: buf.String(), BlockedImages: blocked}
}

func findBody(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && n.Data == "body" {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if b := findBody(c); b != nil {
			return b
		}
	}
	return nil
}

// Text превращает текстовое письмо в безопасный HTML со ссылками.
func Text(s string) string {
	var b strings.Builder
	b.WriteString(`<div style="white-space:pre-wrap;word-break:break-word;font-family:inherit">`)
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteString("\n")
		}
		quoted := strings.HasPrefix(line, ">")
		if quoted {
			b.WriteString(`<span style="color:#6b7280">`)
		}
		b.WriteString(linkify(html.EscapeString(line)))
		if quoted {
			b.WriteString(`</span>`)
		}
	}
	b.WriteString(`</div>`)
	return b.String()
}

func linkify(escaped string) string {
	var out strings.Builder
	for {
		i := strings.Index(escaped, "http")
		if i < 0 {
			out.WriteString(escaped)
			return out.String()
		}
		rest := escaped[i:]
		if !strings.HasPrefix(rest, "http://") && !strings.HasPrefix(rest, "https://") {
			out.WriteString(escaped[:i+4])
			escaped = escaped[i+4:]
			continue
		}
		end := strings.IndexAny(rest, " \t<>\"")
		if end < 0 {
			end = len(rest)
		}
		url := strings.TrimRight(rest[:end], ".,;:!?)")
		out.WriteString(escaped[:i])
		out.WriteString(`<a href="` + url + `" target="_blank" rel="noopener noreferrer">` + url + `</a>`)
		escaped = rest[len(url):]
	}
}
