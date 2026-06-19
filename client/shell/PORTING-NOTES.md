# PORTING-NOTES.md — NexQuake shell onto FTE/WASM

Port of the NexQuake browser shell chrome (PWA loader, overlay UI, touch controls,
rcon, VFS) from `dev-NexQuake/src/client/shell` onto the FTE engine WASM build
(`ftewebgl.js`/`ftewebgl.wasm`) shipped by this repo.

---

## 1. Module contract — what the chrome depends on

These are the symbols the ported chrome files (00-core.js, 50-59-ui.js) read or
write on `window.Module`.  Broken into "provided by chrome" (set before engine
load) and "expected from engine" (must exist after `onRuntimeInitialized`).

### 1a. Set by the chrome / bootstrap, consumed by FTE

| Symbol | Set in | FTE expectation |
|---|---|---|
| `Module.canvas` | 10-fte-bootstrap.js | Standard Emscripten — FTE reads this |
| `Module.arguments` | 10-fte-bootstrap.js | Standard Emscripten argv |
| `Module.files` | 10-fte-bootstrap.js | Standard Emscripten pre-loaded files |
| `Module.noInitialRun` | 10-fte-bootstrap.js | Standard Emscripten; FTE may use `autostart` instead |
| `Module.autostart` | 10-fte-bootstrap.js | FTE-specific; set `false` to gate on ENTER |
| `Module.print` | 10-fte-bootstrap.js | Standard Emscripten stdout |
| `Module.printErr` | 10-fte-bootstrap.js | Standard Emscripten stderr |
| `Module.setStatus` | 10-fte-bootstrap.js | Standard Emscripten progress text |
| `Module.monitorRunDependencies` | 10-fte-bootstrap.js | Standard Emscripten progress hook |
| `Module.onRuntimeInitialized` | 10-fte-bootstrap.js | Standard Emscripten; wrapped by 59-ui-events.js |
| `Module.nqRequestFullscreen` | 00-core.js | Called by bootstrap before entering game on touch |
| `Module.nqSetTransport(name)` | 00-core.js | Called from engine net code on connect/disconnect |

### 1b. Expected from engine after runtime init (used by chrome)

| Symbol | Used by | Origin | FTE equivalent / status |
|---|---|---|---|
| `Module.ccall` | 00-core.js (all nqWasm* wrappers) | Emscripten standard | Present in all Emscripten builds |
| `Module.calledRun` | 59-ui-events.js | Emscripten standard | Set to `true` after `main()` |
| `FS` (global) | 52-ui-vfs.js, 53-ui-upload.js, 54-text-entry.js | Emscripten global | Present in all Emscripten builds |
| `IDBFS` (global) | 11-startup-vfs.js (not ported) | Emscripten global | Present; FTE may already mount it |
| `NQWasm_ExecCommand` (via ccall) | 00-core.js `nqWasmExecCommand` | Custom C export in id-Quake | **NOT in FTE** — shimmed to `Cmd_ExecuteString` |
| `NQWasm_StartMainLoop` (via ccall) | 00-core.js `nqWasmStartMainLoop` | Custom C export | **NOT in FTE** — not needed; FTE loops internally |
| `NQWasm_PrefetchKnownSounds` (via ccall) | 00-core.js | Custom C export | **NOT in FTE** — FTE manages sound loading itself |
| `NQWasm_GetKeyBinding` (via ccall) | 00-core.js | Custom C export | Unknown; likely not exported |
| `NQWasm_TextInputKey` (via ccall) | 54-text-entry.js | Custom C export | Unknown; may map to `Key_Event` |
| `NQWasm_GetTextInputValue` (via ccall) | 54-text-entry.js | Custom C export | Unknown |
| `NQWasm_GetVideoWidth` (via ccall) | 59-ui-events.js | Custom C export | Unknown; may use canvas width directly |
| `NQWasm_GetConnectedServerListenPort` (via ccall) | 50-ui.js join-code | Custom C export | Unknown; check FTE net cvars |
| `Module.exportFile(path)` | 59-ui-events.js (download button) | Shell-defined helper | Provided by bootstrap |
| `Module.setGamma(v)` | 10-startup.js | Shell-defined | Provided by bootstrap; TODO: does FTE call it? |
| `Module.nqShowReloadScreen()` | 10-startup.js | Shell-defined | Provided by bootstrap; TODO: does FTE call it? |
| `Module.nqOverlayCtx` | 50-ui.js onwards | Set by chrome at runtime | Set by 50-ui.js after Module exists |
| `Module.nexquakeInstalledManifests` | 52-ui-vfs.js getDirs() | Set by 11-startup-vfs.js | **NOT SET** — VFS tab list will show only base game |
| `Module.nexquakeBaseGameName` | 00-core.js, 50-ui.js | Set by 10-startup.js | Set to `gamedir` in bootstrap |
| `Module.nexquakeTouchEnabled` | 21-touch-controls.js | Set by 10-startup.js | Set to `true` in bootstrap |
| `Module.nqPerModConfig` | 50-ui.js config-global toggle | Set by 10-startup.js | Not set; 50-ui.js initializes from localStorage |
| `Module.nqIsTouchInput` | 50-ui.js, 59-ui-events.js | Set by 21-touch-controls.js | Set at runtime by touch-controls |
| `Module.nqTouchActive` | 10-startup.js fullscreen gate | Set by 21-touch-controls.js | Set at runtime |

