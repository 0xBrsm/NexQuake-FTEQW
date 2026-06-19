package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestApplyOperatorConsoleTimestampEnv(t *testing.T) {
	t.Run("accepts 1", func(t *testing.T) {
		t.Setenv("CONSOLE_TIMESTAMPS", "1")
		operatorConsoleTimestamp.Store(false)
		applyOperatorConsoleTimestampEnv()
		if !operatorConsoleTimestampsEnabled() {
			t.Fatalf("expected operator console timestamps enabled for 1")
		}
	})

	t.Run("accepts 0", func(t *testing.T) {
		t.Setenv("CONSOLE_TIMESTAMPS", "0")
		operatorConsoleTimestamp.Store(true)
		applyOperatorConsoleTimestampEnv()
		if operatorConsoleTimestampsEnabled() {
			t.Fatalf("expected operator console timestamps disabled for 0")
		}
	})

	t.Run("rejects non 0-1 values", func(t *testing.T) {
		t.Setenv("CONSOLE_TIMESTAMPS", "on")
		operatorConsoleTimestamp.Store(false)
		applyOperatorConsoleTimestampEnv()
		if !operatorConsoleTimestampsEnabled() {
			t.Fatalf("expected default enabled state for invalid value")
		}
	})
}

func TestGetEnvBool01(t *testing.T) {
	t.Run("uses default when unset", func(t *testing.T) {
		t.Setenv("CL_SMENU", "")
		if getEnvBool01("CL_SMENU", true) != true {
			t.Fatalf("expected default true when unset")
		}
		if getEnvBool01("CL_SMENU", false) != false {
			t.Fatalf("expected default false when unset")
		}
	})

	t.Run("accepts 1", func(t *testing.T) {
		t.Setenv("CL_SMENU", "1")
		if !getEnvBool01("CL_SMENU", false) {
			t.Fatalf("expected true for 1")
		}
	})

	t.Run("accepts 0", func(t *testing.T) {
		t.Setenv("CL_SMENU", "0")
		if getEnvBool01("CL_SMENU", true) {
			t.Fatalf("expected false for 0")
		}
	})

	t.Run("invalid uses default", func(t *testing.T) {
		t.Setenv("CL_SMENU", "on")
		if !getEnvBool01("CL_SMENU", true) {
			t.Fatalf("expected true default for invalid")
		}
		if getEnvBool01("CL_SMENU", false) {
			t.Fatalf("expected false default for invalid")
		}
	})
}

func TestGetEnvIntMin(t *testing.T) {
	t.Run("accepts zero when min is zero", func(t *testing.T) {
		t.Setenv("CL_CONCURRENCY", "0")
		if got := getEnvIntMin("CL_CONCURRENCY", 16, 0); got != 0 {
			t.Fatalf("getEnvIntMin()=%d want=0", got)
		}
	})

	t.Run("rejects below-min values", func(t *testing.T) {
		t.Setenv("CL_CONCURRENCY", "-1")
		if got := getEnvIntMin("CL_CONCURRENCY", 16, 0); got != 16 {
			t.Fatalf("getEnvIntMin()=%d want=16", got)
		}
	})
}

func TestGetEnvArgs(t *testing.T) {
	t.Run("uses default when unset", func(t *testing.T) {
		t.Setenv("CL_SEND_ARGS", "")
		want := []string{"-nosound", "+skill", "3"}
		got := getEnvArgs("CL_SEND_ARGS", want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("getEnvArgs()=%v want=%v", got, want)
		}
	})

	t.Run("parses shell args with plus commands", func(t *testing.T) {
		t.Setenv("CL_SEND_ARGS", "-nosound +skill 3 +exec autoexec.cfg")
		want := []string{"-nosound", "+skill", "3", "+exec", "autoexec.cfg"}
		got := getEnvArgs("CL_SEND_ARGS", nil)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("getEnvArgs()=%v want=%v", got, want)
		}
	})

	t.Run("keeps quoted values", func(t *testing.T) {
		t.Setenv("CL_SEND_ARGS", `+name "Player One" +skill 3`)
		want := []string{"+name", "Player One", "+skill", "3"}
		got := getEnvArgs("CL_SEND_ARGS", nil)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("getEnvArgs()=%v want=%v", got, want)
		}
	})

	t.Run("invalid value uses default", func(t *testing.T) {
		t.Setenv("CL_SEND_ARGS", `"unterminated`)
		want := []string{"-window"}
		got := getEnvArgs("CL_SEND_ARGS", want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("getEnvArgs()=%v want=%v", got, want)
		}
	})
}

func clearRuntimeConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"HTTP_PORT",
		"GAME_DIR",
		"CFG_DIR",
		"CD_DIR",
		"LOGS_DIR",
		"BIN_DIR",
		"SERVER_DIR",
		"CLIENT_DIR",
		"CL_CONCURRENCY",
		"CL_SMENU",
		"CL_ARGS",
		"CL_URL_ARGS",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadRuntimeConfig_DefaultClientAndServerDirs(t *testing.T) {
	clearRuntimeConfigEnv(t)

	cfg := loadRuntimeConfig()
	if cfg.serverBinDir != "/app/server" {
		t.Fatalf("serverBinDir=%q want %q", cfg.serverBinDir, "/app/server")
	}
	if cfg.clientDir != "/app/bin/nqwasm" {
		t.Fatalf("clientDir=%q want %q", cfg.clientDir, "/app/bin/nqwasm")
	}
}

func TestLoadRuntimeConfig_ServerDirOverridesDefault(t *testing.T) {
	clearRuntimeConfigEnv(t)
	t.Setenv("SERVER_DIR", "/srv/new")

	cfg := loadRuntimeConfig()
	if cfg.serverBinDir != "/srv/new" {
		t.Fatalf("serverBinDir=%q want %q", cfg.serverBinDir, "/srv/new")
	}
}

func TestAuditf_WritesToNexusLogAndTailRegardlessLogLevel(t *testing.T) {
	oldLevel := currentLogLevel
	currentLogLevel = logInfo
	t.Cleanup(func() { currentLogLevel = oldLevel })

	resetNexusLogHistoryForTest()

	logDir := t.TempDir()
	if err := configureNexusLogFile(logDir); err != nil {
		t.Fatalf("configureNexusLogFile() error = %v", err)
	}
	t.Cleanup(closeNexusLogFile)

	auditf("admin-rcon request actor=%q target=%s command=%q", "alice@example.com", "26000", "status")

	lines := tailNexusLogLines(1)
	if len(lines) != 1 || !strings.Contains(lines[0], "admin-rcon request") {
		t.Fatalf("expected audit line in tail, got %v", lines)
	}

	data, err := os.ReadFile(filepath.Join(logDir, "nexus.log"))
	if err != nil {
		t.Fatalf("ReadFile(nexus.log): %v", err)
	}
	if !strings.Contains(string(data), "admin-rcon request") {
		t.Fatalf("expected audit line written to nexus.log, got %q", string(data))
	}
}

func resetNexusLogHistoryForTest() {
	nexusLogHistoryMu.Lock()
	defer nexusLogHistoryMu.Unlock()
	nexusLogHistory = nil
}
