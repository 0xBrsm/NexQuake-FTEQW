package assets

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/quake106"
)

func installLayer(ctx context.Context, gameDir, game, layer string, sources []string, force bool) error {
	if !layerNeedsInstall(gameDir, game, layer, sources, force) {
		return nil
	}
	destRoot := filepath.Join(gameDir, game, layer)
	for j, urlStr := range sources {
		urlStr = strings.TrimSpace(urlStr)
		if urlStr == "" {
			return fmt.Errorf("%s[%d]: empty url for %s/%s", layer, j, game, layer)
		}
		if err := installFromSource(ctx, urlStr, destRoot); err != nil {
			return fmt.Errorf("failed to install %s/%s from %s: %w", game, layer, urlStr, err)
		}
	}
	return nil
}

func installFromSource(ctx context.Context, urlStr, destRoot string) error {
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return err
	}

	sourcePath, cleanup, err := materializeSource(ctx, urlStr)
	if err != nil {
		return err
	}
	defer cleanup()

	zr, err := zip.OpenReader(sourcePath)
	if err != nil {
		destPath := filepath.Join(destRoot, filepath.Base(strings.TrimSpace(urlStr)))
		src, err := os.Open(sourcePath)
		if err != nil {
			return err
		}
		defer src.Close()
		return copyToFile(destPath, src)
	}
	defer zr.Close()

	sum, err := sha256FileHex(sourcePath)
	if err != nil {
		return err
	}
	if sum == quake106.ZipSHA256 {
		return quake106.ExtractPak0(&zr.Reader, destRoot)
	}
	return extractZipGeneric(&zr.Reader, destRoot)
}

func materializeSource(ctx context.Context, urlStr string) (string, func(), error) {
	if strings.HasPrefix(urlStr, "file://") {
		return strings.TrimPrefix(urlStr, "file://"), func() {}, nil
	}
	return downloadToTemp(ctx, urlStr)
}

func downloadToTemp(ctx context.Context, urlStr string) (string, func(), error) {
	tmp, err := os.CreateTemp("", "nexquake-*.tmp")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	fail := func(err error) (string, func(), error) {
		_ = tmp.Close()
		cleanup()
		return "", nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return fail(err)
	}
	req.Header.Set("User-Agent", "nexquake-nexus")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fail(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return fail(fmt.Errorf("status %s", resp.Status))
	}
	defer resp.Body.Close()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	return tmp.Name(), cleanup, nil
}
