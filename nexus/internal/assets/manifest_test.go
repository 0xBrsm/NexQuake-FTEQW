package assets

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashedAssetServer_IssueAndAssetFetch(t *testing.T) {
	gameDir := t.TempDir()
	cdDir := t.TempDir()

	commonDir := filepath.Join(gameDir, "id1", "common")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "foo.txt"), []byte("hello data"), 0o644); err != nil {
		t.Fatalf("write data file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cdDir, "02-intro.ogg"), []byte("hello cd"), 0o644); err != nil {
		t.Fatalf("write cd file: %v", err)
	}

	gateway := NewHashedAssetServer(gameDir, cdDir, NewPakIndexCache())

	game, cd, ref, err := gateway.IssueManifest()
	if err != nil {
		t.Fatalf("IssueManifest: %v", err)
	}
	if ref == "" {
		t.Fatalf("expected non-empty ref")
	}
	if len(game["id1"]) == 0 {
		t.Fatalf("id1 manifest missing entries: %+v", game)
	}

	hasPath := func(entries []StartManifestEntry, path string) bool {
		for _, entry := range entries {
			if entry.Path == path {
				return true
			}
		}
		return false
	}

	if !hasPath(game["id1"], "foo.txt") {
		t.Fatalf("foo.txt path missing from issued manifest: %+v", game["id1"])
	}
	if !hasPath(cd, "02-intro.ogg") {
		t.Fatalf("cd track path missing from issued manifest: %+v", cd)
	}

	fooHash := hashAssetKey(ref, "mod:id1:"+normalizeVFSKey("foo.txt"))
	cdHash := hashAssetKey(ref, "cd:"+normalizeVFSKey("02-intro.ogg"))

	dataReq := httptest.NewRequest(http.MethodGet, "/nq/"+fooHash, nil)
	dataRec := httptest.NewRecorder()
	gateway.AssetHandler().ServeHTTP(dataRec, dataReq)
	if dataRec.Code != http.StatusOK {
		t.Fatalf("data fetch status=%d body=%q", dataRec.Code, dataRec.Body.String())
	}
	if got := dataRec.Body.String(); got != "hello data" {
		t.Fatalf("data fetch body=%q want=%q", got, "hello data")
	}

	cdReq := httptest.NewRequest(http.MethodGet, "/nq/"+cdHash, nil)
	cdRec := httptest.NewRecorder()
	gateway.AssetHandler().ServeHTTP(cdRec, cdReq)
	if cdRec.Code != http.StatusOK {
		t.Fatalf("cd fetch status=%d body=%q", cdRec.Code, cdRec.Body.String())
	}
	if got := cdRec.Header().Get("Content-Type"); !strings.HasPrefix(got, "audio/ogg") {
		t.Fatalf("cd content-type=%q, expected audio/ogg", got)
	}
	if got := cdRec.Body.String(); got != "hello cd" {
		t.Fatalf("cd fetch body=%q want=%q", got, "hello cd")
	}
}

func TestHashedAssetServer_RangeRequests(t *testing.T) {
	gameDir := t.TempDir()
	commonDir := filepath.Join(gameDir, "id1", "common")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := []byte("abcdef")
	if err := os.WriteFile(filepath.Join(commonDir, "foo.bin"), content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	gateway := NewHashedAssetServer(gameDir, filepath.Join(t.TempDir(), "missing"), NewPakIndexCache())

	game, _, ref, err := gateway.IssueManifest()
	if err != nil {
		t.Fatalf("IssueManifest: %v", err)
	}
	if len(game["id1"]) == 0 {
		t.Fatalf("expected id1 manifest entries")
	}
	hash := hashAssetKey(ref, "mod:id1:"+normalizeVFSKey(game["id1"][0].Path))

	rangeReq := httptest.NewRequest(http.MethodGet, "/nq/"+hash, nil)
	rangeReq.Header.Set("Range", "bytes=1-3")
	rangeRec := httptest.NewRecorder()
	gateway.AssetHandler().ServeHTTP(rangeRec, rangeReq)
	if rangeRec.Code != http.StatusPartialContent {
		t.Fatalf("range status=%d body=%q", rangeRec.Code, rangeRec.Body.String())
	}
	got, err := io.ReadAll(rangeRec.Result().Body)
	if err != nil {
		t.Fatalf("read range body: %v", err)
	}
	if string(got) != "bcd" {
		t.Fatalf("range body=%q want=%q", string(got), "bcd")
	}
}

func TestHashedAssetServer_IssueIgnoresCorruptPakInOtherMod(t *testing.T) {
	gameDir := t.TempDir()
	id1CommonDir := filepath.Join(gameDir, "id1", "common")
	if err := os.MkdirAll(id1CommonDir, 0o755); err != nil {
		t.Fatalf("mkdir id1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(id1CommonDir, "config.cfg"), []byte("echo id1"), 0o644); err != nil {
		t.Fatalf("write id1: %v", err)
	}

	otherCommonDir := filepath.Join(gameDir, "ctf", "common")
	if err := os.MkdirAll(otherCommonDir, 0o755); err != nil {
		t.Fatalf("mkdir ctf: %v", err)
	}
	corrupt := []byte{
		'P', 'A', 'C', 'K',
		0x64, 0x00, 0x00, 0x00, // dir offset = 100
		0x40, 0x00, 0x00, 0x00, // dir length = 64
	}
	if err := os.WriteFile(filepath.Join(otherCommonDir, "pak0.pak"), corrupt, 0o644); err != nil {
		t.Fatalf("write corrupt pak: %v", err)
	}

	gateway := NewHashedAssetServer(gameDir, filepath.Join(t.TempDir(), "missing"), NewPakIndexCache())

	var errorLogs []string
	prev := slog.Default()
	slog.SetDefault(slog.New(captureHandler{entries: &errorLogs}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	game, _, _, err := gateway.IssueManifest()
	if err != nil {
		t.Fatalf("IssueManifest: %v", err)
	}
	if len(game["id1"]) == 0 {
		t.Fatalf("expected id1 entries, got=%+v", game)
	}
	if len(errorLogs) == 0 {
		t.Fatalf("expected gateway to log corrupt pak")
	}
	if !strings.Contains(errorLogs[0], "pak0.pak") {
		t.Fatalf("expected logged path to include pak filename, got=%q", errorLogs[0])
	}
}
