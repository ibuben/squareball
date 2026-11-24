package api

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/ibuben/squareball/internal/mail"
)

const (
	maxStreamsPerSession = 3                // вкладки одного пользователя
	sseHeartbeat         = 25 * time.Second // комментарий-пинг, чтобы прокси не рвали соединение
	sseCoalesce          = 400 * time.Millisecond
)

type streamCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func (sc *streamCounter) acquire(id string) bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.n == nil {
		sc.n = map[string]int{}
	}
	if sc.n[id] >= maxStreamsPerSession {
		return false
	}
	sc.n[id]++
	return true
}

func (sc *streamCounter) total() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	n := 0
	for _, v := range sc.n {
		n += v
	}
	return n
}

func (sc *streamCounter) release(id string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.n[id]--; sc.n[id] <= 0 {
		delete(sc.n, id)
	}
}

// handleEvents — поток Server-Sent Events. На время потока открывается отдельное
// IMAP-соединение в режиме IDLE на «Входящих»; любые изменения (новое письмо,
// удаление, смена флагов) превращаются в событие "mailbox". Остальные папки
// клиент обновляет опросом. Если соединение с IMAP рвётся, поток закрывается,
// а EventSource в браузере сам переподключается.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	if !s.streams.acquire(sess.ID) {
		writeErr(w, http.StatusTooManyRequests, "events.too_many", "")
		return
	}
	defer s.streams.release(sess.ID)

	changed := make(chan struct{}, 1)
	notify := func() {
		select {
		case changed <- struct{}{}:
		default: // событие уже ждёт отправки
		}
	}
	c, err := mail.DialIMAPWatch(&s.cfg.IMAP, s.creds(sess), &imapclient.UnilateralDataHandler{
		Expunge: func(uint32) { notify() },
		Mailbox: func(d *imapclient.UnilateralDataMailbox) {
			if d.NumMessages != nil {
				notify()
			}
		},
		Fetch: func(*imapclient.FetchMessageData) { notify() },
	})
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	defer c.Close()
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		s.mailError(w, r, err)
		return
	}
	idle, err := c.Idle()
	if err != nil {
		s.mailError(w, r, err)
		return
	}
	defer idle.Close()
	idleDone := make(chan error, 1)
	go func() { idleDone <- idle.Wait() }()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // nginx: не буферизовать поток
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 5000\n\nevent: ready\ndata: {}\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-idleDone:
			return // IMAP закрыл соединение — браузер переподключится
		case <-changed:
			time.Sleep(sseCoalesce) // склеиваем пачку EXISTS/FETCH в одно событие
			select {
			case <-changed:
			default:
			}
			fmt.Fprint(w, "event: mailbox\ndata: {\"folder\":\"INBOX\"}\n\n")
			flusher.Flush()
		case <-heartbeat.C:
			if !s.sessions.Alive(sess.ID) {
				return // сессия завершена (выход, смена пароля)
			}
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
