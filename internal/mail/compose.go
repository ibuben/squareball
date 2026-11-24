package mail

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
	"strings"
	"time"

	gomail "github.com/emersion/go-message/mail"
)

type Upload struct {
	Filename    string
	ContentType string
	Data        []byte
}

type Draft struct {
	From        Address
	ReplyTo     []Address
	To, Cc, Bcc []Address
	Subject     string
	Text        string
	HTML        string
	InReplyTo   string
	References  []string
	Attachments []Upload
	Hostname    string // для Message-ID
	KeepBcc     bool   // писать Bcc в заголовки — только для черновиков
	Priority    int    // 1 — высокая, 5 — низкая, 0 — обычная
	ReadReceipt bool   // Disposition-Notification-To: адрес отправителя
	DSN         bool   // уведомление о доставке (SMTP NOTIFY); в заголовки не пишется, но хранится в черновике
}

// Recipients — адреса конверта (To + Cc + Bcc) без повторов.
func (d *Draft) Recipients() []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]Address{d.To, d.Cc, d.Bcc} {
		for _, a := range list {
			k := strings.ToLower(a.Email)
			if a.Email != "" && !seen[k] {
				seen[k] = true
				out = append(out, a.Email)
			}
		}
	}
	return out
}

func toMail(list []Address) []*gomail.Address {
	out := make([]*gomail.Address, 0, len(list))
	for _, a := range list {
		out = append(out, &gomail.Address{Name: a.Name, Address: a.Email})
	}
	return out
}

var (
	reBlock = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/tr|/h[1-6])\s*/?>`)
	reTag   = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpace = regexp.MustCompile(`\n{3,}`)
)

// HTMLToText — простая текстовая версия для multipart/alternative.
func HTMLToText(s string) string {
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.TrimSpace(reSpace.ReplaceAllString(s, "\n\n"))
}

// Build собирает RFC 5322 сообщение. Bcc в заголовки попадает только при KeepBcc.
func Build(d *Draft) ([]byte, string, error) {
	if d.From.Email == "" {
		return nil, "", errors.New("from is required")
	}
	var h gomail.Header
	h.SetDate(time.Now())
	h.SetAddressList("From", toMail([]Address{d.From}))
	if len(d.ReplyTo) > 0 {
		h.SetAddressList("Reply-To", toMail(d.ReplyTo))
	}
	if len(d.To) > 0 {
		h.SetAddressList("To", toMail(d.To))
	}
	if len(d.Cc) > 0 {
		h.SetAddressList("Cc", toMail(d.Cc))
	}
	if d.KeepBcc && len(d.Bcc) > 0 {
		h.SetAddressList("Bcc", toMail(d.Bcc))
	}
	h.SetSubject(d.Subject)
	host := d.Hostname
	if host == "" {
		if _, dom, ok := strings.Cut(d.From.Email, "@"); ok {
			host = dom
		}
	}
	if err := h.GenerateMessageIDWithHostname(host); err != nil {
		return nil, "", err
	}
	msgID, _ := h.MessageID()
	if d.InReplyTo != "" {
		h.SetMsgIDList("In-Reply-To", []string{d.InReplyTo})
		refs := append(append([]string{}, d.References...), d.InReplyTo)
		h.SetMsgIDList("References", refs)
	}
	switch d.Priority {
	case 1:
		h.Set("X-Priority", "1 (Highest)")
		h.Set("Importance", "high")
	case 5:
		h.Set("X-Priority", "5 (Lowest)")
		h.Set("Importance", "low")
	}
	if d.ReadReceipt {
		h.SetAddressList("Disposition-Notification-To", toMail([]Address{d.From}))
	}
	if d.DSN && d.KeepBcc { // только черновик: чтобы галочка сохранилась при продолжении
		h.Set("X-Webmail-DSN", "1")
	}
	h.Set("User-Agent", "webmail")
	h.Set("MIME-Version", "1.0")

	text := d.Text
	if text == "" && d.HTML != "" {
		text = HTMLToText(d.HTML)
	}

	var buf bytes.Buffer
	writeBodies := func(iw *gomail.InlineWriter) error {
		var th gomail.InlineHeader
		th.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		w, err := iw.CreatePart(th)
		if err != nil {
			return err
		}
		io.WriteString(w, text)
		w.Close()
		if d.HTML != "" {
			var hh gomail.InlineHeader
			hh.SetContentType("text/html", map[string]string{"charset": "utf-8"})
			w, err := iw.CreatePart(hh)
			if err != nil {
				return err
			}
			io.WriteString(w, d.HTML)
			w.Close()
		}
		return iw.Close()
	}

	if len(d.Attachments) == 0 && d.HTML == "" {
		h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
		w, err := gomail.CreateSingleInlineWriter(&buf, h)
		if err != nil {
			return nil, "", err
		}
		io.WriteString(w, text)
		if err := w.Close(); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), msgID, nil
	}
	if len(d.Attachments) == 0 {
		iw, err := gomail.CreateInlineWriter(&buf, h)
		if err != nil {
			return nil, "", err
		}
		if err := writeBodies(iw); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), msgID, nil
	}

	mw, err := gomail.CreateWriter(&buf, h)
	if err != nil {
		return nil, "", err
	}
	iw, err := mw.CreateInline()
	if err != nil {
		return nil, "", err
	}
	if err := writeBodies(iw); err != nil {
		return nil, "", err
	}
	for _, a := range d.Attachments {
		var ah gomail.AttachmentHeader
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		ah.SetContentType(ct, nil)
		ah.SetFilename(a.Filename)
		w, err := mw.CreateAttachment(ah)
		if err != nil {
			return nil, "", err
		}
		if _, err := w.Write(a.Data); err != nil {
			return nil, "", err
		}
		w.Close()
	}
	if err := mw.Close(); err != nil {
		return nil, "", fmt.Errorf("build message: %w", err)
	}
	return buf.Bytes(), msgID, nil
}