---

## 2. Feature bridge/port decisions

### 2a. ENTER-gate / PWA loader
**Decision: BRIDGE (already done)**
The loader screen, progress bar, and ENTER button are engine-agnostic HTML/CSS.
`10-fte-bootstrap.js` sets `Module.noInitialRun = true` / `Module.autostart = false`,
waits for `Module.onRuntimeInitialized`, then calls `Module.callMain([])` on ENTER.
Risk: FTE may not honour `noInitialRun`; it may use `autostart` instead, or ignore
both and start immediately. Verify against a real build.

### 2b. Rcon UI (55-rcon.js)
**Decision: BRIDGE with caution**
The rcon UI posts JSON-RPC to `/rcon` on the Nexus backend — it does not call
engine code. It is fully engine-agnostic HTTP code. It works identically over FTE
as long as the Nexus backend (`nexus/`) remains in place. No engine changes needed.
Note: the rcon "join code" display reads `NQWasm_GetConnectedServerListenPort` which
is not exported by FTE. The join code widget will show nothing until that is bridged
or replaced with a FTE cvar read.

### 2c. File upload / user VFS (52-ui-vfs.js, 53-ui-upload.js)
**Decision: NEEDS INVESTIGATION**
The VFS UI browses `USERFS = /NexQuake/game` and `CD_USERFS = /NexQuake/cd` in
Emscripten FS. In the original shell, `11-startup-vfs.js` mounted IDBFS at
`/NexQuake` and symlinked game dirs into it. FTE's own Emscripten wrapper may
already mount IDBFS at a different path (check `sys_web.c` or `ftewebgl.c`).
- If FTE mounts IDBFS at `/NexQuake`, the UI works as-is.
- If FTE mounts IDBFS elsewhere (e.g. `/id1`), we must either redirect `USERFS` or
  add our own `FS.mount(IDBFS, {}, '/NexQuake')` in a `Module.preRun` function.
  Risk of double-mounting: only mount if the path does not already have an IDBFS node.
- `Module.nexquakeInstalledManifests` is never populated (no `/start` bundle), so
  the multi-mod tab list in `getDirs()` will only show the base game tab. That is
  acceptable for FTE which does not use NexQuake's manifest system.

