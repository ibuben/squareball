package mail

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Формат mboxrd: письма разделены строками "From адрес дата", а строки тела,
// начинающиеся с ">*From ", экранируются ещё одним ">". Его читают Thunderbird,
// Apple Mail, mutt, Google Takeout и Roundcube.

var reFromQuoted = regexp.MustCompile(`^>*From `)

// --- экспорт ------------------------------------------------------------------

// exportBatch — сколько писем забирать с сервера за раз.
const exportBatch = 50

// WriteMbox пишет письма папки (или только uids) в w. Флаги сохраняются
// в заголовках Status и X-Status, как у Thunderbird. Возвращает число писем.
func WriteMbox(c *imapclient.Client, folder string, uids []uint32, w io.Writer) (int, error) {
	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return 0, err
	}
	var list []imap.UID
	if len(uids) > 0 {
		for _, u := range uids {
			list = append(list, imap.UID(u))
		}
	} else {
		data, err := c.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
		if err != nil {
			return 0, err
		}
		list = data.AllUIDs()
	}
	bw := bufio.NewWriterSize(w, 64<<10)
	n := 0
	section := &imap.FetchItemBodySection{Peek: true}
	for start := 0; start < len(list); start += exportBatch {
		batch := list[start:min(start+exportBatch, len(list))]
		msgs, err := c.Fetch(imap.UIDSetNum(batch...), &imap.FetchOptions{
			UID: true, Flags: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{section},
		}).Collect()
		if err != nil {
			return n, err
		}
		byUID := make(map[imap.UID]*imapclient.FetchMessageBuffer, len(msgs))
		for _, m := range msgs {
			byUID[m.UID] = m
		}
		for _, uid := range batch {
			m, ok := byUID[uid]
			if !ok {
				continue
			}
			if err := writeMboxMessage(bw, m.FindBodySection(section), m.InternalDate, m.Flags); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, bw.Flush()
}

func writeMboxMessage(w *bufio.Writer, raw []byte, date time.Time, flags []imap.Flag) error {
	if date.IsZero() {
		date = time.Now()
	}
	sender := "MAILER-DAEMON"
	if msg, err := mail.ReadMessage(bytes.NewReader(raw)); err == nil {
		if list, err := msg.Header.AddressList("From"); err == nil && len(list) > 0 && !strings.ContainsAny(list[0].Address, " \t") {
			sender = list[0].Address
		}
	}
	fmt.Fprintf(w, "From %s %s\n", sender, date.UTC().Format("Mon Jan _2 15:04:05 2006"))

	// Заголовки Status/X-Status ставим свои — вместо старых.
	status, xstatus := "O", ""
	if hasFlag(flags, imap.FlagSeen) {
		status = "RO"
	}
	if hasFlag(flags, imap.FlagAnswered) {
		xstatus += "A"
	}
	if hasFlag(flags, imap.FlagFlagged) {
		xstatus += "F"
	}
	if hasFlag(flags, imap.FlagDraft) {
		xstatus += "T"
	}
	inHeader := true
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if inHeader {
			lower := strings.ToLower(line)
			if line == "" {
				inHeader = false
				fmt.Fprintf(w, "Status: %s\n", status)
				if xstatus != "" {
					fmt.Fprintf(w, "X-Status: %s\n", xstatus)
				}
			} else if strings.HasPrefix(lower, "status:") || strings.HasPrefix(lower, "x-status:") {
				continue
			}
		}
		if reFromQuoted.MatchString(line) {
			w.WriteByte('>')
		}
		w.WriteString(line)
		w.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if inHeader { // письмо без тела
		fmt.Fprintf(w, "Status: %s\n\n", status)
	}
	_, err := w.WriteString("\n") // пустая строка перед следующим "From "
	return err
}

// --- импорт -------------------------------------------------------------------

// ErrMessageTooLarge — письмо в mbox больше допустимого.
var ErrMessageTooLarge = errors.New("message too large")

// MboxMessage — письмо из файла mbox: исходник с CRLF, дата и флаги.
type MboxMessage struct {
	Raw   []byte
	Date  time.Time
	Flags []imap.Flag
}

// MboxReader читает письма из mbox (mboxo/mboxrd) или одиночного .eml.
type MboxReader struct {
	br          *bufio.Reader
	max         int
	pending     string // строка "From " следующего письма
	pendingBody string // первая строка .eml без строки "From "
	started     bool
	eof         bool
}

func NewMboxReader(r io.Reader, maxMessage int) *MboxReader {
	return &MboxReader{br: bufio.NewReaderSize(r, 64<<10), max: maxMessage}
}

func (m *MboxReader) readLine() (string, error) {
	line, err := m.br.ReadString('\n')
	if err == io.EOF && line != "" {
		err = nil
	}
	return strings.TrimRight(line, "\r\n"), err
}

// Next возвращает следующее письмо или io.EOF. Письмо больше лимита
// пропускается целиком и возвращается с ErrMessageTooLarge.
func (m *MboxReader) Next() (*MboxMessage, error) {
	if m.eof {
		return nil, io.EOF
	}
	fromLine := m.pending
	m.pending = ""
	if !m.started {
		m.started = true
		// Пропускаем пустые строки в начале; первая непустая решает, mbox это или .eml.
		for {
			line, err := m.readLine()
			if err == io.EOF {
				m.eof = true
				return nil, io.EOF
			}
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			if strings.HasPrefix(line, "From ") {
				fromLine = line
			} else {
				m.pendingBody = line + "\n"
			}
			break
		}
	}
	var buf bytes.Buffer
	if m.pendingBody != "" {
		buf.WriteString(strings.TrimSuffix(m.pendingBody, "\n") + "\r\n")
		m.pendingBody = ""
	}
	tooLarge := false
	prevBlank := true
	got := buf.Len() > 0 // есть строки текущего письма (даже если оно уже отброшено)
	for {
		line, err := m.readLine()
		if err == io.EOF {
			m.eof = true
			break
		}
		if err != nil {
			return nil, err
		}
		if prevBlank && strings.HasPrefix(line, "From ") && got {
			m.pending = line
			break
		}
		prevBlank = line == ""
		got = true
		if reFromQuoted.MatchString(line) && strings.HasPrefix(line, ">") {
			line = line[1:]
		}
		if !tooLarge {
			buf.WriteString(line)
			buf.WriteString("\r\n")
			if m.max > 0 && buf.Len() > m.max {
				tooLarge = true
				buf.Reset()
			}
		}
	}
	if tooLarge {
		return nil, ErrMessageTooLarge
	}
	raw := bytes.TrimRight(buf.Bytes(), "\r\n")
	if len(raw) == 0 {
		if m.eof {
			return nil, io.EOF
		}
		return m.Next()
	}
	raw = append(raw, '\r', '\n')
	return parseMboxMessage(raw, fromLine), nil
}

func parseMboxMessage(raw []byte, fromLine string) *MboxMessage {
	out := &MboxMessage{}
	// Дата: из строки "From " (время получения), иначе из заголовка Date.
	if f := strings.Fields(fromLine); len(f) >= 7 {
		if t, err := time.Parse("Mon Jan _2 15:04:05 2006", strings.Join(f[len(f)-5:], " ")); err == nil {
			out.Date = t
		}
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	headerEnd := bytes.Index(raw, []byte("\r\n\r\n"))
	if err == nil {
		if out.Date.IsZero() {
			out.Date, _ = msg.Header.Date()
		}
		status := msg.Header.Get("Status")
		xs := msg.Header.Get("X-Status")
		if strings.Contains(status, "R") {
			out.Flags = append(out.Flags, imap.FlagSeen)
		}
		if strings.Contains(xs, "A") {
			out.Flags = append(out.Flags, imap.FlagAnswered)
		}
		if strings.Contains(xs, "F") {
			out.Flags = append(out.Flags, imap.FlagFlagged)
		}
		if strings.Contains(xs, "T") {
			out.Flags = append(out.Flags, imap.FlagDraft)
		}
		// Служебные заголовки mbox в почтовом ящике не нужны.
		if headerEnd > 0 && (status != "" || xs != "") {
			raw = stripHeaders(raw, headerEnd, "status", "x-status")
		}
	}
	out.Raw = raw
	return out
}

func stripHeaders(raw []byte, headerEnd int, names ...string) []byte {
	head := string(raw[:headerEnd+2])
	var b strings.Builder
	skip := false
	for _, line := range strings.SplitAfter(head, "\r\n") {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if !skip {
				b.WriteString(line)
			}
			continue
		}
		name, _, _ := strings.Cut(line, ":")
		skip = false
		for _, n := range names {
			if strings.EqualFold(strings.TrimSpace(name), n) {
				skip = true
			}
		}
		if !skip {
			b.WriteString(line)
		}
	}
	return append([]byte(b.String()), raw[headerEnd+2:]...)
}
