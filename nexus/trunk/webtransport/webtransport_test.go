package webtransport

import (
	"errors"
	"fmt"
	"testing"

	"github.com/quic-go/quic-go"
)

func TestClassifyWriteError(t *testing.T) {
	var tooLarge uint64

	if err := classifyWriteError(nil, 100, &tooLarge); err != nil {
		t.Fatalf("nil error: got %v, want nil", err)
	}
	if tooLarge != 0 {
		t.Fatalf("nil error bumped counter to %d", tooLarge)
	}

	fatal := errors.New("connection closed")
	if err := classifyWriteError(fatal, 100, &tooLarge); err != fatal {
		t.Fatalf("fatal error: got %v, want %v", err, fatal)
	}

	oversized := &quic.DatagramTooLargeError{MaxDatagramPayloadSize: 1000}
	if err := classifyWriteError(oversized, 1034, &tooLarge); err != nil {
		t.Fatalf("oversized: got %v, want nil (dropped)", err)
	}
	if tooLarge != 1 {
		t.Fatalf("oversized drop counter = %d, want 1", tooLarge)
	}

	// http3/webtransport layers may wrap the error; errors.Is must still match.
	wrapped := fmt.Errorf("send: %w", oversized)
	if err := classifyWriteError(wrapped, 1034, &tooLarge); err != nil {
		t.Fatalf("wrapped oversized: got %v, want nil (dropped)", err)
	}
	if tooLarge != 2 {
		t.Fatalf("wrapped oversized drop counter = %d, want 2", tooLarge)
	}
}