### 2d. CD audio UI (51-ui-cd.js)
**Decision: BRIDGE where possible, partly unsupported**
FTE natively handles CD audio via the `cd` console command (same syntax as original
Quake). The `cd on/off/stop/play/loop N` commands sent via `nqWasmExecCommand`
will route to FTE's `Cmd_ExecuteString` shim and should work if FTE has compiled-in
CD/OGG support (which it does in the WebGL build when sound is enabled).
The following Module hooks queried by `51-ui-cd.js` are NOT in FTE:
  - `Module.nqCdGetPlaybackState()` — playback state (playing/paused/stopped)
  - `Module.nqCdGetSource()` — currently playing file path
  - `Module.nqCdGetTrackNumberFromPath(path)` — track number from filename
  - `Module.nqCdGetRemoteTracks()` — server-provided track list
These are polled on a timer by the overlay. They will return empty/stopped values,
so the CD status indicator and pause/resume button will show nothing active. The
`cd on/off/loop/stop/pause/resume` commands still work as fire-and-forget.
**To fully support the CD UI:** export the four hooks above from FTE C code, or
accept that the UI will issue commands without showing current state.

### 2e. Touch controls (20-touch-glyphs.js, 21-touch-controls.js)
**Decision: PARTIAL BRIDGE**
The touch HUD is pure JS/CSS; it reads key bindings via `nqWasmGetKeyBinding`
which calls `NQWasm_GetKeyBinding`. That symbol is not in FTE. The HUD buttons
will render but will display fallback glyphs instead of live binding labels, and
button-bound commands will not be sent to FTE via the JS layer.
FTE has its own native touch/gamepad input (`in_touch.c`). The JS overlay
joystick and tap-zone buttons in the source shell worked by synthesising Quake
key events via `NQWasm_TextInputKey`; FTE's touch layer is driven differently
(it reads touch events directly via Emscripten). **Risk of conflict**: the NexQuake
touch overlay and FTE's native touch input may both react to the same touch events.
Disable one or the other. Recommended: disable the NexQuake JS touch HUD initially
and rely on FTE's native touch support, then re-enable the HUD if FTE's touch is
insufficient.

### 2f. Asset prefetch / lazy VFS
**Decision: NOT PORTED — not applicable to FTE**
`11-startup-vfs.js` implemented a custom lazy-VFS backed by NexQuake's `/start`
bundle API and synchronous XHR on first file access. FTE does not use this mechanism.
FTE downloads packages listed in `index.fmf` (PAK/PK3) via its own Emscripten
fetch code before `main()` starts. No JS-side VFS shim is needed or wanted.

### 2g. Game-switch / multi-mod tabs
**Decision: PARTIAL BRIDGE**
The VFS tab list in `52-ui-vfs.js` calls `getDirs()` which scans
`Module.nexquakeInstalledManifests` (never populated with FTE) and `USERFS`.
Result: only the base game tab appears. That is correct for a single-game FTE
deployment. If multi-mod support is needed, populate `Module.nexquakeInstalledManifests`
from a list of available gamedirs at startup.

### 2h. PWA / service worker (manifest.webmanifest)
**Decision: BRIDGE — works as-is**
The manifest is engine-agnostic. It references icons and theme colors only.
Update `start_url` in the manifest if the shell is served at a non-root path.

---

## 3. Open questions for when ftewebgl.js is available

1. **`autostart` vs `noInitialRun`**: Which flag (or both) does FTE's Emscripten
   glue honour to defer `main()`? Search for `Module.autostart` and `Module.noInitialRun`
   in ftewebgl.js. If neither works, FTE may require a different gate (e.g. a
   `Module.preRun` function that stalls).

2. **`Cmd_ExecuteString` export**: Is `Cmd_ExecuteString` in `EXPORTED_FUNCTIONS`?
   Run `grep -F 'Cmd_ExecuteString' ftewebgl.js` or inspect the build's
   `engine/Makefile` for `-s EXPORTED_FUNCTIONS`. If absent, find the correct
   exported symbol for command execution (common alternatives: `Cbuf_AddText`,
   `PR_ExecuteProgram`, or a custom `fteweb_*` export).

