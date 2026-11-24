package mail

import (
	"sort"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// maxThreadSize — сколько писем одной цепочки отдаём в списке (огромные
// рассылочные «цепочки» не должны раздувать ответ).
const maxThreadSize = 200

// ThreadAlgorithm — алгоритм THREAD, который поддерживает сервер ("" — никакой).
func ThreadAlgorithm(c *imapclient.Client) imap.ThreadAlgorithm {
	algs := c.Caps().ThreadAlgorithms()
	for _, want := range []imap.ThreadAlgorithm{imap.ThreadReferences, imap.ThreadOrderedSubject} {
		for _, a := range algs {
			if a == want {
				return a
			}
		}
	}
	return ""
}

type threadNode struct {
	uid   imap.UID
	depth int
}

// flattenThread обходит дерево THREAD в глубину: родитель раньше ответов.
func flattenThread(t imapclient.ThreadData, depth int, out *[]threadNode) {
	for i, n := range t.Chain {
		*out = append(*out, threadNode{imap.UID(n), depth + i})
	}
	d := depth + len(t.Chain)
	for _, sub := range t.SubThreads {
		flattenThread(sub, d, out)
	}
}

// ListThreads — страница цепочек: цепочки упорядочены по самому новому письму,
// строка цепочки — это её самое новое письмо, в Thread — все письма цепочки.
// Без поддержки THREAD на сервере работает как ListMessages.
func ListThreads(c *imapclient.Client, q ListQuery) (*Page, error) {
	alg := ThreadAlgorithm(c)
	if alg == "" {
		return ListMessages(c, q)
	}
	if _, err := c.Select(q.Folder, nil).Wait(); err != nil {
		return nil, err
	}
	crit := listCriteria(q)
	data, err := c.UIDThread(&imapclient.ThreadOptions{Algorithm: alg, SearchCriteria: crit}).Wait()
	if err != nil {
		return nil, err
	}
	order, err := sortedUIDs(c, crit)
	if err != nil {
		return nil, err
	}
	rank := make(map[imap.UID]int, len(order))
	for i, u := range order {
		rank[u] = i
	}
	type thread struct {
		nodes []threadNode
		head  imap.UID // самое новое письмо
		rank  int
	}
	threads := make([]thread, 0, len(data))
	for _, td := range data {
		var nodes []threadNode
		flattenThread(td, 0, &nodes)
		if len(nodes) == 0 {
			continue
		}
		t := thread{nodes: nodes, rank: len(order)}
		for _, n := range nodes {
			if r, ok := rank[n.uid]; ok && r < t.rank {
				t.rank, t.head = r, n.uid
			}
		}
		if t.head == 0 {
			t.head = nodes[len(nodes)-1].uid
		}
		threads = append(threads, t)
	}
	sort.SliceStable(threads, func(i, j int) bool { return threads[i].rank < threads[j].rank })

	page := &Page{Total: len(threads), Offset: q.Offset, Messages: []Summary{}}
	if q.Offset >= len(threads) {
		return page, nil
	}
	end := min(q.Offset+q.Limit, len(threads))
	window := threads[q.Offset:end]

	var want []imap.UID
	for i := range window {
		if len(window[i].nodes) > maxThreadSize {
			// Оставляем самые новые письма, но голову — обязательно.
			sort.SliceStable(window[i].nodes, func(a, b int) bool { return rank[window[i].nodes[a].uid] < rank[window[i].nodes[b].uid] })
			window[i].nodes = window[i].nodes[:maxThreadSize]
		}
		for _, n := range window[i].nodes {
			want = append(want, n.uid)
		}
	}
	sums, err := fetchSummaries(c, want)
	if err != nil {
		return nil, err
	}
	byUID := make(map[uint32]Summary, len(sums))
	for _, s := range sums {
		byUID[s.UID] = s
	}
	for _, t := range window {
		head, ok := byUID[uint32(t.head)]
		if !ok {
			continue
		}
		if len(t.nodes) > 1 {
			for _, n := range t.nodes {
				if s, ok := byUID[uint32(n.uid)]; ok {
					s.Depth = min(n.depth, 8)
					head.Thread = append(head.Thread, s)
				}
			}
		}
		page.Messages = append(page.Messages, head)
	}
	return page, nil
}
