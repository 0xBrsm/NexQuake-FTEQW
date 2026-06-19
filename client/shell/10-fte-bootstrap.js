// 10-fte-bootstrap.js — FTE/Emscripten Module configuration and launch
//
// This file replaces the role of 10-startup.js + 11-startup-vfs.js from the
// source NexQuake shell (which were tightly coupled to the original id-Quake
// engine's NQWasm_* C exports and its custom lazy-VFS / /start-bundle server).
//
// CONTRACT with the chrome (00-core.js, 50-59-ui.js):
//   The chrome expects these to be set on Module BEFORE ftewebgl.js loads:
//     Module.canvas               — the HTMLCanvasElement
//     Module.print(text)          — console line output
//     Module.printErr(text)       — error line output
//     Module.setStatus(text)      — loader status text
//     Module.onRuntimeInitialized — called when WASM is ready (no-initial-run gate)
//     Module.noInitialRun         — true, so we control when main() runs
//     Module.arguments            — argv passed to FTE main()
//     Module.files                — virtual pre-loaded files (masters.txt, etc.)
//
//   The chrome also reads/calls these AFTER runtime init:
//     Module.nqRequestFullscreen  — set in 00-core.js, called here before enter
//     Module.nqSetTransport(name) — set in 00-core.js, called by FTE net shim
//     Module.nqOverlayCtx         — set by 50-ui.js after Module exists
//     Module.calledRun            — Emscripten standard flag; true after main() ran
//     Module.ccall(...)           — Emscripten standard; used by nqWasmExecCommand etc.
//     FS                          — Emscripten global FS; used by VFS UI (52-ui-vfs.js)
//     IDBFS                       — Emscripten global; used only if we mount it ourselves
//
// DIFFERENCES from the original NexQuake 10-startup.js:
//   - No /start bundle fetch, no NexQuake lazy-VFS, no manifest-backed remote files.
//     FTE has its own FMF-manifest VFS (index.fmf), package download, and IDBFS.
//   - No Module.callMain / NQWasm_StartMainLoop pattern.
//     FTE auto-starts its main loop when Module.autostart is true (default).
//     With noInitialRun=true we call Module.callMain([]) ourselves after ENTER.
//   - No NQWasm_ExecCommand. We shim nqWasmExecCommand via Module.ccall('Cmd_ExecuteString')
//     or the FTE-specific cbuf stuffcmd approach. See below.
//   - Transport indicator is wired to a stub; the real hook needs FTE net code inspection.

// ─── 1. Read config injected by config.js / entrypoint.sh ───────────────────
//
// config.js sets window.WEBQUAKE with:
//   manifestUrl    — path to .fmf (default: "/index.fmf")
//   gamedir        — game directory name (default: "id1")
//   serverListUrl  — URL used for in-game server browser
//   connectHost    — window.location.host (set below; not in config.js)
//
// NOTE: config.js is loaded BEFORE this script in shell.html's {{{ SCRIPT }}}
// block. If it is absent (dev mode), the cfg object defaults are used.

