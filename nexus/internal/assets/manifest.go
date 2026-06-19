package assets

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultManifestTTL = 37 * time.Minute

// maxLiveManifests bounds outstanding per-session manifests. IssueManifest is
// unauthenticated, so without a cap a spammer accumulates one asset map per
// request for the full TTL. When the cap is hit the oldest session is
// evicted; legitimate concurrent page loads stay far below it.
const maxLiveManifests = 256

// StartManifestEntry is one entry in an issued asset manifest. The Path field
// is the logical asset path (e.g. "foo.txt"); the matching content-addressed
// hash is registered internally and served from /nq/<hash>.
type StartManifestEntry struct {
	Path string `json:"path"`
}

type issuedManifest struct {
	createdAt time.Time
	assets    map[string]hashedAsset
}

// HashedAssetServer issues per-session asset manifests and serves the
// content-addressed assets they reference. It owns asset state (mod tree
// scanning, hash issuance, TTL pruning) and nothing about HTTP framing or
// client/transport configuration.
type HashedAssetServer struct {
	gameDir     string
	cdDir       string
	pakCache    *PakIndexCache
	manifestTTL time.Duration

	mu           sync.RWMutex
	manifests    map[string]*issuedManifest
	assetsByHash map[string]hashedAsset
}

// NewHashedAssetServer creates a HashedAssetServer rooted at gameDir (the
// layered mod tree) and cdDir (CD-audio tracks; may be empty/missing). A
// nil pakCache gets a fresh one.
func NewHashedAssetServer(gameDir, cdDir string, pakCache *PakIndexCache) *HashedAssetServer {
	if pakCache == nil {
		pakCache = NewPakIndexCache()
	}
	return &HashedAssetServer{
		gameDir:      gameDir,
		cdDir:        cdDir,
		pakCache:     pakCache,
		manifestTTL:  defaultManifestTTL,
		manifests:    make(map[string]*issuedManifest),
		assetsByHash: make(map[string]hashedAsset),
	}
}

// IssueManifest scans the asset tree, registers a fresh per-session set of
// hash addresses for each asset, and returns the manifest entries plus the
// session ref. The hashes are valid until the session expires (manifestTTL,
// default 37 min). The caller owns HTTP framing.
//
// game is keyed by mod name. cd is empty when no CD tracks are configured.
// Both may be empty (no mods found) — the caller decides whether that is a
// 404 or some other shape.
func (g *HashedAssetServer) IssueManifest() (game map[string][]StartManifestEntry, cd []StartManifestEntry, ref string, err error) {
	ref = newManifestRef()
	game, cd, assets, err := g.buildSnapshot(ref)
	if err != nil {
		return nil, nil, "", err
	}
	if len(game) == 0 {
		return nil, nil, "", nil
	}

	now := time.Now()
	g.mu.Lock()
	g.pruneExpiredLocked(now)
	// The cap is enforced on every insert, so the map can be at most one
	// entry over after this single eviction.
	if len(g.manifests) >= maxLiveManifests {
		g.evictOldestLocked()
	}
	g.manifests[ref] = &issuedManifest{
		createdAt: now,
		assets:    assets,
	}
	for hash, asset := range assets {
		g.assetsByHash[hash] = asset
	}
	g.mu.Unlock()

	return game, cd, ref, nil
}

// AssetHandler returns an HTTP handler that serves individual assets by the
// per-session hash address issued by IssueManifest.
//
// The URL path must be /nq/<hash> where hash is a 16-character hex string.
// Supports GET and HEAD. Returns 404 for unknown or expired hashes.
func (g *HashedAssetServer) AssetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		hash := strings.TrimPrefix(r.URL.Path, "/nq/")
		hash = strings.TrimSpace(hash)
		if hash == "" || strings.Contains(hash, "/") {
			http.NotFound(w, r)
			return
		}

		asset, ok := g.lookupAsset(hash)
		if !ok {
			http.NotFound(w, r)
			return
		}

		start := time.Now()
		rc, err := asset.open()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer rc.Close()
		openDur := time.Since(start)

		if asset.contentType != "" {
			w.Header().Set("Content-Type", asset.contentType)
		}
		w.Header().Set("Cache-Control", "private, no-store")
		http.ServeContent(w, r, asset.name, asset.modTime, rc)

		// Clients report multi-second fetches for KB-sized assets; the split
		// says which side to blame. open covers storage; the rest of total is
		// ServeContent, which doesn't return until the body is written, so a
		// fast open with a slow total means the bytes sat in the network path
		// (tunnel/uplink/client), not in nexus.
		if total := time.Since(start); total > time.Second {
			slog.Warn(fmt.Sprintf("asset serve slow: %s open=%dms total=%dms remote=%s",
				asset.name, openDur.Milliseconds(), total.Milliseconds(), r.RemoteAddr))
		}
	}
}

