package mail

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
	"github.com/ibuben/squareball/internal/locale"
)

// FlagMDNSent — уведомление о прочтении уже отправлено или отклонено (RFC 3503).
const FlagMDNSent = "$MDNSent"

// prioritySection — только заголовки важности: дёшево для списка писем.
var prioritySection = &imap.FetchItemBodySection{
	Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"X-Priority", "Importance"}, Peek: true,
}

// ParsePriority сводит X-Priority и Importance к 1 (высокая), 5 (низкая) или 0.
func ParsePriority(xPriority, importance string) int {
	if xp := strings.TrimSpace(xPriority); xp != "" {
		if n, err := strconv.Atoi(xp[:1]); err == nil {
			switch {
			case n <= 2:
				return 1
			case n >= 4:
				return 5
			}
			return 0
		}
	}
	switch strings.ToLower(strings.TrimSpace(importance)) {
	case "high":
		return 1
	case "low":
		return 5
	}
	return 0
}

func priorityFromHeader(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	h, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(raw, '\r', '\n')))).ReadMIMEHeader()
	if err != nil && len(h) == 0 {
		return 0
	}
	return ParsePriority(h.Get("X-Priority"), h.Get("Importance"))
}

// MDN — уведомление о прочтении (RFC 8098) на письмо orig.
type MDN struct {
	From      Address // ящик, который прочитал письмо
	To        string  // адрес из Disposition-Notification-To
	Subject   string  // тема исходного письма
	MessageID string
	Date      time.Time // дата исходного письма
	Automatic bool
	Hostname  string
	Lang      string // язык текста: код языка (internal/locale)
}

// BuildMDN собирает multipart/report; report-type=disposition-notification.
func BuildMDN(n MDN) ([]byte, error) {
	tx := func(k string) string { return locale.S(n.Lang, "mdn."+k) }
	var h message.Header
	mh := gomail.Header{Header: h}
	mh.SetDate(time.Now())
	mh.SetAddressList("From", toMail([]Address{n.From}))
	mh.SetAddressList("To", []*gomail.Address{{Address: n.To}})
	mh.SetSubject(tx("subject") + n.Subject)
	host := n.Hostname
	if host == "" {
		_, host, _ = strings.Cut(n.From.Email, "@")
	}
	if err := mh.GenerateMessageIDWithHostname(host); err != nil {
		return nil, err
	}
	if n.MessageID != "" {
		mh.SetMsgIDList("References", []string{n.MessageID})
	}
	mh.Set("MIME-Version", "1.0")
	if n.Automatic {
		mh.Set("Auto-Submitted", "auto-replied")
	}
	mh.SetContentType("multipart/report", map[string]string{"report-type": "disposition-notification"})

	var buf bytes.Buffer
	w, err := message.CreateWriter(&buf, mh.Header)
	if err != nil {
		return nil, err
	}
	var th message.Header
	th.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
	th.Set("Content-Transfer-Encoding", "8bit")
	pw, err := w.CreatePart(th)
	if err != nil {
		return nil, err
	}
	when := ""
	if !n.Date.IsZero() {
		when = tx("sent_on") + n.Date.Format(tx("date_format"))
	}
	fmt.Fprintf(pw, tx("body"), when, n.From.Email, n.Subject, time.Now().Format(tx("now_format")))
	pw.Close()

	var rh message.Header
	rh.SetContentType("message/disposition-notification", nil)
	rh.Set("Content-Transfer-Encoding", "7bit")
	rw, err := w.CreatePart(rh)
	if err != nil {
		return nil, err
	}
	mode := "manual-action/MDN-sent-manually"
	if n.Automatic {
		mode = "automatic-action/MDN-sent-automatically"
	}
	io.WriteString(rw, "Reporting-UA: "+host+"; webmail\r\n")
	io.WriteString(rw, "Final-Recipient: rfc822;"+n.From.Email+"\r\n")
	if n.MessageID != "" {
		io.WriteString(rw, "Original-Message-ID: <"+n.MessageID+">\r\n")
	}
	io.WriteString(rw, "Disposition: "+mode+"; displayed\r\n")
	rw.Close()
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
