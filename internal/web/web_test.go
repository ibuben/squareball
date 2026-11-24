package web

import (
	"strings"
	"testing"
)

func TestWithBase(t *testing.T) {
	got := string(withBase([]byte("<html><head><title>x</title></head></html>"), "/webmail"))
	if !strings.Contains(got, `<head>`+"\n"+`    <base href="/webmail/" />`) {
		t.Fatal(got)
	}
	if got := string(withBase([]byte("<head>"), "")); !strings.Contains(got, `<base href="/" />`) {
		t.Fatal(got)
	}
}