func (g *HashedAssetServer) lookupAsset(hash string) (hashedAsset, bool) {
	g.mu.RLock()
	if asset, ok := g.assetsByHash[hash]; ok {
		g.mu.RUnlock()
		return asset, true
	}
	g.mu.RUnlock()

	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneExpiredLocked(time.Now())
	asset, ok := g.assetsByHash[hash]
	return asset, ok
}

func (g *HashedAssetServer) pruneExpiredLocked(now time.Time) {
	for ref, session := range g.manifests {
		if now.Sub(session.createdAt) > g.manifestTTL {
			g.removeManifestLocked(ref)
		}
	}
}

// evictOldestLocked removes the oldest live session to enforce
// maxLiveManifests. Callers hold g.mu.
func (g *HashedAssetServer) evictOldestLocked() {
	var oldestRef string
	var oldestAt time.Time
	for ref, session := range g.manifests {
		if oldestRef == "" || session.createdAt.Before(oldestAt) {
			oldestRef = ref
			oldestAt = session.createdAt
		}
	}
	g.removeManifestLocked(oldestRef)
}

// removeManifestLocked drops a session and its asset hashes together —
// manifests and assetsByHash must stay in sync on every removal path.
func (g *HashedAssetServer) removeManifestLocked(ref string) {
	session, ok := g.manifests[ref]
	if !ok {
		return
	}
	for hash := range session.assets {
		delete(g.assetsByHash, hash)
	}
	delete(g.manifests, ref)
}

func (g *HashedAssetServer) buildSnapshot(ref string) (game map[string][]StartManifestEntry, cd []StartManifestEntry, assets map[string]hashedAsset, err error) {
	mods, err := ListMods(g.gameDir)
	if err != nil {
		return nil, nil, nil, err
	}

	game = make(map[string][]StartManifestEntry, len(mods))
	assets = make(map[string]hashedAsset)

	for _, mod := range mods {
		manifest, manifestErr := buildVFSManifestWithWarnings(g.gameDir, mod, g.pakCache, func(format string, args ...any) {
			slog.Error(fmt.Sprintf("asset manifest warning: "+format, args...))
		})
		if manifestErr != nil {
			return nil, nil, nil, manifestErr
		}

		entries, err := appendManifestAssets(ref, "mod:"+mod, manifest, assets)
		if err != nil {
			return nil, nil, nil, err
		}
		game[mod] = entries
	}

	cdManifest, err := buildCDManifest(g.cdDir)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(cdManifest) > 0 {
		cd, err = appendManifestAssets(ref, "cd", cdManifest, assets)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	return game, cd, assets, nil
}

func appendManifestAssets(ref, namespace string, manifest []assetManifestEntry, assets map[string]hashedAsset) ([]StartManifestEntry, error) {
	out := make([]StartManifestEntry, 0, len(manifest))
	for _, ent := range manifest {
		key := normalizeVFSKey(ent.Path)
		if key == "" {
			continue
		}
		asset, err := hashedAssetFromEntry(ent)
		if err != nil {
			return nil, err
		}
		hash := hashAssetKey(ref, namespace+":"+key)
		if _, exists := assets[hash]; exists {
			return nil, fmt.Errorf("hash collision for %q", ent.Path)
		}
		assets[hash] = asset
		out = append(out, StartManifestEntry{Path: ent.Path})
	}
	return out, nil
}

func hashAssetKey(ref, key string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(ref + ":" + key))
	return fmt.Sprintf("%016x", h.Sum64())
}

func newManifestRef() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
