package mail

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

const msgA = "From: Alice <alice@test.local>\r\nSubject: A\r\nDate: Mon, 02 Jan 2023 10:00:00 +0000\r\nStatus: O\r\n\r\nHello\r\nFrom here on\r\n>From quoted\r\n\r\nFrom the end\r\n"
const msgB = "From: bob@test.local\r\nSubject: B\r\n\r\nBody B\r\n"

func TestMboxRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	d := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := writeMboxMessage(w, []byte(msgA), d, []imap.Flag{imap.FlagSeen, imap.FlagFlagged}); err != nil {
		t.Fatal(err)
	}
	writeMboxMessage(w, []byte(msgB), d, nil)
	w.Flush()
	out := buf.String()
	for _, want := range []string{"From alice@test.local Mon May  6 07:08:09 2024\n", "\n>From here on\n", "\n>>From quoted\n",
		"\n>From the end\n", "Status: RO\nX-Status: F\n\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("mbox misses %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "Status: O\n") != 1 || strings.Contains(out, "Status: O\nStatus") {
		t.Errorf("old Status header must be replaced:\n%s", out)
	}

	r := NewMboxReader(strings.NewReader(out), 1<<20)
	m1, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !m1.Date.Equal(d) || !slices.Contains(m1.Flags, imap.FlagSeen) || !slices.Contains(m1.Flags, imap.FlagFlagged) {
		t.Errorf("meta: %v %v", m1.Date, m1.Flags)
	}
	body := string(m1.Raw)
	if !strings.Contains(body, "\r\nFrom here on\r\n>From quoted\r\n\r\nFrom the end\r\n") || strings.Contains(body, "Status:") {
		t.Errorf("body:\n%q", body)
	}
	m2, err := r.Next()
	if err != nil || !strings.Contains(string(m2.Raw), "Subject: B") || len(m2.Flags) != 0 {
		t.Fatalf("second: %v %+v", err, m2)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Errorf("want EOF, got %v", err)
	}
}

func TestMboxReaderEdgeCases(t *testing.T) {
	// .eml без строки "From " — одно письмо, дата из заголовка.
	r := NewMboxReader(strings.NewReader(strings.ReplaceAll(msgA, "\r\n", "\n")), 0)
	m, err := r.Next()
	if err != nil || m.Date.Year() != 2023 || !bytes.HasPrefix(m.Raw, []byte("From: Alice")) {
		t.Fatalf("eml: %v %+v", err, m)
	}
	// Слишком большое письмо пропускается, следующее читается.
	big := "From a@b Mon Jan  1 00:00:00 2024\nSubject: big\n\n" + strings.Repeat("x", 3000) + "\n\nFrom a@b Mon Jan  1 00:00:00 2024\nSubject: small\n\nok\n"
	r = NewMboxReader(strings.NewReader(big), 1000)
	if _, err := r.Next(); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("want too large, got %v", err)
	}
	if m, err := r.Next(); err != nil || !strings.Contains(string(m.Raw), "Subject: small") {
		t.Fatalf("after big: %v %+v", err, m)
	}
	if _, err := NewMboxReader(strings.NewReader("\n\n"), 0).Next(); err != io.EOF {
		t.Errorf("empty: %v", err)
	}
}
