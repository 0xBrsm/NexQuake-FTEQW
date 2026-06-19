package admin

import (
	"testing"
)

// --- parseServerTargetToken -------------------------------------------------

func TestParseServerTargetToken_Index(t *testing.T) {
	idx, all, err := parseServerTargetToken("3")
	if err != nil || all || idx != 3 {
		t.Fatalf("got idx=%d all=%v err=%v", idx, all, err)
	}
}

func TestParseServerTargetToken_All(t *testing.T) {
	idx, all, err := parseServerTargetToken("all")
	if err != nil || !all || idx != 0 {
		t.Fatalf("got idx=%d all=%v err=%v", idx, all, err)
	}
}

func TestParseServerTargetToken_AllIgnoresCase(t *testing.T) {
	_, all, err := parseServerTargetToken("ALL")
	if err != nil || !all {
		t.Fatalf("expected ALL to be accepted, got all=%v err=%v", all, err)
	}
}

func TestParseServerTargetToken_RejectsEmpty(t *testing.T) {
	_, _, err := parseServerTargetToken("")
	if _, ok := err.(*MethodError); !ok || err.(*MethodError).Code != ErrCodeInvalidParams {
		t.Fatalf("expected InvalidParams, got %v", err)
	}
}

func TestParseServerTargetToken_RejectsHostname(t *testing.T) {
	_, _, err := parseServerTargetToken("fragfest")
	if _, ok := err.(*MethodError); !ok || err.(*MethodError).Code != ErrCodeInvalidParams {
		t.Fatalf("expected InvalidParams for hostname, got %v", err)
	}
}

func TestParseServerTargetToken_RejectsNonPositive(t *testing.T) {
	_, _, err := parseServerTargetToken("0")
	if _, ok := err.(*MethodError); !ok || err.(*MethodError).Code != ErrCodeInvalidParams {
		t.Fatalf("expected InvalidParams for 0, got %v", err)
	}
}