3. **IDBFS mount path**: Run `ftewebgl.js` in a throwaway page and inspect `FS.mounts`
   or `FS.root` after `onRuntimeInitialized`. If FTE mounts IDBFS at a known path,
   add a `Module.preRun` function in `10-fte-bootstrap.js` that creates `/NexQuake`
   and symlinks into FTE's actual IDBFS root so the VFS UI can browse user files.

4. **FTE FS layout under WASM**: After init, list `FS.root` to understand what paths
   FTE creates. Specifically: where does it put downloaded package contents, where
   does it write config/saves, does it create per-game subdirectories?

5. **`callMain` behaviour**: Does `Module.callMain([])` work after `onRuntimeInitialized`,
   or does FTE start the main loop during instantiation regardless of `noInitialRun`?
   If the latter, the ENTER gate is non-functional and we need a different approach
   (e.g. start paused, unpause on ENTER).

6. **CD audio in ftewebgl.js**: Does the WASM build include OGG/CD audio? Check for
   `CD_` symbols or `snd_` in the binary. If CD is compiled in, are there JS-callable
   hooks to read playback state? Searching ftewebgl.js for `fteweb_cd` or `CDAudio`
   would confirm this.

7. **Transport indicator**: Search ftewebgl.js for WebSocket / WebTransport setup code.
   Identify the JS callback or Module property FTE uses when a connection is adopted.
   Wire it to call `Module.nqSetTransport(name)`.

8. **Key binding reads**: Search ftewebgl.js for `Key_GetBinding` or similar. If
   exported, update the `nqWasmGetKeyBinding` shim so touch button labels are live.

9. **Touch input conflict**: Test on a real mobile device to determine whether FTE's
   native Emscripten touch handling and the NexQuake JS touch HUD fire on the same
   events. Likely fix: set `Module.nexquakeTouchEnabled = false` in bootstrap until
   this is resolved.

10. **`NQWasm_GetConnectedServerListenPort`**: The join-code widget reads the listen
    port of the currently-connected server. FTE exposes this via the `cl_serveraddress`
    cvar or similar. Either read the cvar via Cmd_ExecuteString or export a helper.

---

## 4. Biggest risks

1. **FTE may auto-start before ENTER is pressed.** If `Module.autostart` is not
   supported (or defaults to true), the WASM starts immediately and the loader ENTER
   button never gates the engine. The screen would go blank before the user presses
   anything. Mitigation: confirm `autostart:false` behaviour against a real build before
   shipping the shell page.

2. **`Cmd_ExecuteString` may not be exported.** Most FTE WASM builds only export a small
   set of symbols. If the command execution shim fails silently, CD audio, cfg exec,
   menu toggles, and the join-code widget all break without any visible error. Add a
   runtime warning in the browser console if ccall('Cmd_ExecuteString') throws.

3. **IDBFS path mismatch.** If FTE mounts its own IDBFS before our preRun hook, a second
   `FS.mount(IDBFS, {}, '/NexQuake')` may throw or silently fail, leaving the VFS UI
   unable to write files. Read ftewebgl.js FS setup carefully before adding any IDBFS
   mounts.

4. **Touch input conflict.** Shipping both the NexQuake JS touch HUD and FTE's native
   Emscripten touch layer simultaneously may result in double-input or broken touch on
   mobile. Keep `nexquakeTouchEnabled = false` until tested.

5. **shell.html template substitutions.** `shell.html` contains `{{{ SCRIPT }}}`,
   `__NEXQUAKE_GAMENAME__`, `__NEXQUAKE_REMOTE_ROOT_BASENAME__`, and
   `__NEXQUAKE_VERSION__` placeholders that the source build system replaced at build
   time. These must be substituted (or their references in JS stubs removed) before the
   shell page is functional. `00-core.js` reads `NEXQUAKE_GAMENAME` and
   `NEXQUAKE_REMOTE_ROOT` as JS globals. With FTE there is no remote-root; set
   `NEXQUAKE_GAMENAME` to the gamedir and `NEXQUAKE_REMOTE_ROOT` to a dummy value.
