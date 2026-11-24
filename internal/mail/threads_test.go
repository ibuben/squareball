package mail

import (
	"testing"

	"github.com/emersion/go-imap/v2/imapclient"
)

func TestFlattenThread(t *testing.T) {
	// (1 2 (3 4)(5)) — 1 → 2 → {3 → 4, 5}
	td := imapclient.ThreadData{Chain: []uint32{1, 2}, SubThreads: []imapclient.ThreadData{
		{Chain: []uint32{3, 4}}, {Chain: []uint32{5}},
	}}
	var out []threadNode
	flattenThread(td, 0, &out)
	want := []threadNode{{1, 0}, {2, 1}, {3, 2}, {4, 3}, {5, 2}}
	if len(out) != len(want) {
		t.Fatalf("got %v", out)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("node %d: %v, want %v", i, out[i], want[i])
		}
	}
}
