// Package assets manages the NexQuake game-asset pipeline: scanning and serving
// VFS (Virtual File System) manifests built from layered mod directories and .pak
// archives, streaming CD audio tracks, and bootstrapping missing game data on
// first run.
//
// # Layout
//
// Each mod lives under GAME_DIR/<mod>/ and is split into three layers:
//
//	common/  – shared data (paks, loose files)
//	server/  – server-only overrides (merged into the runtime basedir)
//	client/  – client-only assets (served to browsers; override common in VFS)
//
// # Key types
//
//	HashedAssetServer  – start-manifest + hash-addressed asset serving
//	PakIndexCache       – thread-safe cache of parsed .pak directory entries
//
// Typical usage in the nexus HTTP mux:
//
//	gw := assets.NewHashedAssetServer(gameDir, cdDir, pakCache, concurrency, smenu, args, urlArgs)
//	mux.Handle("/start", http.HandlerFunc(gw.StartHandler()))
//	mux.Handle("/nq/",   http.HandlerFunc(gw.AssetHandler()))
package assets
