package assets

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

// overlayLayers lists source layers in precedence order (later wins).
var overlayLayers = []string{"common", "server"}

// PrepareRuntimeBasedir creates an ephemeral overlay basedir with symlinks into
// sourceGameDir for each mod and starts a watcher that incrementally
// reconciles symlinks as files are added, removed, or renamed in the source
// tree. The returned stop function halts the watcher; callers should invoke it
// before removing the returned directory.
func PrepareRuntimeBasedir(sourceGameDir string, mods []string) (string, func(), error) {
	root, err := os.MkdirTemp("", "nexquake-nexus-basedir-")
	if err != nil {
		return "", nil, fmt.Errorf("create runtime basedir: %w", err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		_ = os.RemoveAll(root)
		return "", nil, fmt.Errorf("create overlay watcher: %w", err)
	}

	o := &overlay{
		runtimeRoot:   root,
		sourceDataDir: sourceGameDir,
		mods:          mods,
		w:             w,
		watched:       make(map[string]overlayWatch),
	}
	if err := o.seed(); err != nil {
		_ = w.Close()
		_ = os.RemoveAll(root)
		return "", nil, err
	}

	done := make(chan struct{})
	go o.run(done)
	stop := func() { _ = w.Close(); <-done }
	return root, stop, nil
}

type overlay struct {
	runtimeRoot   string
	sourceDataDir string
	mods          []string
	w             *fsnotify.Watcher
	watched       map[string]overlayWatch
}

type overlayWatch struct {
	mod       string
	layerRoot string
}

// reconcile makes runtime/<mod>/<rel> reflect the current source state.
// Later layers win; absence in all layers removes the symlink.
func (o *overlay) reconcile(mod, rel string) {
	if rel == "" || rel == "." {
		return
	}
	dst := filepath.Join(o.runtimeRoot, mod, strings.ToLower(filepath.ToSlash(rel)))
	for i := len(overlayLayers) - 1; i >= 0; i-- {
		src := filepath.Join(o.sourceDataDir, mod, overlayLayers[i], rel)
		if st, err := os.Lstat(src); err == nil && !st.IsDir() {
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			_ = os.RemoveAll(dst)
			_ = os.Symlink(src, dst)
			return
		}
	}
	_ = os.RemoveAll(dst)
}

// scan seeds symlinks + watches for a subtree rooted at start (must be under
// the given mod's layerRoot).
func (o *overlay) scan(mod, layerRoot, start string) {
	_ = filepath.WalkDir(start, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			o.watch(mod, layerRoot, full)
			return nil
		}
		if rel, err := filepath.Rel(layerRoot, full); err == nil {
			o.reconcile(mod, rel)
		}
		return nil
	})
}

func (o *overlay) watch(mod, layerRoot, dir string) {
	_ = o.w.Add(dir)
	o.watched[filepath.Clean(dir)] = overlayWatch{mod: mod, layerRoot: layerRoot}
}

// classify resolves an absolute event path to (mod, rel, layerRoot).
func (o *overlay) classify(abs string) (mod, rel, layerRoot string) {
	for dir := filepath.Clean(abs); ; dir = filepath.Dir(dir) {
		if watch, ok := o.watched[dir]; ok {
			rel, err := filepath.Rel(watch.layerRoot, abs)
			if err == nil && !strings.HasPrefix(rel, "..") {
				return watch.mod, rel, watch.layerRoot
			}
			return "", "", ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", ""
		}
	}
}

// seed does the initial pass: mkdir each mod dir, then walk every layer to
// install watches and create symlinks.
func (o *overlay) seed() error {
	for _, m := range o.mods {
		if err := os.MkdirAll(filepath.Join(o.runtimeRoot, m), 0o755); err != nil {
			return fmt.Errorf("create runtime mod dir %q: %w", m, err)
		}
		for _, l := range overlayLayers {
			root := filepath.Join(o.sourceDataDir, m, l)
			if st, err := os.Stat(root); err == nil && st.IsDir() {
				o.scan(m, root, root)
			}
		}
	}
	return nil
}

// run processes fsnotify events until the watcher is closed.
func (o *overlay) run(done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case ev, ok := <-o.w.Events:
			if !ok {
				return
			}
			mod, rel, layerRoot := o.classify(ev.Name)
			if mod == "" {
				continue
			}
			if ev.Op&fsnotify.Create != 0 {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					o.scan(mod, layerRoot, ev.Name)
					continue
				}
			}
			o.reconcile(mod, rel)
		case _, ok := <-o.w.Errors:
			if !ok {
				return
			}
		}
	}
}
