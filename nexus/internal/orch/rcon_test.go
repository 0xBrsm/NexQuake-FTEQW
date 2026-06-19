package orch

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fakeServerWriteSentinels reads the framed command Nexus wrote to the PTY,
// extracts the BEGIN/END markers, and replays them around the supplied response
// lines on the console's publish path. Tests use this to stand in for the
// dedicated server's output without depending on real timing.
//
// Safe to call from a test goroutine — uses t.Errorf rather than t.Fatalf to
// avoid go vet's non-test-goroutine warning.
func fakeServerWriteSentinels(t *testing.T, ptyRead *os.File, console *serverConsole, responseLines ...string) string {
	t.Helper()
	buf := make([]byte, 512)
	n, err := ptyRead.Read(buf)
	if err != nil {
		t.Errorf("read pty: %v", err)
		return ""
	}
	written := string(buf[:n])
	begin, end := extractConsoleSentinelsFromWrite(written)
	if begin == "" || end == "" {
		t.Errorf("expected sentinel markers in framed command, got %q", written)
		return written
	}
	console.publishLine(begin + "\n")
	for _, line := range responseLines {
		console.publishLine(line)
	}
	console.publishLine(end + "\n")
	return written
}

func TestDispatchServerCmd_CapturesConsoleOutput(t *testing.T) {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = ptyRead.Close(); _ = ptyWrite.Close() })

	srv := &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(ptyWrite)}
	go fakeServerWriteSentinels(t, ptyRead, srv.Console, "sv_maxspeed is \"320\"\n")

	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := mgr.registerBareInstance(serverLaunch{Line: 0})
	mgr.updatePort(rec, 26000)
	rec.Running = srv

	reply, err := mgr.DispatchInstanceCmd(26000, "sv_maxspeed", "")
	if err != nil {
		t.Fatalf("DispatchInstanceCmd error = %v", err)
	}
	if !strings.Contains(reply, "sv_maxspeed is \"320\"") {
		t.Fatalf("expected captured server output, got %q", reply)
	}
	if strings.Contains(reply, "Command executed successfully.") {
		t.Fatalf("expected real output, got fallback reply %q", reply)
	}
	if strings.Contains(reply, consoleSentinelPrefix) {
		t.Fatalf("expected sentinel markers stripped from reply, got %q", reply)
	}
}

func TestDispatchServerCmd_FiltersNoisyConsoleOutput(t *testing.T) {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = ptyRead.Close(); _ = ptyWrite.Close() })

	srv := &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(ptyWrite)}
	go fakeServerWriteSentinels(t, ptyRead, srv.Console,
		"FindFile: maps/e1m1.bsp\n",
		"sv_maxspeed is \"320\"\n",
	)

	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := mgr.registerBareInstance(serverLaunch{Line: 0})
	mgr.updatePort(rec, 26000)
	rec.Running = srv

	reply, err := mgr.DispatchInstanceCmd(26000, "sv_maxspeed", "")
	if err != nil {
		t.Fatalf("DispatchInstanceCmd error = %v", err)
	}
	if strings.Contains(reply, "FindFile: maps/e1m1.bsp") {
		t.Fatalf("expected FindFile noise filtered from reply, got %q", reply)
	}
	if !strings.Contains(reply, "sv_maxspeed is \"320\"") {
		t.Fatalf("expected retained command output, got %q", reply)
	}
}

func TestFormatServerCommandAuditEcho_TrimsTrailingSemicolon(t *testing.T) {
	got := formatServerCommandAuditEcho("status;  ", "alice@example.com")
	want := `echo "alice@example.com: status"`
	if got != want {
		t.Fatalf("formatServerCommandAuditEcho()=%q want=%q", got, want)
	}
}

