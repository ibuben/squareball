package mail

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // KOI8-R, CP1251 и прочие кодировки
	gomail "github.com/emersion/go-message/mail"
)

var (
	ErrNotFound = errors.New("message not found")
	ErrNoDrafts = errors.New("drafts folder not found")
)

type Address struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

type Summary struct {
	UID            uint32    `json:"uid"`
	From           []Address `json:"from"`
	To             []Address `json:"to"`
	Subject        string    `json:"subject"`
	Date           time.Time `json:"date"`
	Size           int64     `json:"size"`
	Seen           bool      `json:"seen"`
	Flagged        bool      `json:"flagged"`
	Answered       bool      `json:"answered"`
	Forwarded      bool      `json:"forwarded"`
	Draft          bool      `json:"draft"`
	HasAttachments bool      `json:"has_attachments"`
}

type Page struct {
	Total    int       `json:"total"`
	Offset   int       `json:"offset"`
	Messages []Summary `json:"messages"`
}

type ListQuery struct {
	Folder string
	Offset int
	Limit  int
	Text   string // поиск по заголовкам и тексту
	Filter string // "" | unread | flagged | unanswered
}

func addrs(list []imap.Address) []Address {
	out := make([]Address, 0, len(list))
	for _, a := range list {
		if a.Mailbox == "" && a.Host == "" {
			continue // начало/конец группы
		}
		out = append(out, Address{Name: a.Name, Email: a.Addr()})
	}
	return out
}

func hasFlag(flags []imap.Flag, f imap.Flag) bool {
	for _, x := range flags {
		if strings.EqualFold(string(x), string(f)) {
			return true
		}
	}
	return false
}

func hasAttachments(bs imap.BodyStructure) bool {
	found := false
	if bs == nil {
		return false
	}
	bs.Walk(func(path []int, part imap.BodyStructure) bool {
		sp, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}
		if d := sp.Disposition(); d != nil && strings.EqualFold(d.Value, "attachment") {
			found = true
		} else if sp.Filename() != "" && !strings.HasPrefix(sp.MediaType(), "text/") {
			found = true
		}
		return !found
	})
	return found
}

// ListMessages возвращает страницу писем, новые сверху.
func ListMessages(c *imapclient.Client, q ListQuery) (*Page, error) {
	if _, err := c.Select(q.Folder, nil).Wait(); err != nil {
		return nil, err
	}
	crit := &imap.SearchCriteria{}
	if t := strings.TrimSpace(q.Text); t != "" {
		crit.Text = []string{t}
	}
	switch q.Filter {
	case "unread":
		crit.NotFlag = append(crit.NotFlag, imap.FlagSeen)
	case "flagged":
		crit.Flag = append(crit.Flag, imap.FlagFlagged)
	case "unanswered":
		crit.NotFlag = append(crit.NotFlag, imap.FlagAnswered)
	}

	var uids []imap.UID
	if c.Caps().Has(imap.CapSort) {
		nums, err := c.UIDSort(&imapclient.SortOptions{
			SearchCriteria: crit,
			SortCriteria:   []imapclient.SortCriterion{{Key: imapclient.SortKeyDate, Reverse: true}},
		}).Wait()
		if err != nil {
			return nil, err
		}
		for _, n := range nums {
			uids = append(uids, imap.UID(n))
		}
	} else {
		data, err := c.UIDSearch(crit, nil).Wait()
		if err != nil {
			return nil, err
		}
		all := data.AllUIDs()
		for i := len(all) - 1; i >= 0; i-- { // по порядку поступления, новые сверху
			uids = append(uids, all[i])
		}
	}

	page := &Page{Total: len(uids), Offset: q.Offset, Messages: []Summary{}}
	if q.Offset >= len(uids) {
		return page, nil
	}
	end := q.Offset + q.Limit
	if end > len(uids) {
		end = len(uids)
	}
	window := uids[q.Offset:end]

	bufs, err := c.Fetch(imap.UIDSetNum(window...), &imap.FetchOptions{
		UID: true, Flags: true, Envelope: true, RFC822Size: true, InternalDate: true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	}).Collect()
	if err != nil {
		return nil, err
	}
	byUID := make(map[imap.UID]*imapclient.FetchMessageBuffer, len(bufs))
	for _, b := range bufs {
		byUID[b.UID] = b
	}
	for _, uid := range window {
		b, ok := byUID[uid]
		if !ok {
			continue // удалено между SORT и FETCH
		}
		s := Summary{
			UID: uint32(uid), Size: b.RFC822Size, Date: b.InternalDate,
			Seen: hasFlag(b.Flags, imap.FlagSeen), Flagged: hasFlag(b.Flags, imap.FlagFlagged),
			Answered: hasFlag(b.Flags, imap.FlagAnswered), Forwarded: hasFlag(b.Flags, imap.FlagForwarded),
			Draft: hasFlag(b.Flags, imap.FlagDraft), HasAttachments: hasAttachments(b.BodyStructure),
		}
		if e := b.Envelope; e != nil {
			s.Subject = e.Subject
			s.From = addrs(e.From)
			s.To = addrs(e.To)
			if !e.Date.IsZero() {
				s.Date = e.Date
			}
		}
		page.Messages = append(page.Messages, s)
	}
	return page, nil
}

