package sanitize

import (
	"strings"
	"testing"
)

func noCID(string) string { return "" }

func TestScriptsAndHandlersRemoved(t *testing.T) {
	in := `<p onclick="x()">Привет</p><script>alert(1)</script><a href="javascript:alert(1)">a</a>
<iframe src="https://evil"></iframe><style>body{display:none}</style><form action="/x"><input></form>`
	out := Email(in, noCID, false).HTML
	for _, bad := range []string{"<script", "onclick", "javascript:", "<iframe", "<style", "<form", "<input", "alert"} {
		if strings.Contains(out, bad) {
			t.Errorf("output contains %q:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "Привет") {
		t.Errorf("text lost: %s", out)
	}
}

func TestRemoteImagesBlocked(t *testing.T) {
	in := `<img src="https://track.example/p.png"><img src="//cdn.example/x.png"><img src="data:image/png;base64,AAAA">`
	r := Email(in, noCID, false)
	if r.BlockedImages != 2 {
		t.Errorf("blocked = %d, want 2: %s", r.BlockedImages, r.HTML)
	}
	if strings.Contains(r.HTML, ` src="https://`) || strings.Contains(r.HTML, ` src="//`) ||
		strings.Contains(r.HTML, `<img src="https://`) || strings.Contains(r.HTML, `<img src="//`) {
		t.Errorf("remote src kept: %s", r.HTML)
	}
	if !strings.Contains(r.HTML, `src="data:image/png`) {
		t.Errorf("data image dropped: %s", r.HTML)
	}
	r = Email(in, noCID, true)
	if r.BlockedImages != 0 || !strings.Contains(r.HTML, `src="https://track.example/p.png"`) {
		t.Errorf("allowRemote must keep images: %+v", r)
	}
}

func TestCIDRewritten(t *testing.T) {
	r := Email(`<img src="cid:logo@x">`, func(cid string) string {
		if cid == "logo@x" {
			return "/api/v1/part?index=2"
		}
		return ""
	}, false)
	if !strings.Contains(r.HTML, `src="/api/v1/part?index=2"`) {
		t.Errorf("cid not rewritten: %s", r.HTML)
	}
}

func TestStylesFiltered(t *testing.T) {
	r := Email(`<p style="color:red;background-image:url(https://x/y);position:fixed">x</p>`, noCID, false)
	if !strings.Contains(r.HTML, "color: red") && !strings.Contains(r.HTML, "color:red") {
		t.Errorf("safe style dropped: %s", r.HTML)
	}
	if strings.Contains(r.HTML, "url(") || strings.Contains(r.HTML, "position") {
		t.Errorf("unsafe style kept: %s", r.HTML)
	}
}

func TestLinksOpenSafely(t *testing.T) {
	r := Email(`<a href="https://example.com">x</a>`, noCID, false)
	if !strings.Contains(r.HTML, `target="_blank"`) || !strings.Contains(r.HTML, "noreferrer") {
		t.Errorf("link attrs: %s", r.HTML)
	}
}

func TestTextEscapedAndLinkified(t *testing.T) {
	out := Text("<b>hi</b> see https://example.com/a?b=1&c=2.\n> quoted")
	if strings.Contains(out, "<b>") {
		t.Errorf("html not escaped: %s", out)
	}
	if !strings.Contains(out, `href="https://example.com/a?b=1&amp;c=2"`) {
		t.Errorf("link: %s", out)
	}
	if !strings.Contains(out, "&gt; quoted") {
		t.Errorf("quote: %s", out)
	}
}
