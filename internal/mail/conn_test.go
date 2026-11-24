package mail

import (
	"testing"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/ibuben/squareball/internal/config"
)

// После Close запрос, ждавший старую запись, должен получить новую запись пула,
// а не открыть соединение в удалённой (которое потом никто не закроет).
func TestWithAfterCloseUsesFreshEntry(t *testing.T) {
	cfg := config.Defaults().IMAP
	cfg.Host, cfg.Port = "127.0.0.1", 1 // соединение не откроется — нам важна только запись пула
	p := &Pool{cfg: &cfg, byID: map[string]*entry{}}
	_ = p.With("s", Creds{}, func(*imapclient.Client) error { return nil })
	old := p.byID["s"]
	p.Close("s")
	if !old.closed {
		t.Fatal("closed entry not marked")
	}
	_ = p.With("s", Creds{}, func(*imapclient.Client) error { return nil })
	if p.byID["s"] == old {
		t.Fatal("pool reused a closed entry")
	}
}