// Attachment — вложение или inline-часть письма.
type Attachment struct {
	Index       int    `json:"index"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	Inline      bool   `json:"inline"`
	ContentID   string `json:"content_id,omitempty"`
}

type Message struct {
	UID         uint32       `json:"uid"`
	Folder      string       `json:"folder"`
	MessageID   string       `json:"message_id,omitempty"`
	InReplyTo   string       `json:"in_reply_to,omitempty"`
	References  []string     `json:"references,omitempty"`
	From        []Address    `json:"from"`
	To          []Address    `json:"to"`
	Cc          []Address    `json:"cc,omitempty"`
	Bcc         []Address    `json:"bcc,omitempty"` // есть только в черновиках и «Отправленных» некоторых клиентов
	ReplyTo     []Address    `json:"reply_to,omitempty"`
	Subject     string       `json:"subject"`
	Date        time.Time    `json:"date"`
	Text        string       `json:"text,omitempty"`
	HTML        string       `json:"html,omitempty"` // сырой HTML; санитизация — в API
	Attachments []Attachment `json:"attachments"`
	Flags       []string     `json:"flags"`
	ListUnsub   string       `json:"list_unsubscribe,omitempty"`
}

func fetchRaw(c *imapclient.Client, folder string, uid imap.UID) ([]byte, []imap.Flag, error) {
	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return nil, nil, err
	}
	section := &imap.FetchItemBodySection{Peek: true}
	bufs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
		UID: true, Flags: true, BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return nil, nil, err
	}
	if len(bufs) == 0 || len(bufs[0].BodySection) == 0 {
		return nil, nil, ErrNotFound
	}
	return bufs[0].BodySection[0].Bytes, bufs[0].Flags, nil
}

func mailAddrs(h *gomail.Header, key string) []Address {
	list, err := h.AddressList(key)
	if err != nil {
		return nil
	}
	out := make([]Address, 0, len(list))
	for _, a := range list {
		out = append(out, Address{Name: a.Name, Email: a.Address})
	}
	return out
}

// partVisitor получает каждую листовую часть письма по порядку.
type partVisitor func(index int, att Attachment, isBody bool, ctype string, body io.Reader) (stop bool, err error)

func walkParts(raw []byte, visit partVisitor) (*gomail.Header, error) {
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) {
		return nil, err
	}
	hdr := mr.Header
	index := 0
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			if message.IsUnknownCharset(err) {
				continue
			}
			return &hdr, err
		}
		var att Attachment
		isBody := false
		var ctype string
		switch h := p.Header.(type) {
		case *gomail.InlineHeader:
			ctype, _, _ = h.ContentType()
			cid := strings.Trim(h.Get("Content-Id"), "<>")
			if (ctype == "text/plain" || ctype == "text/html") && cid == "" {
				isBody = true
			} else {
				att = Attachment{ContentType: ctype, Inline: true, ContentID: cid}
				_, params, _ := h.ContentType()
				att.Filename = params["name"]
			}
		case *gomail.AttachmentHeader:
			ctype, _, _ = h.ContentType()
			name, _ := h.Filename()
			att = Attachment{ContentType: ctype, Filename: name, ContentID: strings.Trim(h.Get("Content-Id"), "<>")}
			if disp, _, _ := mime.ParseMediaType(h.Get("Content-Disposition")); disp == "inline" && att.ContentID != "" {
				att.Inline = true
			}
		}
		if !isBody {
			att.Index = index
			index++
		}
		stop, err := visit(att.Index, att, isBody, ctype, p.Body)
		if err != nil || stop {
			return &hdr, err
		}
	}
	return &hdr, nil
}

// GetMessage загружает письмо, разбирает MIME и помечает его прочитанным.
func GetMessage(c *imapclient.Client, folder string, uid uint32, markSeen bool) (*Message, error) {
	raw, flags, err := fetchRaw(c, folder, imap.UID(uid))
	if err != nil {
		return nil, err
	}
	m := &Message{UID: uid, Folder: folder, Attachments: []Attachment{}}
	for _, f := range flags {
		m.Flags = append(m.Flags, string(f))
	}
	hdr, err := walkParts(raw, func(_ int, att Attachment, isBody bool, ctype string, body io.Reader) (bool, error) {
		data, err := io.ReadAll(io.LimitReader(body, 32<<20))
		if err != nil {
			return false, nil // битая часть не должна ломать всё письмо
		}
		if isBody {
			if ctype == "text/html" && m.HTML == "" {
				m.HTML = string(data)
			} else if ctype == "text/plain" && m.Text == "" {
				m.Text = string(data)
			}
			return false, nil
		}
		att.Size = len(data)
		if att.Filename == "" {
			att.Filename = defaultName(att)
		}
		m.Attachments = append(m.Attachments, att)
		return false, nil
	})
	if err != nil && hdr == nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}
	if hdr != nil {
		m.Subject, _ = hdr.Subject()
		m.Date, _ = hdr.Date()
		m.MessageID, _ = hdr.MessageID()
		m.References, _ = hdr.MsgIDList("References")
		if irt, _ := hdr.MsgIDList("In-Reply-To"); len(irt) > 0 {
			m.InReplyTo = irt[0]
		}
		m.Bcc = mailAddrs(hdr, "Bcc")
		m.From = mailAddrs(hdr, "From")
		m.To = mailAddrs(hdr, "To")
		m.Cc = mailAddrs(hdr, "Cc")
		m.ReplyTo = mailAddrs(hdr, "Reply-To")
		m.ListUnsub = hdr.Get("List-Unsubscribe")
	}
	if markSeen && !hasFlag(flags, imap.FlagSeen) {
		err := c.Store(imap.UIDSetNum(imap.UID(uid)), &imap.StoreFlags{
			Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen},
		}, nil).Close()
		if err == nil {
			m.Flags = append(m.Flags, string(imap.FlagSeen))
		}
	}
	return m, nil
}

func defaultName(att Attachment) string {
	exts, _ := mime.ExtensionsByType(att.ContentType)
	ext := ".bin"
	if len(exts) > 0 {
		ext = exts[0]
	}
	return fmt.Sprintf("part-%d%s", att.Index+1, ext)
}

// GetPart возвращает содержимое вложения по индексу.
func GetPart(c *imapclient.Client, folder string, uid uint32, index int) (*Attachment, []byte, error) {
	raw, _, err := fetchRaw(c, folder, imap.UID(uid))
	if err != nil {
		return nil, nil, err
	}
	var found *Attachment
	var data []byte
	_, err = walkParts(raw, func(i int, att Attachment, isBody bool, _ string, body io.Reader) (bool, error) {
		if isBody || i != index {
			return false, nil
		}
		b, err := io.ReadAll(body)
		if err != nil {
			return true, err
		}
		att.Size = len(b)
		if att.Filename == "" {
			att.Filename = defaultName(att)
		}
		found, data = &att, b
		return true, nil
	})
	if err != nil {
		return nil, nil, err
	}
	if found == nil {
		return nil, nil, ErrNotFound
	}
	return found, data, nil
}

// GetRaw возвращает исходник письма (.eml).
func GetRaw(c *imapclient.Client, folder string, uid uint32) ([]byte, error) {
	raw, _, err := fetchRaw(c, folder, imap.UID(uid))
	return raw, err
}

func uidSet(uids []uint32) imap.UIDSet {
	set := imap.UIDSet{}
	for _, u := range uids {
		set.AddNum(imap.UID(u))
	}
	return set
}

// SetFlags добавляет или снимает флаги.
func SetFlags(c *imapclient.Client, folder string, uids []uint32, flags []string, add bool) error {
	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return err
	}
	op := imap.StoreFlagsDel
	if add {
		op = imap.StoreFlagsAdd
	}
	fl := make([]imap.Flag, 0, len(flags))
	for _, f := range flags {
		fl = append(fl, imap.Flag(f))
	}
	return c.Store(uidSet(uids), &imap.StoreFlags{Op: op, Silent: true, Flags: fl}, nil).Close()
}

// Move перемещает письма (MOVE или COPY+EXPUNGE, если MOVE не поддержан).
func Move(c *imapclient.Client, folder string, uids []uint32, dest string) error {
	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return err
	}
	_, err := c.Move(uidSet(uids), dest).Wait()
	return err
}

// Expunge удаляет письма навсегда.
func Expunge(c *imapclient.Client, folder string, uids []uint32) error {
	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return err
	}
	set := uidSet(uids)
	if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		return err
	}
	if c.Caps().Has(imap.CapUIDPlus) || c.Caps().Has(imap.CapIMAP4rev2) {
		return c.UIDExpunge(set).Close()
	}
	return c.Expunge().Close()
}

// Append кладёт письмо в папку (Sent, Drafts) и возвращает его UID,
// если сервер поддерживает UIDPLUS (иначе 0).
func Append(c *imapclient.Client, folder string, raw []byte, flags ...imap.Flag) (uint32, error) {
	cmd := c.Append(folder, int64(len(raw)), &imap.AppendOptions{Flags: flags, Time: time.Now()})
	if _, err := cmd.Write(raw); err != nil {
		return 0, err
	}
	if err := cmd.Close(); err != nil {
		return 0, err
	}
	data, err := cmd.Wait()
	if err != nil {
		return 0, err
	}
	return uint32(data.UID), nil
}
