package main

import (
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/google/shlex"
)

// runtimeConfig holds the cross-cutting values derived from environment
// variables at startup. Transport-specific knobs live with their transport
// setup functions, not here.
type runtimeConfig struct {
	httpPort               string
	gameDir                string
	cfgDir                 string
	cdDir                  string
	logsDir                string
	binDir                 string
	serverBinDir           string
	clientDir              string
	vfsPrefetchConcurrency int
	clientAutoSMenu        bool
	clientSendArgs         []string
	clientURLArgs          bool
	serverMaxInstances     int
}

// loadRuntimeConfig reads cross-cutting environment variables once and
// returns the resolved configuration.
func loadRuntimeConfig() runtimeConfig {
	return runtimeConfig{
		httpPort:               getEnv("HTTP_PORT", "1337"),
		gameDir:                getEnv("GAME_DIR", "/app/game"),
		cfgDir:                 getEnv("CFG_DIR", "/app/etc"),
		cdDir:                  getEnv("CD_DIR", "/app/cd"),
		logsDir:                getEnv("LOGS_DIR", "/app/logs"),
		binDir:                 getEnv("BIN_DIR", "/app/bin"),
		serverBinDir:           getEnv("SERVER_DIR", "/app/server"),
		clientDir:              getEnv("CLIENT_DIR", "/app/bin/nqwasm"),
		vfsPrefetchConcurrency: getEnvIntMin("CL_CONCURRENCY", 16, 0),
		clientAutoSMenu:        getEnvBool01("CL_SMENU", false),
		clientSendArgs:         getEnvArgs("CL_ARGS", nil),
		clientURLArgs:          getEnvBool01("CL_URL_ARGS", true),
		serverMaxInstances:     getEnvIntMin("SV_MAX_INSTANCES", 1, 1),
	}
}

// prependPath adds dir to the front of PATH if it is not already present.
// It is a no-op for empty strings.
func prependPath(binDir string) error {
	dir := strings.TrimSpace(binDir)
	if dir == "" {
		return nil
	}
	existing := os.Getenv("PATH")
	sep := string(os.PathListSeparator)
	if slices.Contains(strings.Split(existing, sep), dir) {
		return nil
	}
	if existing == "" {
		return os.Setenv("PATH", dir)
	}
	return os.Setenv("PATH", dir+sep+existing)
}

// getEnv returns the value of key, or defaultValue when the variable is unset or empty.
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvIntMin parses key as an integer. Returns defaultValue when the variable
// is unset, non-integer, or below minValue, and warns in those last two cases.
func getEnvIntMin(key string, defaultValue, minValue int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minValue {
		slog.Warn(fmt.Sprintf("Invalid %s=%q (expected integer >= %d); using %d", key, raw, minValue, defaultValue))
		return defaultValue
	}
	return value
}

// getEnvBool01 parses key as "0" or "1". Returns defaultValue when the variable
// is unset or not a recognised value, and warns in the latter case.
func getEnvBool01(key string, defaultValue bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	switch raw {
	case "1":
		return true
	case "0":
		return false
	default:
		slog.Warn(fmt.Sprintf("Invalid %s=%q (expected 0|1); using default (%t)", key, raw, defaultValue))
		return defaultValue
	}
}

// getEnvArgs parses key as shell-style arguments using shlex. Returns a copy of
// defaultValue when the variable is unset, empty, or contains a parse error.
func getEnvArgs(key string, defaultValue []string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return slices.Clone(defaultValue)
	}
	parsed, err := shlex.Split(raw)
	if err != nil {
		slog.Warn(fmt.Sprintf("Invalid %s=%q (expected shell-style args); using default", key, raw))
		return slices.Clone(defaultValue)
	}
	return parsed
}
