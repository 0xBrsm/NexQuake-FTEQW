package assets

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func writeTestPak(t *testing.T, pakPath string, files map[string][]byte) {
	t.Helper()

	type dirEnt struct {
		name   string
		offset int32
		size   int32
	}

	var data bytes.Buffer
	data.Grow(12)
	data.Write(make([]byte, 12)) // header placeholder

	entries := make([]dirEnt, 0, len(files))
	for name, content := range files {
		off := int32(data.Len())
		data.Write(content)
		entries = append(entries, dirEnt{name: name, offset: off, size: int32(len(content))})
	}

	dirOffset := int32(data.Len())
	for _, e := range entries {
		nameBytes := make([]byte, 56)
		copy(nameBytes, []byte(e.name))
		data.Write(nameBytes)

		var tmp [8]byte
		binary.LittleEndian.PutUint32(tmp[0:4], uint32(e.offset))
		binary.LittleEndian.PutUint32(tmp[4:8], uint32(e.size))
		data.Write(tmp[:])
	}
	dirLen := int32(len(entries) * 64)

	out := data.Bytes()
	copy(out[0:4], []byte("PACK"))
	binary.LittleEndian.PutUint32(out[4:8], uint32(dirOffset))
	binary.LittleEndian.PutUint32(out[8:12], uint32(dirLen))

	if err := os.WriteFile(pakPath, out, 0o644); err != nil {
		t.Fatalf("write pak: %v", err)
	}
}

func TestReadPakContents(t *testing.T) {
	tmp := t.TempDir()
	pakPath := filepath.Join(tmp, "pak0.pak")

	writeTestPak(t, pakPath, map[string][]byte{
		"gfx/palette.lmp": {0x01, 0x02, 0x03},
		"progs.dat":       []byte("abcd"),
	})

	entries, err := readPakContents(pakPath)
	if err != nil {
		t.Fatalf("readPakContents: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	byName := map[string]pakFileEntry{}
	for _, e := range entries {
		byName[e.name] = e
	}
	if e, ok := byName["gfx/palette.lmp"]; !ok || e.size != 3 {
		t.Fatalf("missing gfx/palette.lmp or wrong size: %+v", e)
	}
	if e, ok := byName["progs.dat"]; !ok || e.size != 4 {
		t.Fatalf("missing progs.dat or wrong size: %+v", e)
	}
}