func TestDispatchServerCmd_FramesCommandWithAuditPreambleAndSentinels(t *testing.T) {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = ptyRead.Close(); _ = ptyWrite.Close() })

	srv := &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(ptyWrite)}
	wroteLine := make(chan string, 1)
	go func() {
		wroteLine <- fakeServerWriteSentinels(t, ptyRead, srv.Console, "host: ok\n")
	}()

	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := mgr.registerBareInstance(serverLaunch{Line: 0})
	mgr.updatePort(rec, 26000)
	rec.Running = srv

	reply, err := mgr.DispatchInstanceCmd(26000, "status;", "alice@example.com")
	if err != nil {
		t.Fatalf("DispatchInstanceCmd error = %v", err)
	}
	if !strings.Contains(reply, "host: ok") {
		t.Fatalf("expected command output reply, got %q", reply)
	}

	select {
	case got := <-wroteLine:
		if !strings.Contains(got, `echo "alice@example.com: status";`) {
			t.Fatalf("expected audit preamble in framed command, got %q", got)
		}
		if !strings.Contains(got, "echo "+consoleSentinelPrefix+"B_") {
			t.Fatalf("expected begin sentinel echo in framed command, got %q", got)
		}
		if !strings.Contains(got, "echo "+consoleSentinelPrefix+"E_") {
			t.Fatalf("expected end sentinel echo in framed command, got %q", got)
		}
		if !strings.Contains(got, "; status;") {
			t.Fatalf("expected user command preserved between sentinels, got %q", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("expected command write to pty")
	}
}

func TestDispatchServerCmd_SuppressesPtyEchoFromReply(t *testing.T) {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = ptyRead.Close(); _ = ptyWrite.Close() })

	srv := &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(ptyWrite)}
	go func() {
		buf := make([]byte, 512)
		n, _ := ptyRead.Read(buf)
		written := string(buf[:n])
		// Replay the verbatim PTY echo of the framed command — the
		// suppressed-relay-echo map should swallow it before subscribers see it.
		srv.Console.publishLine(written)
		begin, end := extractConsoleSentinelsFromWrite(written)
		srv.Console.publishLine(begin + "\n")
		srv.Console.publishLine("host: delayed-response\n")
		srv.Console.publishLine(end + "\n")
	}()

	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := mgr.registerBareInstance(serverLaunch{Line: 0})
	mgr.updatePort(rec, 26000)
	rec.Running = srv

	reply, err := mgr.DispatchInstanceCmd(26000, "status", "")
	if err != nil {
		t.Fatalf("DispatchInstanceCmd error = %v", err)
	}
	if strings.Contains(reply, "echo "+consoleSentinelPrefix) {
		t.Fatalf("expected framed-command echo suppressed from reply, got %q", reply)
	}
	if !strings.Contains(reply, "host: delayed-response") {
		t.Fatalf("expected command output to be captured, got %q", reply)
	}
}

func TestDispatchServerCmd_TailDefaultsToLastTenLines(t *testing.T) {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	srv := &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(nil)}
	for i := 1; i <= 12; i++ {
		srv.Console.publishLine(fmt.Sprintf("line %02d\n", i))
	}

	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := mgr.registerBareInstance(serverLaunch{Line: 0})
	mgr.updatePort(rec, 26000)
	rec.Running = srv

	reply, err := mgr.DispatchInstanceCmd(26000, "tail", "")
	if err != nil {
		t.Fatalf("DispatchInstanceCmd(tail) error = %v", err)
	}
	if strings.Contains(reply, "line 01") || strings.Contains(reply, "line 02") {
		t.Fatalf("expected tail to skip first two lines, got %q", reply)
	}
	if !strings.Contains(reply, "line 03") || !strings.Contains(reply, "line 12") {
		t.Fatalf("expected tail to include lines 03..12, got %q", reply)
	}
}

func TestDispatchServerCmd_TailUsesFilteredOutput(t *testing.T) {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	srv := &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(nil)}
	srv.Console.publishLine("line a\n")
	srv.Console.publishLine("FindFile: maps/e1m1.bsp\n")
	srv.Console.publishLine("PackFile: id1/pak0.pak : maps/e1m1.bsp\n")
	srv.Console.publishLine("FindFile: can't find progs.dat\n")
	srv.Console.publishLine("line b\n")

	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := mgr.registerBareInstance(serverLaunch{Line: 0})
	mgr.updatePort(rec, 26000)
	rec.Running = srv

	reply, err := mgr.DispatchInstanceCmd(26000, "tail", "")
	if err != nil {
		t.Fatalf("DispatchInstanceCmd(tail) error = %v", err)
	}
	if strings.Contains(reply, "FindFile: maps/e1m1.bsp") || strings.Contains(reply, "PackFile: id1/pak0.pak") {
		t.Fatalf("expected noisy lines filtered from tail reply, got %q", reply)
	}
	if !strings.Contains(reply, "FindFile: can't find progs.dat") || !strings.Contains(reply, "line b") {
		t.Fatalf("expected retained tail lines, got %q", reply)
	}
}

func TestDispatchServerCmd_TailUsageError(t *testing.T) {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	srv := &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(nil)}
	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := mgr.registerBareInstance(serverLaunch{Line: 0})
	mgr.updatePort(rec, 26000)
	rec.Running = srv

	_, err = mgr.DispatchInstanceCmd(26000, "tail 5", "")
	if err == nil {
		t.Fatalf("expected tail usage error")
	}
	if !strings.Contains(err.Error(), "usage: rcon <host|port|idx> tail") {
		t.Fatalf("expected tail usage error, got %q", err.Error())
	}
}

func TestDispatchServerCmd_NoOutputUsesSuccessFallback(t *testing.T) {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	ptyRead, ptyWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = ptyRead.Close(); _ = ptyWrite.Close() })

	srv := &managedServer{Cmd: &exec.Cmd{Process: process}, Console: newServerConsole(ptyWrite)}
	go fakeServerWriteSentinels(t, ptyRead, srv.Console)

	mgr := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)
	rec := mgr.registerBareInstance(serverLaunch{Line: 0})
	mgr.updatePort(rec, 26000)
	rec.Running = srv

	reply, err := mgr.DispatchInstanceCmd(26000, "status", "")
	if err != nil {
		t.Fatalf("DispatchInstanceCmd error = %v", err)
	}
	if reply != "Command executed successfully.\n" {
		t.Fatalf("expected success fallback reply, got %q", reply)
	}
}
