package sieve

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ibuben/squareball/internal/config"
)

func devstand(t *testing.T) *config.Endpoint {
	if os.Getenv("WEBMAIL_IT") != "1" {
		t.Skip("set WEBMAIL_IT=1 with deploy/devstand running")
	}
	return &config.Endpoint{Host: "127.0.0.1", Port: 4190, Security: "starttls", TLSVerify: false}
}

func TestIntegrationClient(t *testing.T) {
	ep := devstand(t)
	if _, err := Dial(ep, Creds{Username: "bob@test.local", Password: "wrong"}); !errors.Is(err, ErrAuth) {
		t.Fatalf("bad password: %v", err)
	}
	c, err := Dial(ep, Creds{Username: "bob@test.local", Password: "Test12345!"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Extensions()["vacation"] || !c.Extensions()["fileinto"] {
		t.Fatalf("extensions: %v", c.Extensions())
	}
	bad := "require \"fileinto\";\nif header :contains \"subject\" \"x\" { fileinto }\n"
	var se *ServerError
	if err := c.CheckScript(bad); !errors.As(err, &se) || se.Message == "" {
		t.Fatalf("checkscript should fail with message: %v", err)
	}
	script := "# webmail test\r\nrequire \"fileinto\";\nif header :contains \"subject\" \"\\\"кавычки\\\"\" {\n  fileinto \"Junk\";\n}\n"
	if err := c.PutScript("webmail-it", script); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetScript("webmail-it")
	if err != nil || got != script {
		t.Fatalf("roundtrip:\n%q\n%q %v", got, script, err)
	}
	list, err := c.ListScripts()
	if err != nil || !strings.Contains(listNames(list), "webmail-it") {
		t.Fatalf("list: %v %v", list, err)
	}
	if _, err := c.cmd(`DELETESCRIPT "webmail-it"`); err != nil {
		t.Fatal(err)
	}
}

func listNames(l []Script) string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.Name + " ")
	}
	return b.String()
}

// Сгенерированный скрипт со всеми возможностями формы принимает сам Dovecot.
func TestIntegrationGeneratedScriptAccepted(t *testing.T) {
	ep := devstand(t)
	c, err := Dial(ep, Creds{Username: "bob@test.local", Password: "Test12345!"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.CheckScript(sample().Script("en")); err != nil {
		t.Fatalf("dovecot rejected generated script: %v\n%s", err, sample().Script("en"))
	}
	if err := c.CheckScript(roundcube); err != nil {
		t.Fatalf("roundcube sample: %v", err)
	}
}
