package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestIdentitiesLifecycle(t *testing.T) {
	s, ctx := open(t), context.Background()
	ids, err := s.Identities(ctx, "bob@x.test")
	if err != nil || len(ids) != 1 || ids[0].Email != "bob@x.test" || !ids[0].IsDefault {
		t.Fatalf("default identity: %+v %v", ids, err)
	}
	if _, err := s.Identities(ctx, "bob@x.test"); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.Identities(ctx, "bob@x.test"); len(ids) != 1 {
		t.Fatalf("default created twice: %d", len(ids))
	}

	sales, err := s.SaveIdentity(ctx, "bob@x.test", Identity{Name: "Отдел продаж", Email: "sales@x.test", Signature: "<p>--</p>", IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	ids, _ = s.Identities(ctx, "bob@x.test")
	if len(ids) != 2 || ids[0].ID != sales.ID || ids[1].IsDefault {
		t.Fatalf("default not switched: %+v", ids)
	}

	// Чужой ящик не видит и не меняет отправителей bob.
	if _, err := s.Identity(ctx, "eve@x.test", sales.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-mailbox read: %v", err)
	}
	if _, err := s.SaveIdentity(ctx, "eve@x.test", Identity{ID: sales.ID, Email: "evil@x.test"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-mailbox update: %v", err)
	}
	if err := s.DeleteIdentity(ctx, "eve@x.test", sales.ID); err == nil {
		t.Fatal("cross-mailbox delete allowed")
	}

	if err := s.DeleteIdentity(ctx, "bob@x.test", sales.ID); err != nil {
		t.Fatal(err)
	}
	ids, _ = s.Identities(ctx, "bob@x.test")
	if len(ids) != 1 || !ids[0].IsDefault {
		t.Fatalf("default not restored after delete: %+v", ids)
	}
	if err := s.DeleteIdentity(ctx, "bob@x.test", ids[0].ID); !errors.Is(err, ErrLastOne) {
		t.Fatalf("last identity deleted: %v", err)
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.db")
	for i := 0; i < 2; i++ {
		s, err := Open(p)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		s.Close()
	}
}
