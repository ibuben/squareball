package api

import (
	"crypto/rand"
	"encoding/hex"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/ibuben/squareball/internal/mail"
	"github.com/ibuben/squareball/internal/sanitize"
)

// Картинки в тексте письма приходят двумя путями:
//   - новые — файлами формы "inline:<метка>", в HTML на них ссылается src="cid:<метка>";
//   - уже лежащие в почте (цитата письма, продолжаемый черновик) — ссылками
//     src="api/v1/part?folder=…&uid=…&index=…" (относительной, от корня или
//     полным адресом — как её сохранил редактор): их забираем из ящика.
// Все они становятся частями multipart/related с уникальными Content-ID,
// а картинки, которых в итоговом HTML нет, в письмо не попадают.

var (
	reInlineToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	reProxySrc    = regexp.MustCompile(`(?i)(<img\b[^>]*?\ssrc\s*=\s*)(["'])((?:[^"'\s]*/)?api/v1/img\?[^"']*)(["'])`)
	rePartSrc     = regexp.MustCompile(`(?i)(<img\b[^>]*?\ssrc\s*=\s*)(["'])((?:[^"'\s]*/)?api/v1/part\?[^"']*)(["'])`)
)

// maxInlineImages — защита от письма из тысячи картинок.
const maxInlineImages = 100

func inlineImageType(name string, data []byte) string {
	ct := http.DetectContentType(data)
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return ct
	}
	if strings.EqualFold(path.Ext(name), ".svg") {
		return "" // SVG может содержать скрипты — только вложением
	}
	return ""
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) inlineImages(w http.ResponseWriter, r *http.Request, src string) (string, []mail.Upload, int64, bool) {
	sess := sessionFrom(r.Context())
	_, domain, _ := strings.Cut(sess.Mailbox, "@")
	if domain == "" {
		domain = "webmail"
	}
	suffix := "." + randHex(6) + "@" + domain
	byToken := map[string]mail.Upload{}

	if r.MultipartForm != nil {
		for key, fhs := range r.MultipartForm.File {
			token, ok := strings.CutPrefix(key, "inline:")
			if !ok || !reInlineToken.MatchString(token) || len(fhs) != 1 {
				continue
			}
			f, err := fhs[0].Open()
			if err != nil {
				writeErr(w, http.StatusBadRequest, "invalid", "")
				return "", nil, 0, false
			}
			data, _ := io.ReadAll(f)
			f.Close()
			ct := inlineImageType(fhs[0].Filename, data)
			if ct == "" {
				writeErr(w, http.StatusBadRequest, "send.bad_image", fhs[0].Filename)
				return "", nil, 0, false
			}
			byToken[token] = mail.Upload{Filename: fhs[0].Filename, ContentType: ct, Data: data, ContentID: token + suffix}
		}
	}

	// Картинки цитаты, показанные через прокси, уходят с исходными адресами.
	src = reProxySrc.ReplaceAllStringFunc(src, func(m string) string {
		g := reProxySrc.FindStringSubmatch(m)
		orig, ok := s.unproxyImageURL(html.UnescapeString(g[3]))
		if !ok {
			return m
		}
		return g[1] + g[2] + html.EscapeString(orig) + g[4]
	})

	// Ссылки на части писем из ящика → новые метки.
	type ref struct {
		folder string
		uid    uint32
		index  int
	}
	refs := map[ref]string{}
	var order []ref
	src = rePartSrc.ReplaceAllStringFunc(src, func(m string) string {
		g := rePartSrc.FindStringSubmatch(m)
		u, err := url.Parse(html.UnescapeString(g[3]))
		if err != nil {
			return m
		}
		q := u.Query()
		uid, err1 := strconv.ParseUint(q.Get("uid"), 10, 32)
		index, err2 := strconv.Atoi(q.Get("index"))
		if err1 != nil || err2 != nil || q.Get("folder") == "" {
			return m
		}
		k := ref{q.Get("folder"), uint32(uid), index}
		token, ok := refs[k]
		if !ok {
			if len(refs) >= maxInlineImages {
				return m
			}
			token = "p" + strconv.Itoa(len(refs)+1)
			refs[k] = token
			order = append(order, k)
		}
		return g[1] + g[2] + "cid:" + token + g[4]
	})
	if len(order) > 0 {
		if !s.withIMAP(w, r, func(c *imapclient.Client) error {
			for _, k := range order {
				att, data, err := mail.GetPart(c, k.folder, k.uid, k.index)
				if err != nil {
					continue // картинку удалили — письмо всё равно уходит
				}
				ct := inlineImageType(att.Filename, data)
				if ct == "" {
					continue
				}
				name := att.Filename
				if mt, _, _ := mime.ParseMediaType(att.ContentType); name == "" && mt != "" {
					name = "image"
				}
				token := refs[k]
				byToken[token] = mail.Upload{Filename: name, ContentType: ct, Data: data, ContentID: token + suffix}
			}
			return nil
		}) {
			return "", nil, 0, false
		}
	}

	used := map[string]bool{}
	clean := sanitize.Email(src, func(cid string) string {
		if up, ok := byToken[cid]; ok {
			used[cid] = true
			return "cid:" + up.ContentID
		}
		return ""
	}, sanitize.KeepRemote).HTML

	var out []mail.Upload
	var size int64
	for token, up := range byToken {
		if used[token] && len(out) < maxInlineImages {
			out = append(out, up)
			size += int64(len(up.Data))
		}
	}
	return clean, out, size, true
}
