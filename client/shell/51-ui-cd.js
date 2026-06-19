// nq-ui: CD audio state machine and track list
(function() {
  var ctx = Module.nqOverlayCtx;
  if (!ctx) return;

  var STATUS_CD_INFO = 'cd-info';

  var CD_PLAY_ICON = '<svg viewBox="0 0 24 24" fill="currentColor"><polygon points="8,5 20,12 8,19"/></svg>';
  var CD_PAUSE_ICON = '<svg viewBox="0 0 24 24" fill="currentColor"><rect x="7" y="5" width="4" height="14"/><rect x="13" y="5" width="4" height="14"/></svg>';

  function syncCdPauseToggleButton() {
    var showPlay;
    var icon;
    if (!ctx.cdPauseToggleBtn)
      return;
    showPlay = !ctx.cdPlaying || ctx.cdPaused;
    ctx.cdPauseToggleBtn.classList.toggle('cd-paused', showPlay);
    icon = showPlay ? CD_PLAY_ICON : CD_PAUSE_ICON;
    if (ctx.cdPauseToggleBtn.innerHTML !== icon)
      ctx.cdPauseToggleBtn.innerHTML = icon;
    ctx.cdPauseToggleBtn.title = showPlay ? 'cd resume' : 'cd pause';
    ctx.cdPauseToggleBtn.setAttribute('aria-label', showPlay ? 'cd resume' : 'cd pause');
  }

  function getCdRuntimeState() {
    var state = 'stopped';
    var path = '';
    var file = '';

    try {
      if (Module && typeof Module.nqCdGetPlaybackState === 'function')
        state = String(Module.nqCdGetPlaybackState() || 'stopped').toLowerCase();
    } catch (e) {}

    if (state !== 'playing' && state !== 'paused' && state !== 'loading' && state !== 'stopped')
      state = 'stopped';

    try {
      if (Module && typeof Module.nqCdGetSource === 'function')
        path = String(Module.nqCdGetSource() || '');
    } catch (e2) {}

    file = path ? path.split(/[\\/]/).pop() : '';
    return { state: state, file: file, path: path };
  }

  function getCdTrackNumber(path) {
    if (!Module.nqCdGetTrackNumberFromPath)
      return 0;
    return Number(Module.nqCdGetTrackNumberFromPath(path) || 0) | 0;
  }

  function syncCdInfoMessage(runtime) {
    var text;
    var level = 'info';
    if (ctx.isCdDir(ctx.currentDir)) {
      ctx.cdInfoText = '';
      ctx.cdInfoLevel = '';
      ctx.clearStatusMessage(STATUS_CD_INFO);
      return;
    }

    if (!ctx.cdEnabled)
      text = '';
    else if (runtime.state === 'paused' || runtime.state === 'playing' || runtime.state === 'loading') {
      text = runtime.file ? ('CD: ' + runtime.file) : 'CD playing';
      if (runtime.state === 'playing' || runtime.state === 'loading')
        level = 'warning';
    } else
      text = '';

    if (text === ctx.cdInfoText && level === ctx.cdInfoLevel)
      return;
    ctx.cdInfoText = text;
    ctx.cdInfoLevel = level;

    if (!text) {
      ctx.clearStatusMessage(STATUS_CD_INFO);
      return;
    }

    var showMessage = level === 'warning' ? ctx.showWarningMessage : ctx.showInfoMessage;
    showMessage(text, 0, true, { key: STATUS_CD_INFO });
  }

  function syncCdRuntimeState() {
    var runtime = getCdRuntimeState();

    if (!ctx.cdEnabled) {
      ctx.cdPlaying = false;
      ctx.cdPaused = false;
    } else {
      ctx.cdPlaying = runtime.state === 'playing' || runtime.state === 'paused' || runtime.state === 'loading';
      ctx.cdPaused = runtime.state === 'paused';
    }

    syncCdPauseToggleButton();
    syncCdInfoMessage(runtime);
    return runtime;
  }

  function syncCdButtonsState() {
    ctx.cdButtons.forEach(function(btn) {
      var command = btn.getAttribute('data-cd-command');
      btn.disabled = ctx.uploadBusy || (!ctx.cdEnabled && command !== 'toggle') || (!nqGameStarted && command === 'toggle');
    });
  }

  function syncCdModeUI() {
    var inCdMode = ctx.isCdDir(ctx.currentDir);
    syncCdRuntimeState();
    ctx.panel.classList.toggle('cd-mode', inCdMode);
    var showEjectFocus = inCdMode && ctx.cdEnabled;
    if (ctx.cdEjectBtn) {
      ctx.cdEjectBtn.classList.toggle('active', showEjectFocus);
      ctx.cdEjectBtn.setAttribute('aria-pressed', showEjectFocus ? 'true' : 'false');
    }
    if (inCdMode)
      ctx.setTabsOpen(false);
  }

  function setCdEnabled(enabled, persistPreference) {
    ctx.cdEnabled = !!enabled;
    if (persistPreference !== false) {
      ctx.cdPreferredEnabled = ctx.cdEnabled;
      if (typeof nqSaveCdEnabled === 'function')
        nqSaveCdEnabled(ctx.cdPreferredEnabled);
    }
    if (ctx.cdRow)
      ctx.cdRow.classList.toggle('cd-off', !ctx.cdEnabled);
    if (ctx.cdPowerBtn) {
      ctx.cdPowerBtn.setAttribute('aria-pressed', ctx.cdEnabled ? 'true' : 'false');
      ctx.cdPowerBtn.title = ctx.cdEnabled ? 'cd off' : 'cd on';
    }
    syncCdRuntimeState();
    syncCdButtonsState();
    if (ctx.panel && ctx.panel.classList.contains('open') && ctx.isCdDir(ctx.currentDir))
      ctx.refresh();
  }

  function syncCdEnabledFromPreference() {
    setCdEnabled(!!nqGameStarted && !!ctx.cdPreferredEnabled, false);
  }

  function applyCdPreferenceToGame() {
    var command;
    if (!nqGameStarted)
      return;
    syncCdEnabledFromPreference();
    command = ctx.cdPreferredEnabled ? 'cd on' : 'cd off';
    if (!nqWasmExecCommand(command))
      console.error('Failed to apply CD preference: wasm command bridge unavailable');
  }

  function toggleCdView() {
    if (ctx.isCdDir(ctx.currentDir)) {
      ctx.currentDir = (ctx.nonCdDir && !ctx.isCdDir(ctx.nonCdDir)) ? ctx.nonCdDir : ctx.getDefaultDirForConfig();
      ctx.nonCdDir = null;
      ctx.refresh();
      return;
    }

    ctx.nonCdDir = ctx.currentDir;
    ctx.ensureCdDirs();
    ctx.setTabsOpen(false);
    ctx.currentDir = ctx.CD_DIR;
    ctx.refresh();
  }

  async function runCdCommand(cdCommand, trackOverride) {
    var command = String(cdCommand || '').trim().toLowerCase();
    var finalCommand;
    if (!command) return;
    if (!nqGameStarted) {
      syncCdEnabledFromPreference();
      return;
    }

    if (command === 'eject') {
      if (ctx.uploadBusy)
        return;
      toggleCdView();
      return;
    }

    if (command === 'toggle')
      command = ctx.cdEnabled ? 'off' : 'on';
    if (command === 'pause-toggle')
      command = (!ctx.cdPlaying || ctx.cdPaused) ? 'resume' : 'pause';

    if (command === 'loop') {
      var track = Number(trackOverride) | 0;
      if (track <= 0) {
        ctx.showErrorMessage('No track number to loop', 2000);
        return;
      }
      finalCommand = 'cd loop ' + track;
    } else {
      finalCommand = 'cd ' + command;
    }

    if (!nqWasmExecCommand(finalCommand)) {
      ctx.showErrorMessage('Failed to run: ' + finalCommand, 3000);
      console.error('Exec command failed: wasm command bridge unavailable');
      return;
    }

    if (command === 'on')
      setCdEnabled(true);
    if (command === 'off') {
      setCdEnabled(false);
      if (ctx.isCdDir(ctx.currentDir))
        toggleCdView();
    }
    syncCdRuntimeState();

    if (command === 'loop' || command === 'resume' || command === 'pause') {
      setTimeout(syncCdRuntimeState, 0);
      return;
    }

    if (command !== 'off' && command !== 'stop' && command !== 'on')
      ctx.showInfoMessage(finalCommand, 1200);
  }

  function getCdRemoteTracks() {
    var raw = [];
    try { raw = Module.nqCdGetRemoteTracks ? Module.nqCdGetRemoteTracks() : []; } catch (e) {}
    if (!Array.isArray(raw)) return [];
    return raw.map(function(path) {
      path = String(path || '').trim().replace(/^\/+/, '');
      return path ? (ctx.CD_DIR + path) : '';
    }).filter(Boolean);
  }

  function getCdTrackButtonState(trackPath, runtime, forceDisabled) {
    var runtimePath = runtime && runtime.path ? String(runtime.path).replace(/\\/g, '/').toLowerCase() : '';
    var pathLower = String(trackPath || '').replace(/\\/g, '/').toLowerCase();
    var isPlaying = runtime && (runtime.state === 'playing' || runtime.state === 'loading');
    var isPaused = runtime && runtime.state === 'paused';
    var isCurrentTrack;
    var disabled = !!forceDisabled || !ctx.cdEnabled;
    var trackNumber = getCdTrackNumber(trackPath);
    isCurrentTrack = !!(runtimePath && runtimePath === pathLower);
    var state = {
      isCurrentTrack: isCurrentTrack,
      isCurrentActive: isCurrentTrack && (isPlaying || isPaused),
      isCurrentPlaying: isCurrentTrack && isPlaying,
      isCurrentPaused: isCurrentTrack && isPaused,
      trackNumber: trackNumber,
      disabled: disabled
    };
    if (disabled) {
      state.isCurrentPlaying = false;
      state.isCurrentPaused = false;
      state.isCurrentActive = false;
    }
    return state;
  }

  function runCdTrackCommand(command, trackNumber, errorMessage) {
    runCdCommand(command, trackNumber).catch(function(err) { ctx.showErrorMessage(errorMessage, 2500); console.error(errorMessage + ':', err); });
  }

  function runCdTrackToggle(trackPath, trackState) {
    if (!trackState || trackState.disabled) return;
    if (trackState.isCurrentPlaying) {
      runCdTrackCommand('pause', 0, 'CD pause failed');
      return;
    }
    if (trackState.isCurrentPaused) {
      runCdTrackCommand('resume', 0, 'CD resume failed');
      return;
    }
    if (!trackState.trackNumber) {
      ctx.showErrorMessage('Track filename must start or end with a number', 2500);
      return;
    }
    runCdTrackCommand('loop', trackState.trackNumber, 'CD loop failed');
  }

  function toggleCdTrack(trackPath, forceDisabled) {
    runCdTrackToggle(trackPath, getCdTrackButtonState(trackPath, getCdRuntimeState(), !!forceDisabled));
  }

  function applyCdTrackToggleState(btn, trackState) {
    var state = trackState || { trackNumber: 0 };
    var disabled = !!state.disabled || (!state.isCurrentActive && !state.trackNumber);
    var label = 'loop track ' + (state.trackNumber || '');

    if (state.isCurrentPlaying)
      label = 'pause track';
    else if (state.isCurrentPaused)
      label = 'resume track';

    btn.classList.toggle('active', !!(state.isCurrentPlaying || state.isCurrentPaused));
    btn.innerHTML = state.isCurrentPlaying ? CD_PAUSE_ICON : CD_PLAY_ICON;
    btn.setAttribute('aria-label', label);
    btn.disabled = disabled;
  }

  function setCdTrackMeta(el, trackPath, forceDisabled) {
    if (!el)
      return;
    el.setAttribute('data-cd-track-path', trackPath);
    el.setAttribute('data-cd-track-disabled', forceDisabled ? '1' : '0');
  }

  function applyCdTrackElementState(li, runtime) {
    var btn = li.querySelector('.nq-cd-track-toggle');
    var state = getCdTrackButtonState(
      li.getAttribute('data-cd-track-path') || '',
      runtime,
      li.getAttribute('data-cd-track-disabled') === '1'
    );
    li.classList.toggle('nq-cd-track-active', !!state.isCurrentActive);
    li.classList.toggle('nq-cd-track-clickable', !state.disabled && (state.isCurrentActive || state.trackNumber));
    if (btn) applyCdTrackToggleState(btn, state);
  }

  function createCdTrackToggleButton(trackPath, forceDisabled, runtime) {
    var btn = document.createElement('button');
    btn.className = 'nq-cd-track-toggle';
    setCdTrackMeta(btn, trackPath, forceDisabled);
    applyCdTrackToggleState(btn, getCdTrackButtonState(trackPath, runtime, forceDisabled));
    return btn;
  }

  function updateCdTrackRows(runtime) {
    if (!ctx.list || !ctx.isCdDir(ctx.currentDir))
      return false;
    ctx.list.querySelectorAll('li[data-cd-track-path]').forEach(function(li) {
      applyCdTrackElementState(li, runtime);
    });
    return true;
  }

  function appendCdServerTracks(serverTracks, runtime, localTrackNumbers) {
    var heading;
    if (!serverTracks.length)
      return;
    heading = document.createElement('li');
    heading.className = 'nq-cd-server-heading';
    heading.textContent = 'Server CD tracks';
    ctx.list.appendChild(heading);

    serverTracks.forEach(function(trackPath) {
      var li = document.createElement('li');
      var span = document.createElement('span');
      var trackNumber = getCdTrackNumber(trackPath);
      var overridden = !!(trackNumber > 0 && localTrackNumbers && localTrackNumbers[trackNumber]);
      li.className = 'nq-cd-track-server';
      if (overridden)
        li.classList.add('nq-cd-track-overridden');
      setCdTrackMeta(li, trackPath, overridden);
      span.className = 'nq-fname';
      span.textContent = trackPath.startsWith(ctx.CD_DIR) ? trackPath.slice(ctx.CD_DIR.length) : trackPath;
      li.appendChild(createCdTrackToggleButton(trackPath, overridden, runtime));
      li.appendChild(span);
      applyCdTrackElementState(li, runtime);
      ctx.list.appendChild(li);
    });
  }

  Object.assign(ctx, {
    CD_PLAY_ICON,
    CD_PAUSE_ICON,
    getCdTrackNumber,
    getCdRuntimeState,
    isGameLoaded: function() { return !!nqGameStarted; },
    syncCdButtonsState,
    syncCdModeUI,
    syncCdEnabledFromPreference,
    applyCdPreferenceToGame,
    setCdEnabled,
    runCdCommand,
    getCdRemoteTracks,
    getCdTrackButtonState,
    getCdTrackState: getCdTrackButtonState,
    setCdTrackMeta,
    applyCdTrackElementState,
    createCdTrackToggleButton,
    updateCdTrackRows,
    appendCdServerTracks,
    toggleCdTrack
  });

  Module.nqOverlayOnCdStateChange = function() {
    var runtime = syncCdRuntimeState();
    if (ctx.panel && ctx.panel.classList.contains('open') && ctx.isCdDir(ctx.currentDir)) {
      if (!ctx.updateCdTrackRows || !ctx.updateCdTrackRows(runtime))
        ctx.refresh();
    }
    return runtime;
  };
})();
