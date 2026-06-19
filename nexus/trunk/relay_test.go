package trunk

import (
	"context"
	"testing"
)

func TestBuildFrame(t *testing.T) {
	frame := buildFrame(26000, []byte{1, 2, 3})
	if len(frame) != 5 {
		t.Fatalf("buildFrame() len = %d, want 5", len(frame))
	}
	if frame[0] != byte(26000>>8) || frame[1] != byte(26000&0xff) {
		t.Fatalf("unexpected port header: [%d %d]", frame[0], frame[1])
	}
	if frame[2] != 1 || frame[3] != 2 || frame[4] != 3 {
		t.Fatalf("unexpected payload bytes: %v", frame[2:])
	}

	if frame := buildFrame(-1, nil); frame != nil {
		t.Fatalf("buildFrame(-1) should be nil")
	}
	if frame := buildFrame(70000, nil); frame != nil {
		t.Fatalf("buildFrame(70000) should be nil")
	}
}

func TestDecodeFrame(t *testing.T) {
	if _, _, ok := decodeFrame([]byte{1}); ok {
		t.Fatalf("decodeFrame() should reject short packets")
	}

	frame := buildFrame(26000, []byte{9, 8, 7})
	port, payload, ok := decodeFrame(frame)
	if !ok {
		t.Fatalf("decodeFrame() returned ok=false for valid frame")
	}
	if port != 26000 {
		t.Fatalf("decoded port = %d, want 26000", port)
	}
	if string(payload) != string([]byte{9, 8, 7}) {
		t.Fatalf("decoded payload = %v, want [9 8 7]", payload)
	}
}

func TestConnHandleFrame_ControlDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	called := false
	tk := &Trunk{
		onCtrlFrame: func(s *Session, payload []byte) {
			called = true
			if string(payload) != "ping" {
				t.Fatalf("control payload = %q, want %q", string(payload), "ping")
			}
			_ = s.SendControl([]byte("pong"))
		},
	}
	r := &Session{
		tx:     make(chan []byte, 1),
		ctx:    ctx,
		cancel: cancel,
		trunk:  tk,
	}

	r.handleFrame(buildFrame(controlPort, []byte("ping")))
	if !called {
		t.Fatalf("HandleControlFrame callback was not called")
	}

	select {
	case frame := <-r.tx:
		if frame[0] != byte(controlPort>>8) || frame[1] != byte(controlPort) {
			t.Fatalf("response frame routed to non-zero port: [%d %d]", frame[0], frame[1])
		}
		if string(frame[2:]) != "pong" {
			t.Fatalf("response payload = %q, want %q", string(frame[2:]), "pong")
		}
	default:
		t.Fatalf("expected control response frame")
	}
}
