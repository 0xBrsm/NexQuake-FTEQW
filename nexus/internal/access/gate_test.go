package access

import "testing"

func TestBlocklist(t *testing.T) {
	b := NewBlocklist()
	if b.IsBlocked("198.51.100.10") {
		t.Fatal("fresh blocklist should not block source")
	}
	b.Block("198.51.100.10")
	if !b.IsBlocked("198.51.100.10") {
		t.Fatal("expected source to be blocked")
	}
}
