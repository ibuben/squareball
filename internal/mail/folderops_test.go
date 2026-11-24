package mail

import "testing"

func TestCheckFolderName(t *testing.T) {
	for _, ok := range []string{"Проекты", "2024 отчёты", "a.b"} {
		if err := CheckFolderName(ok, "/"); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " x", "x ", "a/b", "a*", "50%", "inbox", "a\tb", string(make([]rune, 129))} {
		if CheckFolderName(bad, "/") == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if CheckFolderName("a.b", ".") == nil {
		t.Error("delimiter '.' must be rejected")
	}
}

func TestProtectedAndChildren(t *testing.T) {
	fs := []Folder{
		{Name: "INBOX", Role: RoleInbox, Delimiter: "/"},
		{Name: "Mail", Delimiter: "/"},
		{Name: "Mail/Sent", Delimiter: "/", Depth: 1, Role: RoleSent},
		{Name: "Work", Delimiter: "/"},
		{Name: "Work/A", Delimiter: "/", Depth: 1},
		{Name: "Work/A/B", Delimiter: "/", Depth: 2},
		{Name: "Workshop", Delimiter: "/"},
	}
	if !protected(fs, &fs[1]) {
		t.Error("parent of a special folder must be protected")
	}
	if protected(fs, &fs[3]) {
		t.Error("ordinary folder is not protected")
	}
	ch := children(fs, "Work", "/")
	if len(ch) != 2 || ch[0].Name != "Work/A/B" {
		t.Errorf("children: %+v", ch)
	}
}

func TestZipNames(t *testing.T) {
	used := map[string]bool{}
	for in, want := range map[string]string{"../../etc/passwd": "_.._etc_passwd", "a\\b.txt": "a_b.txt", "..": "attachment", "c:\x01d": "c_d"} {
		if got := safeZipName(in); got != want {
			t.Errorf("safeZipName(%q) = %q, want %q", in, got, want)
		}
	}
	if a, b := uniqueName("x.pdf", used), uniqueName("X.pdf", used); a != "x.pdf" || b != "X (2).pdf" {
		t.Errorf("unique: %q %q", a, b)
	}
}
