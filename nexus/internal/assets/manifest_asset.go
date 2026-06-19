package assets

import (
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"
)

type sectionReadSeekCloser struct {
	io.ReadSeeker
	closeFn func() error
}

func (s *sectionReadSeekCloser) Close() error {
	return s.closeFn()
}

type hashedAsset struct {
	name        string
	modTime     time.Time
	contentType string
	open        func() (io.ReadSeekCloser, error)
}

func hashedAssetFromEntry(ent assetManifestEntry) (hashedAsset, error) {
	backing := ent.backing
	statPath := backing.filePath
	name := path.Base(backing.filePath)
	open := func() (io.ReadSeekCloser, error) {
		return os.Open(backing.filePath)
	}

	if backing.pakPath != "" {
		statPath = backing.pakPath
		entry := backing.pakEntry
		name = path.Base(entry.name)
		open = func() (io.ReadSeekCloser, error) {
			f, openErr := os.Open(backing.pakPath)
			if openErr != nil {
				return nil, openErr
			}
			section := io.NewSectionReader(f, entry.offset, entry.size)
			return &sectionReadSeekCloser{
				ReadSeeker: section,
				closeFn:    f.Close,
			}, nil
		}
	}

	st, err := os.Stat(statPath)
	if err != nil {
		return hashedAsset{}, err
	}
	if backing.pakPath == "" && st.IsDir() {
		return hashedAsset{}, fmt.Errorf("expected file, got directory: %s", statPath)
	}
	if name == "" {
		name = ent.Path
	}

	return hashedAsset{
		name:        name,
		modTime:     st.ModTime(),
		contentType: contentTypeForAsset(name),
		open:        open,
	}, nil
}

func contentTypeForAsset(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".ogg":
		return "audio/ogg"
	case ".mp3":
		return "audio/mpeg"
	case ".pak", ".data":
		return "application/octet-stream"
	default:
		return ""
	}
}