(function () {
  var cfg = window.WEBQUAKE || {};
  var manifestUrl = cfg.manifestUrl || '/index.fmf';
  // Game is chosen per deployment by the entrypoint (gameconfig.js -> NQ_GAMEDIR).
  var gamedir     = window.NQ_GAMEDIR || cfg.gamedir || 'id1';
  var connectHost = window.location.host;

  // Transport config, mirroring NexQuake's Module.nqTransportConfig: prefer
  // WebTransport (QUIC datagrams) on HTTPS, pinning the self-signed cert by hash
  // (wtconfig.js -> NQWT_HASHES); else the trunk WebSocket path is used. Append
  // ?transport=ws to force WebSocket.
  var fteForceWS = /(?:^|[?&])transport=ws(?:&|$)/.test(window.location.search);
  var fteTransportConfig = {};
  if (!fteForceWS && window.location.protocol === 'https:' && typeof WebTransport === 'function') {
    fteTransportConfig.webtransport = { url: 'https://' + connectHost + '/connect' };
    if (window.NQWT_HASHES && window.NQWT_HASHES.length)
      fteTransportConfig.webtransport.serverCertificateHashes = window.NQWT_HASHES;
  }

  // ─── Canvas sizing ──────────────────────────────────────────────────────
  // The shell's letterbox CSS (shell-ui.css) sizes the canvas from --nq-ar /
  // --nq-canvas-* vars that the *original* NexQuake engine set each frame. FTE
  // doesn't, so that formula is invalid and the canvas stays at its 300x150
  // default. FTE renders responsively to the canvas size (the bare page filled
  // fine with width:100%), so make the canvas simply fill the viewport and nudge
  // FTE to re-read it. Injected before the engine loads so it reads the right
  // size at init.
  (function injectCanvasFill() {
    var st = document.createElement('style');
    st.textContent =
      'html,body{width:100%;height:100%;margin:0;overflow:hidden;background:#000}' +
      'canvas#canvas{position:fixed;top:0;left:0;width:100vw!important;height:100vh!important;' +
      'max-width:none!important;max-height:none!important;display:block}';
    document.head.appendChild(st);
  })();
  function nqFitCanvas() {
    // FTE resizes its renderer to the canvas on window 'resize'; fire one so it
    // adopts the full size after the loader hands off.
    try { window.dispatchEvent(new Event('resize')); } catch (e) {}
  }

  // ─── 2. Build the masters.txt virtual file ──────────────────────────────
  //
  // FTE's in-game server browser reads a file named "masters.txt".
  // The original web/index.html injects it into Module.files using an
  // obfuscated token ("masterhttp:nq") and filename ("masters.txt") to avoid
  // plaintext scraping. We preserve that approach verbatim.

  function engineServerListToken() {
    return String.fromCharCode(109,97,115,116,101,114,104,116,116,112,58,110,113);
  }
  function engineBrowserConfigFilename() {
    return String.fromCharCode(109,97,115,116,101,114,115,46,116,120,116);
  }

  function resolveServerListUrl(override) {
    if (override) return override;
    return window.location.origin + '/servers.txt';
  }

  var serverListUrl = resolveServerListUrl(cfg.serverListUrl);
  var mastersTxtContent = serverListUrl
    ? (serverListUrl + ' ' + engineServerListToken() + ' Servers\n')
    : null;

  var preloadedFiles = mastersTxtContent
    ? { [engineBrowserConfigFilename()]: new TextEncoder().encode(mastersTxtContent).buffer }
    : {};

  // ─── 3. Bootstrap state ─────────────────────────────────────────────────
  //
  // nqBootstrapReady  — set true when FTE WASM runtime is initialized
  // nqRuntimeReady    — alias used by chrome (00-core, 10-startup convention)
  // nqGameStarted     — set true when Module.callMain has been called
  //                     (declared in 00-core.js; we just write it here)
  //
  // The progress bar phase scheme from the source is simplified: FTE drives its
  // own download progress via Module.setStatus / Module.monitorRunDependencies,
  // so we don't need a manual 3-phase bootstrap sequence.

  var fteBootstrapReady  = false;
  var fteMainLoopStarted = false;

  // ─── 4. Loader-screen helpers ────────────────────────────────────────────
  //
  // These mirror the nqSetLoaderEnterButtonEnabled / nqShowEnterButton helpers
  // in the original 10-startup.js, adapted for our simpler phase model.

  function fteSetLoaderEnterEnabled() {
    if (!loaderReloadButton) return;
    loaderReloadButton.textContent = 'ENTER';
    loaderReloadButton.disabled    = false;
    loaderReloadButton.classList.remove('hidden');
  }

  function fteShowEnterButton() {
    fteBootstrapReady = true;
    nqRuntimeReady    = true;  // chrome flag from 00-core.js
    nqSetOverlayToggleVisible(true);
    if (loaderElement) loaderElement.classList.add('enter-mode');
    if (loaderStatusElement) loaderStatusElement.textContent = '';
    fteSetLoaderEnterEnabled();
    console.info('[fte-bootstrap] WASM runtime ready — ENTER available');

    // Auto-start on non-touch desktop when ?autostart is in the URL.
    if (new URLSearchParams(window.location.search).has('autostart')) {
      var isTouch = (window.matchMedia && window.matchMedia('(pointer: coarse)').matches) ||
                    ('ontouchstart' in window && screen.width <= 1024);
      if (!isTouch) fteStartGame();
    }
  }

  // ─── 5. ENTER → main() gate ──────────────────────────────────────────────
  //
  // In the original shell: ENTER calls nqStartGameFromEnter() → nqStartGameRuntime()
  // which calls Module.callMain(args) then NQWasm_StartMainLoop().
  //
  // FTE with noInitialRun=true: we call Module.callMain([]) to start the engine.
  // FTE's Emscripten wrapper handles the main loop internally (emscripten_set_main_loop
  // or requestAnimationFrame). There is NO NQWasm_StartMainLoop equivalent.
  //
  // TODO(real-build): verify that Module.callMain([]) actually works for FTE or
  // whether Module.run() / Module._main() is the correct entry point.

  function fteStartGame() {
    if (nqGameStarted) {
      window.location.reload();
      return;
    }
    if (!fteBootstrapReady || fteMainLoopStarted) return;

    fteMainLoopStarted = true;
    nqGameStarted      = true;  // chrome flag from 00-core.js

    if (loaderReloadButton) {
      loaderReloadButton.textContent = 'STARTING…';
      loaderReloadButton.disabled    = true;
    }

    // Request fullscreen on touch devices before handing off to FTE,
    // mirroring the original nqRequestStartupFullscreen logic.
    if (Module.nexquakeTouchEnabled !== false && Module.nqTouchActive) {
      var done = false;
      function afterFullscreen() {
        if (done) return;
        done = true;
        fteCallMain();
      }
      try {
        var req = (typeof Module.nqRequestFullscreen === 'function')
          ? Module.nqRequestFullscreen()
          : null;
        if (req && req.then) {
          req.then(function () { setTimeout(afterFullscreen, 120); })
             .catch(function () { afterFullscreen(); });
        } else {
          setTimeout(afterFullscreen, 120);
        }
      } catch (e) {
        afterFullscreen();
      }
      return;
    }

    fteCallMain();
  }

  function fteCallMain() {
    console.info('[fte-bootstrap] calling Module.callMain()');
    try {
      // FTE's Emscripten module honours Module.arguments as the argv list that
      // was pre-built below; callMain([]) passes an empty additional array so
      // the pre-built arguments array takes effect.
      //
      // TODO(real-build): if FTE ignores Module.arguments when callMain is used,
      // switch to Module.callMain(Module.arguments) or Module._main(0,0).
      Module.callMain([]);

      // Hide the loader and show the canvas (mirrors Module.hideConsole in
      // the original shell).
      if (loaderElement)   loaderElement.classList.add('hidden');
      if (outputElement)   outputElement.style.display  = 'none';
      if (canvasElement) {
        canvasElement.style.display = 'block';
        canvasElement.focus();
      }
      nqFitCanvas();
      setTimeout(nqFitCanvas, 200);  // again after layout settles

      // Sync CD/overlay state now that the game has started.
      if (Module.nqOverlayCtx) {
        try {
          if (typeof Module.nqOverlayCtx.applyCdPreferenceToGame === 'function')
            Module.nqOverlayCtx.applyCdPreferenceToGame();
          if (typeof Module.nqOverlayCtx.refresh === 'function')
            Module.nqOverlayCtx.refresh();
        } catch (overlayErr) {
          console.warn('[fte-bootstrap] overlay refresh failed:', overlayErr);
        }
      }
    } catch (err) {
      console.error('[fte-bootstrap] callMain failed:', err);
      fteMainLoopStarted = false;
      nqGameStarted      = false;
      fteSetLoaderEnterEnabled();
    }
  }

  // ─── 6. Command execution shim ───────────────────────────────────────────
  //
  // The chrome calls nqWasmExecCommand(cmd) for CD control, cfg exec, menu
  // open/close, rcon, and text-entry key injection.
  //
  // nqWasmExecCommand is defined in 00-core.js as:
  //   Module.ccall('NQWasm_ExecCommand', 'void', ['string'], [cmd])
  //
  // FTE does NOT export NQWasm_ExecCommand. The equivalent in FTE is:
  //   Module.ccall('Cmd_ExecuteString', 'void', ['string', 'number'], [cmd, 0])
  // where the second argument is the "source" enum (0 = CMDFL_DONTRESUME in
  // FTE, equivalent to "stuffed from the server/console").
  //
  // TODO(real-build): verify Cmd_ExecuteString is exported in the FTE WASM
  // build (check EXPORTED_FUNCTIONS in the emscripten Makefile). Alternative:
  //   Module._Cmd_ExecuteString(ptr, 0)  — requires manual string allocation.
  // Also check whether cbuf_addtext / Cbuf_AddText is exported instead.
  //
  // We monkey-patch 00-core.js's nqWasmExecCommand by redefining it on window
  // AFTER 00-core.js has loaded but BEFORE the chrome uses it. This is safe
  // because all chrome files call it as the global `nqWasmExecCommand`.

  window.nqWasmExecCommand = function (command) {
    if (typeof Module === 'undefined' || !Module ||
        typeof Module.ccall !== 'function') return false;
    try {
      // Primary: FTE Cmd_ExecuteString
      Module.ccall('Cmd_ExecuteString', 'void', ['string', 'number'], [String(command || ''), 0]);
      return true;
    } catch (e1) {
      // Fallback: try the original symbol name in case a patched build exports it
      try {
        Module.ccall('NQWasm_ExecCommand', 'void', ['string'], [String(command || '')]);
        return true;
      } catch (e2) {
        console.warn('[fte-bootstrap] nqWasmExecCommand failed:', e1);
        return false;
      }
    }
  };

  // ─── 7. Key-injection and text-entry shims ───────────────────────────────
  //
  // 54-text-entry.js calls:
  //   nqWasmTextInputKey(keyCode)         — send a keypress to the engine
  //   nqWasmGetTextInputValue()           — read current text-edit buffer
  //
  // These are defined in 00-core.js as Module.ccall('NQWasm_TextInputKey', ...)
  // and Module.ccall('NQWasm_GetTextInputValue', ...).
  //
  // FTE handles keyboard input natively; it does not export these symbols.
  // The text-entry widget is a mobile convenience; on FTE we fall back to
  // stuffing the key as a console command if the symbols are absent.
  //
  // TODO(real-build): determine whether FTE exports Key_Event or similar.
  // For now we leave the 00-core.js definitions in place — they will silently
  // no-op via the nqWasmCall fallback path when the symbol is not found, which
  // means text-entry won't work but also won't crash.
  //
  // Similarly, nqWasmGetVideoWidth / nqWasmGetConnectedServerListenPort in
  // 00-core.js will return 0 until FTE exports are confirmed.

  // ─── 8. Transport indicator hook ─────────────────────────────────────────
  //
  // 00-core.js sets Module.nqSetTransport(name) as the callable hook.
  // In the original engine, net_wasm.c calls this from C via JS interop.
  //
  // FTE's net code does NOT call Module.nqSetTransport — it has its own
  // network status via cvars/console. Options:
  //   a) Patch FTE net_wins.c / net_websocksv.c to call Module.nqSetTransport.
  //   b) Poll a FTE cvar (e.g. "net_transport") on an interval and update the
  //      indicator from JS.
  //   c) Leave the transport indicator hidden (it will just never be shown).
  //
  // TODO(real-build): decide whether to patch FTE or use option (c). For now
  // Module.nqSetTransport is defined by 00-core.js and will simply never be
  // called, so the indicator stays hidden.

  // ─── 9. Module object — must be set BEFORE ftewebgl.js is injected ───────
  //
  // FTE's Emscripten wrapper merges window.Module at script load time. We
  // pre-populate it here. 00-core.js has already set Module.nqRequestFullscreen
  // and Module.nqSetTransport.

  var existingModule = (typeof Module !== 'undefined' && Module) ? Module : {};

  window.Module = Object.assign(existingModule, {

    // FTE reads Module.autostart. Set to false so we control the launch gate.
    // TODO(real-build): confirm FTE honours noInitialRun vs autostart.
    // The original web/index.html used autostart:true; we flip to false here.
    autostart: false,
    noInitialRun: true,

    canvas: canvasElement,

    // Trunk transport selection (WebTransport/WebSocket) read by the NP_TRUNK driver.
    nqTransportConfig: fteTransportConfig,

    // FTE argument vector: mirrors web/index.html exactly.
    arguments: [
      '-manifest', manifestUrl,
      '-game',     gamedir,
      '+set', 'cl_web_connect_host', connectHost
    ],

    // Virtual pre-loaded files (masters.txt for server browser).
    files: preloadedFiles,

    // Console output — batch-flushed to the textarea, mirroring 10-startup.js.
    print: (function () {
      if (outputElement) outputElement.value = '';
      var OUTPUT_CAP  = 64 * 1024;
      var pending     = [];
      var flushTimer  = 0;
      function flush() {
        flushTimer = 0;
        var next = (outputElement ? outputElement.value : '') + pending.join('\n') + '\n';
        pending.length = 0;
        if (next.length > OUTPUT_CAP) next = next.slice(next.length - OUTPUT_CAP);
        if (outputElement) {
          outputElement.value     = next;
          outputElement.scrollTop = outputElement.scrollHeight;
        }
      }
      return function (text) {
        if (arguments.length > 1) text = Array.prototype.slice.call(arguments).join(' ');
        console.log(text);
        pending.push(text);
        if (!flushTimer) flushTimer = setTimeout(flush, 50);
      };
    })(),

    printErr: function (msg) { console.error(msg); },

    // Loader status text — filters out FTE's noisy progress strings.
    setStatus: function (text) {
      if (!loaderStatusElement) return;
      var s = String(text || '').trim();
      if (!s) return;
      var low = s.toLowerCase();
      if (low === 'preparing...' || low === 'loading...' ||
          low === 'all downloads complete.' || low === 'downloading...') return;
      loaderStatusElement.textContent = s;
    },

    // Progress bar — FTE drives totalDependencies / monitorRunDependencies.
    totalDependencies: 0,
    monitorRunDependencies: function (left) {
      this.totalDependencies = Math.max(this.totalDependencies, left);
      if (!loaderProgressBar || this.totalDependencies <= 0) return;
      var done = this.totalDependencies - left;
      var pct  = Math.round((done / this.totalDependencies) * 90);
      loaderProgressBar.style.width = pct + '%';
    },

    // onRuntimeInitialized: WASM instantiated, FS mounted.
    // FTE calls this after WASM instantiation; the main loop has NOT started yet.
    onRuntimeInitialized: function () {
      console.info('[fte-bootstrap] onRuntimeInitialized fired');
      // Let any previously-set handler run first (59-ui-events.js wraps this).
      fteShowEnterButton();
    },

    // exportFile: download a file from Emscripten FS — mirrors original shell.
    exportFile: function (filePath) {
      try {
        var parts     = filePath.split('/');
        var dataArray = new Uint8Array(FS.readFile(filePath));
        var blob      = new Blob([dataArray], { type: 'application/octet-stream' });
        var objURL    = URL.createObjectURL(blob);
        exportElement.href     = objURL;
        exportElement.download = parts[parts.length - 1];
        exportElement.click();
        URL.revokeObjectURL(objURL);
      } catch (err) {
        console.error('[fte-bootstrap] exportFile failed:', err);
      }
    },

    // setGamma: approximate gamma via CSS filter, mirroring original shell.
    // TODO(real-build): check if FTE calls this or uses its own gamma cvar.
    setGamma: function (vidGamma) {
      vidGamma = Number(Number(vidGamma).toFixed(2));
      if (canvasElement)
        canvasElement.style.filter = 'brightness(' + ((1.35 - vidGamma) * 2) + ')';
    },

    // nqShowReloadScreen: called by FTE if/when it wants to show the quit screen.
    // TODO(real-build): FTE may not call this at all. Wire it if FTE exports a quit hook.
    nqShowReloadScreen: function () {
      try {
        if (document.pointerLockElement && document.exitPointerLock)
          document.exitPointerLock();
      } catch (e) {}
      fteMainLoopStarted = false;
      nqGameStarted      = false;
      if (loaderProgressBar)    loaderProgressBar.style.width    = '0%';
      if (loaderStatusElement)  loaderStatusElement.textContent  = '';
      fteSetLoaderEnterEnabled();
      if (loaderElement) {
        loaderElement.classList.remove('hidden');
        loaderElement.classList.add('enter-mode');
      }
      nqSetOverlayToggleVisible(true);
      if (canvasElement) canvasElement.style.display = 'none';
      if (outputElement) outputElement.style.display = 'none';
      if (Module.nqOverlayCtx && Module.nqOverlayCtx.setPanelOpen)
        try { Module.nqOverlayCtx.setPanelOpen(false); } catch (e) {}
      window.location.reload();
    },

    // Stub: the original shell populated these from config. FTE does not use them.
    nexquakeBaseGameName:  gamedir,
    nexquakeTouchEnabled:  true,

    // TODO(real-build): FTE IDBFS persistence.
    // The original 11-startup-vfs.js mounted IDBFS at /NexQuake for saved games,
    // configs, and user uploads. FTE has its own IDBFS mount via its emscripten
    // wrapper (check FTE source: web/ftewebgl.c or sys_web.c for FS.mount calls).
    // If FTE already mounts IDBFS, we should NOT mount it again here.
    // The VFS UI (52-ui-vfs.js) browses Module.nqOverlayCtx.USERFS ('/NexQuake/game')
    // and ctx.CD_USERFS ('/NexQuake/cd'). These paths must exist in Emscripten FS.
    // DECISION NEEDED: inspect ftewebgl.js at runtime to see what FS paths FTE creates.
  });

  // ─── 10. Wire ENTER button ───────────────────────────────────────────────

  if (loaderReloadButton) {
    loaderReloadButton.onclick = fteStartGame;
  }

  // ─── 11. Inject ftewebgl.js ──────────────────────────────────────────────
  //
  // Done last, after Module is fully populated. FTE's Emscripten glue reads
  // window.Module at script evaluation time.

  var s = document.createElement('script');
  s.src   = 'ftewebgl.js';
  s.async = true;
  s.addEventListener('error', function () {
    console.error('[fte-bootstrap] failed to load ftewebgl.js');
    if (loaderStatusElement)
      loaderStatusElement.textContent = 'Failed to load ftewebgl.js — check your build.';
  });
  document.head.appendChild(s);

  console.info('[fte-bootstrap] Module configured; injecting ftewebgl.js');
})();
