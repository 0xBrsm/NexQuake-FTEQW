package access

import (
	"net/http/httptest"
	"testing"
)

func TestClientID_PrefersExplicitIdentity(t *testing.T) {
	if got := clientID("alice@example.com", "1.2.3.4"); got != "alice@example.com" {
		t.Fatalf("got %q", got)
	}
}

func TestClientID_FallsBackToSourceIP(t *testing.T) {
	if got := clientID("anonymous", "198.51.100.11"); got != "198.51.100.11" {
		t.Fatalf("got %q", got)
	}
}

func TestClientID_DefaultsToAnonymous(t *testing.T) {
	if got := clientID("", ""); got != "anonymous" {
		t.Fatalf("got %q", got)
	}
}

func TestClientSourceIP(t *testing.T) {
	t.Run("uses remote address by default", func(t *testing.T) {
		i := &Identity{}
		r := httptest.NewRequest("GET", "http://example/ws", nil)
		r.RemoteAddr = "198.51.100.10:7890"

		if got := i.ClientSourceIP(r); got != "198.51.100.10" {
			t.Fatalf("ClientSourceIP() = %q, want %q", got, "198.51.100.10")
		}
	})

	t.Run("ignores forwarding headers unless configured", func(t *testing.T) {
		i := &Identity{}
		r := httptest.NewRequest("GET", "http://example/ws", nil)
		r.RemoteAddr = "198.51.100.10:7890"
		r.Header.Set("X-Forwarded-For", "203.0.113.50")

		if got := i.ClientSourceIP(r); got != "198.51.100.10" {
			t.Fatalf("ClientSourceIP() = %q, want %q", got, "198.51.100.10")
		}
	})

	t.Run("uses configured header when valid", func(t *testing.T) {
		i := &Identity{clientIPHeader: "CF-Connecting-IP"}
		r := httptest.NewRequest("GET", "http://example/ws", nil)
		r.RemoteAddr = "10.0.0.1:3456"
		r.Header.Set("CF-Connecting-IP", "203.0.113.50")

		if got := i.ClientSourceIP(r); got != "203.0.113.50" {
			t.Fatalf("ClientSourceIP() = %q, want %q", got, "203.0.113.50")
		}
	})

	t.Run("falls back to remote ip on invalid header", func(t *testing.T) {
		i := &Identity{clientIPHeader: "X-Real-IP"}
		r := httptest.NewRequest("GET", "http://example/ws", nil)
		r.RemoteAddr = "198.51.100.10:7890"
		r.Header.Set("X-Real-IP", "not-an-ip")

		if got := i.ClientSourceIP(r); got != "198.51.100.10" {
			t.Fatalf("ClientSourceIP() = %q, want %q", got, "198.51.100.10")
		}
	})

	t.Run("parses XFF list from configured header", func(t *testing.T) {
		i := &Identity{clientIPHeader: "X-Forwarded-For"}
		r := httptest.NewRequest("GET", "http://example/ws", nil)
		r.RemoteAddr = "10.0.0.1:3456"
		r.Header.Set("X-Forwarded-For", "203.0.113.50, 10.0.0.1")

		if got := i.ClientSourceIP(r); got != "203.0.113.50" {
			t.Fatalf("ClientSourceIP() = %q, want %q", got, "203.0.113.50")
		}
	})

	t.Run("handles bracketed ipv6", func(t *testing.T) {
		i := &Identity{}
		r := httptest.NewRequest("GET", "http://example/ws", nil)
		r.RemoteAddr = "[2001:db8::7]:443"

		if got := i.ClientSourceIP(r); got != "2001:db8::7" {
			t.Fatalf("ClientSourceIP() = %q, want %q", got, "2001:db8::7")
		}
	})
}
