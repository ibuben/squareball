package api

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Метрики в текстовом формате Prometheus (без внешних зависимостей).
// Отдаются на отдельном адресе metrics.listen, не на публичном порту.

var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

type routeKey struct{ route, code string }

type histogram struct {
	counts []uint64 // по latencyBuckets, накопительно не храним
	sum    float64
	n      uint64
}

type metrics struct {
	mu       sync.Mutex
	requests map[routeKey]uint64
	latency  map[string]*histogram
	started  time.Time
}

func newMetrics() *metrics {
	return &metrics{requests: map[routeKey]uint64{}, latency: map[string]*histogram{}, started: time.Now()}
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Flush нужен потоку событий (SSE).
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// instrument считает запросы и время ответа по шаблону маршрута.
func (m *metrics) instrument(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		h.ServeHTTP(sw, r)
		route := r.Pattern // ServeMux заполняет его при выборе обработчика
		if route == "" {
			route = "other"
		}
		code := sw.code
		if code == 0 {
			code = http.StatusOK
		}
		d := time.Since(start).Seconds()
		m.mu.Lock()
		m.requests[routeKey{route, strconv.Itoa(code)}]++
		if route != "GET /api/v1/events" { // длинный поток — не задержка
			hg := m.latency[route]
			if hg == nil {
				hg = &histogram{counts: make([]uint64, len(latencyBuckets))}
				m.latency[route] = hg
			}
			for i, b := range latencyBuckets {
				if d <= b {
					hg.counts[i]++
				}
			}
			hg.sum += d
			hg.n++
		}
		m.mu.Unlock()
	})
}

func escLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

// MetricsHandler — /metrics для metrics.listen.
func (s *Server) MetricsHandler(version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		var b strings.Builder
		gauge := func(name, help string, v float64) {
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, v)
		}
		fmt.Fprintf(&b, "# HELP webmail_build_info Версия сборки.\n# TYPE webmail_build_info gauge\nwebmail_build_info{version=\"%s\",go=\"%s\"} 1\n",
			escLabel(version), runtime.Version())
		gauge("webmail_sessions", "Активные сессии.", float64(s.sessions.Count()))
		gauge("webmail_imap_sessions", "Сессии с открытым IMAP-соединением.", float64(s.pool.Sessions()))
		gauge("webmail_event_streams", "Открытые потоки событий (IMAP IDLE).", float64(s.streams.total()))
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		gauge("go_goroutines", "Число горутин.", float64(runtime.NumGoroutine()))
		gauge("go_memstats_heap_alloc_bytes", "Занятая куча.", float64(ms.HeapAlloc))
		gauge("go_memstats_sys_bytes", "Память, полученная от ОС.", float64(ms.Sys))
		gauge("process_start_time_seconds", "Время запуска.", float64(s.metrics.started.Unix()))

		m := s.metrics
		m.mu.Lock()
		keys := make([]routeKey, 0, len(m.requests))
		for k := range m.requests {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].route != keys[j].route {
				return keys[i].route < keys[j].route
			}
			return keys[i].code < keys[j].code
		})
		b.WriteString("# HELP webmail_http_requests_total Запросы по маршруту и коду ответа.\n# TYPE webmail_http_requests_total counter\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "webmail_http_requests_total{route=\"%s\",code=\"%s\"} %d\n", escLabel(k.route), k.code, m.requests[k])
		}
		routes := make([]string, 0, len(m.latency))
		for r := range m.latency {
			routes = append(routes, r)
		}
		sort.Strings(routes)
		b.WriteString("# HELP webmail_http_request_duration_seconds Время ответа.\n# TYPE webmail_http_request_duration_seconds histogram\n")
		for _, r := range routes {
			hg := m.latency[r]
			for i, le := range latencyBuckets {
				fmt.Fprintf(&b, "webmail_http_request_duration_seconds_bucket{route=\"%s\",le=\"%g\"} %d\n", escLabel(r), le, hg.counts[i])
			}
			fmt.Fprintf(&b, "webmail_http_request_duration_seconds_bucket{route=\"%s\",le=\"+Inf\"} %d\n", escLabel(r), hg.n)
			fmt.Fprintf(&b, "webmail_http_request_duration_seconds_sum{route=\"%s\"} %g\n", escLabel(r), hg.sum)
			fmt.Fprintf(&b, "webmail_http_request_duration_seconds_count{route=\"%s\"} %d\n", escLabel(r), hg.n)
		}
		m.mu.Unlock()
		_, _ = w.Write([]byte(b.String()))
	})
}
