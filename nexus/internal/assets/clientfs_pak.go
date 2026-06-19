package assets

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sync"
)

// pakFileEntry describes a single file inside a Quake .pak archive.
type pakFileEntry struct {
	name   string
	offset int64
	size   int64
}

// readPakContents reads a Quake .pak file and returns directory entries.
// It validates offsets/sizes against the underlying file to avoid out-of-bounds reads.
func readPakContents(pakPath string) ([]pakFileEntry, error) {
	f, err := os.Open(pakPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	fileSize := st.Size()
	if fileSize < 12 {
		return nil, fmt.Errorf("pak too small: %d", fileSize)
	}

	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return nil, err
	}

	if string(hdr[0:4]) != "PACK" {
		return nil, fmt.Errorf("invalid pak magic: %q", string(hdr[0:4]))
	}

	dirOffset := int64(int32(binary.LittleEndian.Uint32(hdr[4:8])))
	dirLen := int64(int32(binary.LittleEndian.Uint32(hdr[8:12])))
	if dirOffset < 0 || dirLen < 0 {
		return nil, fmt.Errorf("invalid pak directory offset/len: %d/%d", dirOffset, dirLen)
	}
	if dirOffset+dirLen > fileSize {
		return nil, fmt.Errorf("pak directory out of bounds: off=%d len=%d size=%d", dirOffset, dirLen, fileSize)
	}
	if dirLen%64 != 0 {
		return nil, fmt.Errorf("invalid pak directory length: %d (not multiple of 64)", dirLen)
	}

	if _, err := f.Seek(dirOffset, io.SeekStart); err != nil {
		return nil, err
	}

	dirBytes := make([]byte, dirLen)
	if _, err := io.ReadFull(f, dirBytes); err != nil {
		return nil, err
	}

	out := make([]pakFileEntry, 0, dirLen/64)
	for i := int64(0); i < dirLen; i += 64 {
		chunk := dirBytes[i : i+64]
		rawName := chunk[0:56]
		if nul := bytes.IndexByte(rawName, 0); nul >= 0 {
			rawName = rawName[:nul]
		}
		originalName := string(rawName)
		if originalName == "" {
			continue
		}
		name, err := cleanVFSPath(originalName)
		if err != nil {
			return nil, fmt.Errorf("invalid pak entry name %q: %w", originalName, err)
		}

		off := int64(int32(binary.LittleEndian.Uint32(chunk[56:60])))
		sz := int64(int32(binary.LittleEndian.Uint32(chunk[60:64])))
		if off < 0 || sz < 0 {
			return nil, fmt.Errorf("invalid entry offset/size for %q: %d/%d", name, off, sz)
		}
		if off+sz > fileSize {
			return nil, fmt.Errorf("entry out of bounds for %q: off=%d size=%d file=%d", name, off, sz, fileSize)
		}

		out = append(out, pakFileEntry{name: name, offset: off, size: sz})
	}

	return out, nil
}

type pakIndex struct {
	modTimeUnixNano int64
	fileSize        int64
	entries         map[string]pakFileEntry // keyed by normalized (lowercased) name
}

// PakIndexCache is a thread-safe cache of parsed pak directory indices.
type PakIndexCache struct {
	mu     sync.Mutex
	byPath map[string]*pakIndex
}

// NewPakIndexCache creates a new pak index cache.
func NewPakIndexCache() *PakIndexCache {
	return &PakIndexCache{byPath: make(map[string]*pakIndex)}
}

// get returns a cached pak index, re-reading the pak if the file has changed.
func (c *PakIndexCache) get(pakPath string) (*pakIndex, error) {
	st, err := os.Stat(pakPath)
	if err != nil {
		return nil, err
	}
	mt := st.ModTime().UnixNano()
	sz := st.Size()

	c.mu.Lock()
	if existing := c.byPath[pakPath]; existing != nil && existing.modTimeUnixNano == mt && existing.fileSize == sz {
		c.mu.Unlock()
		return existing, nil
	}
	c.mu.Unlock()

	contents, err := readPakContents(pakPath)
	if err != nil {
		return nil, err
	}
	m := make(map[string]pakFileEntry, len(contents))
	for _, e := range contents {
		key := normalizeVFSKey(e.name)
		if key == "" {
			continue
		}
		m[key] = e
	}

	idx := &pakIndex{
		modTimeUnixNano: mt,
		fileSize:        sz,
		entries:         m,
	}

	c.mu.Lock()
	c.byPath[pakPath] = idx
	c.mu.Unlock()
	return idx, nil
}
