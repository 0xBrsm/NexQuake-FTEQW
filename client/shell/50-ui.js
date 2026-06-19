// nq-ui: settings panel bootstrap and core helpers
(function() {
  var toggle = document.getElementById('nq-overlay-toggle');
  var panel = document.getElementById('nq-overlay-panel');
  if (!toggle || !panel) return;

  function syncOverlayViewportHeight() {
    var viewport = window.visualViewport;
    var h = viewport && viewport.height ? viewport.height : window.innerHeight;
    if (!h) return;
    document.documentElement.style.setProperty('--nq-overlay-vh', Math.round(h) + 'px');
  }

  var canvasViewportWidth = 0;
  var canvasViewportHeight = 0;
  var canvasViewportOrientation = '';

  function syncCanvasViewportSize(forceReset) {
    var root = document.documentElement;
    var viewportWidth = window.innerWidth || root.clientWidth || 0;
    var viewportHeight = window.innerHeight || root.clientHeight || 0;
    var orientation = viewportWidth >= viewportHeight ? 'landscape' : 'portrait';
    var freezeTouchViewport = !!(Module && Module.nqIsTouchInput);
    forceReset = forceReset === true;

    if (!(viewportWidth > 0 && viewportHeight > 0))
      return;

    if (!freezeTouchViewport) {
      canvasViewportWidth = viewportWidth;
      canvasViewportHeight = viewportHeight;
      canvasViewportOrientation = orientation;
    } else {
      /* Touch keyboards can transiently shrink viewport units; keep the
       * gameplay canvas pinned to the largest viewport seen per orientation. */
      if (forceReset || canvasViewportOrientation !== orientation) {
        canvasViewportWidth = 0;
        canvasViewportHeight = 0;
        canvasViewportOrientation = orientation;
      }
      if (viewportWidth > canvasViewportWidth)
        canvasViewportWidth = viewportWidth;
      if (viewportHeight > canvasViewportHeight)
        canvasViewportHeight = viewportHeight;
    }

    root.style.setProperty('--nq-canvas-vw', Math.round(canvasViewportWidth) + 'px');
    root.style.setProperty('--nq-canvas-vh', Math.round(canvasViewportHeight) + 'px');
  }
  syncOverlayViewportHeight();
  syncCanvasViewportSize(true);

  function onResize() {
    syncOverlayViewportHeight();
    syncCanvasViewportSize(false);
  }
  function onOrientationChange() {
    syncOverlayViewportHeight();
    syncCanvasViewportSize(true);
  }
  window.addEventListener('resize', onResize, { passive: true });
  window.addEventListener('orientationchange', onOrientationChange, { passive: true });
  if (window.visualViewport) {
    window.visualViewport.addEventListener('resize', onResize, { passive: true });
    window.visualViewport.addEventListener('scroll', onResize, { passive: true });
  }

  var cdRow = document.getElementById('nq-cd-row');
  var userExts = (Module && Array.isArray(Module.nqUserFileExts) && Module.nqUserFileExts.length)
    ? Module.nqUserFileExts.slice()
    : nqGetUserFileExts();
  var cdExts = ['ogg', 'mp3'];
  var storedCdEnabled = (typeof nqLoadCdEnabled === 'function') ? nqLoadCdEnabled() : null;

  var ctx = {
    toggle: toggle,
    closeButton: document.getElementById('nq-overlay-close'),
    panel: panel,
    tabs: document.getElementById('nq-vfs-tabs'),
    list: document.getElementById('nq-vfs-list'),
    cdRow: cdRow,
    cdButtons: cdRow ? cdRow.querySelectorAll('.nq-cd-btn[data-cd-command]') : [],
    cdEjectBtn: cdRow ? cdRow.querySelector('.nq-cd-btn[data-cd-command="eject"]') : null,
    cdPowerBtn: cdRow ? cdRow.querySelector('.nq-cd-btn[data-cd-command="toggle"]') : null,
    cdPauseToggleBtn: cdRow ? cdRow.querySelector('.nq-cd-btn[data-cd-command="pause-toggle"]') : null,
    upload: document.getElementById('nq-vfs-upload'),
    fileInput: document.getElementById('nq-vfs-file'),
    uploadError: document.getElementById('nq-upload-error'),
    editor: document.getElementById('nq-editor'),
    editorPath: document.getElementById('nq-editor-path'),
    editorText: document.getElementById('nq-editor-text'),
    editorSave: document.getElementById('nq-editor-save'),
    editorCancel: document.getElementById('nq-editor-cancel'),
    confirm: document.getElementById('nq-confirm'),
    confirmText: document.getElementById('nq-confirm-text'),
    confirmOk: document.getElementById('nq-confirm-ok'),
    confirmCancel: document.getElementById('nq-confirm-cancel'),
    textEntry: document.getElementById('nq-text-entry'),
    textEntryForm: document.getElementById('nq-text-entry-form'),
    textEntryInput: document.getElementById('nq-text-entry-input'),
    configGlobalBtn: document.getElementById('nq-config-global'),
    joinCodeBtn: document.getElementById('nq-join-code'),
    joinCodeValue: document.getElementById('nq-join-code-value'),
    brandingRow: document.getElementById('nq-branding-row'),
    branding: document.getElementById('nq-branding'),
    versionEl: document.getElementById('nq-version'),
    tabsWrap: document.getElementById('nq-tabs-wrap'),
    dirHeader: document.getElementById('nq-dir-header'),
    dirLabel: document.getElementById('nq-dir-label'),
    tabsMeasureCtx: document.createElement('canvas').getContext('2d'),

    USERFS: (Module && Module.nqUserFSRoot) ? Module.nqUserFSRoot : '/NexQuake/game',
    CD_USERFS: (Module && Module.nqCdUserFSRoot) ? Module.nqCdUserFSRoot : '/NexQuake/cd',
    USER_EXTS: userExts,
    USER_FILE_ACCEPT: userExts.map(function(ext) { return '.' + ext; }).join(','),
    USER_FILE_DESC: userExts.map(function(ext) { return '.' + ext; }).join(' '),
    CD_DIR: (typeof nqGetCdDir === 'function') ? nqGetCdDir() : '/cd/',
    CD_EXTS: cdExts,
    CD_FILE_ACCEPT: cdExts.map(function(ext) { return '.' + ext; }).join(','),
    CD_FILE_DESC: '.ogg .mp3',

    currentDir: null,
    editingFile: null,
    uploadBusy: false,
    cdPreferredEnabled: storedCdEnabled === null ? true : !!storedCdEnabled,
    cdEnabled: true,
    cdPaused: false,
    cdPlaying: false,
    joinCodePort: 0,
    nonCdDir: null,

    safeReadDir: nqSafeReadDir,
    safeStat: nqSafeStat,
    safeMkdirTree: nqSafeMkdirTree,
    safeUnlink: nqSafeUnlink,
    safeSyncFS: nqSafeSyncFS
  };

  Module.nqTextEntryOpen = false;
  Module.nqTextEntryFocused = false;
  Module.nqConsoleTextEntryOpen = false;
  Module.nqMessageTextEntryOpen = false;
  ctx.syncModalOpen = function() {
    var blocking = ctx.panel.classList.contains('open') ||
      ctx.editor.classList.contains('open') ||
      (ctx.confirm && ctx.confirm.classList.contains('open'));
    Module.nqOverlayBlockingModalOpen = blocking;
    Module.nqOverlayModalOpen = blocking || !!Module.nqTextEntryOpen;
    // Touch HUD reacts to modal state immediately instead of waiting for
    // its 200ms visibility poll (hook exported by 21-touch-controls.js).
    if (typeof Module.nqTouchSyncVisibility === 'function')
      Module.nqTouchSyncVisibility();
  };
  ctx.syncModalOpen();
  Module.nqOverlayCtx = ctx;
  ctx.syncCanvasViewportSize = syncCanvasViewportSize;

  function isUserFile(path) {
    var ext = path.slice(path.lastIndexOf('.') + 1).toLowerCase();
    return ctx.USER_EXTS.indexOf(ext) >= 0;
  }

  function isCdFile(path) {
    var ext = path.slice(path.lastIndexOf('.') + 1).toLowerCase();
    return ctx.CD_EXTS.indexOf(ext) >= 0;
  }

  function isCdDir(dir) {
    return dir === ctx.CD_DIR;
  }

  function getBaseGameDir() {
    var baseName = (typeof nqGetBaseGameName === 'function')
      ? nqGetBaseGameName()
      : (Module.nexquakeBaseGameName || 'id1');
    baseName = String(baseName || 'id1').replace(/^\/+|\/+$/g, '') || 'id1';
    return '/' + baseName + '/';
  }

  var getDefaultDirForConfig = getBaseGameDir;
  function getUploadDir() { return ctx.currentDir; }

  function getCurrentUploadSettings() {
    if (isCdDir(ctx.currentDir)) {
      return {
        extensions: ctx.CD_EXTS,
        invalidText: 'CD tracks ' + ctx.CD_FILE_DESC + ' only',
        dir: ctx.CD_DIR,
        accept: ctx.CD_FILE_ACCEPT
      };
    }
    return {
      extensions: ctx.USER_EXTS,
      invalidText: 'Quake ' + ctx.USER_FILE_DESC + ' only',
      dir: getUploadDir(),
      accept: ctx.USER_FILE_ACCEPT
    };
  }

  function ensureCdDirs() {
    var root = ctx.CD_DIR.replace(/\/$/, '');
    var backup = String(ctx.CD_USERFS || '').replace(/\/$/, '');
    if (!backup)
      return;
    ctx.safeMkdirTree(backup);
    try { FS.symlink(backup, root); } catch (e) {}
  }

  function setTabsOpen(open) {
    if (open && isCdDir(ctx.currentDir))
      open = false;
    ctx.tabsWrap.classList.toggle('open', open);
  }

  function setTabsOpenWidth(labels) {
    var maxWidth = 0;
    var measureCtx = ctx.tabsMeasureCtx;
    if (!labels || !labels.length) labels = ['GAME'];

    measureCtx.font = '11px monospace';
    labels.forEach(function(label) {
      maxWidth = Math.max(maxWidth, measureCtx.measureText(label).width);
    });

    ctx.panel.style.setProperty('--nq-tabs-open-width', Math.ceil(maxWidth + 26) + 'px');
  }

  var STATUS_PROGRESS = 'upload-progress';
  var STATUS_TOAST = 'toast';
  var STATUS_CD_INFO = 'cd-info';
  var STATUS_ORDER = [STATUS_PROGRESS, STATUS_TOAST, STATUS_CD_INFO];
  var statusSlots = {
    'upload-progress': null,
    'toast': null,
    'cd-info': null
  };

  function renderStatusMessages() {
    ctx.uploadError.innerHTML = '';
    STATUS_ORDER.forEach(function(key) {
      var entry = statusSlots[key];
      var item;
      if (!entry) return;
      item = document.createElement('div');
      item.className = 'nq-status-message ' + entry.level;
      item.textContent = entry.text;
      ctx.uploadError.appendChild(item);
    });
  }

  function clearStatusMessage(key) {
    key = String(key || '').trim();
    if (!key) {
      STATUS_ORDER.forEach(function(slotKey) {
        var entry = statusSlots[slotKey];
        if (entry && entry.timeoutId)
          clearTimeout(entry.timeoutId);
        statusSlots[slotKey] = null;
      });
      renderStatusMessages();
      return;
    }

    if (!Object.prototype.hasOwnProperty.call(statusSlots, key))
      key = STATUS_TOAST;
    var entry = statusSlots[key];
    if (!entry) return;
    if (entry.timeoutId) clearTimeout(entry.timeoutId);
    statusSlots[key] = null;
    renderStatusMessages();
  }

  function showStatusMessage(level, msg, clearAfterMs, sticky, options) {
    var text = String(msg || '').trim();
    var opts = options || {};
    var key = String(opts.key || STATUS_TOAST).trim();
    var timeoutMs;

    if (!Object.prototype.hasOwnProperty.call(statusSlots, key))
      key = STATUS_TOAST;

    if (!text) {
      clearStatusMessage(key);
      return;
    }

    clearStatusMessage(key);
    var entry = statusSlots[key] = {
      level: level,
      text: text,
      timeoutId: 0
    };

    renderStatusMessages();

    timeoutMs = Number(clearAfterMs) | 0;
    if (!sticky && timeoutMs > 0) {
      entry.timeoutId = setTimeout(function() {
        if (statusSlots[key] === entry)
          clearStatusMessage(key);
      }, timeoutMs);
    }
  }

  function showInfoMessage(msg, ms, sticky, opts) { showStatusMessage('info', msg, ms, sticky, opts); }
  function showWarningMessage(msg, ms, sticky, opts) { showStatusMessage('warning', msg, ms, sticky, opts); }
  function showErrorMessage(msg, ms, sticky, opts) { showStatusMessage('error', msg, ms, sticky, opts); }

  var pendingConfirmResolve = null;
  var pendingNoticeOnOk = null;

  function closeConfirmModal(result) {
    var resolve = pendingConfirmResolve;
    if (!resolve)
      return false;
    pendingConfirmResolve = null;
    pendingNoticeOnOk = null;
    if (ctx.confirmCancel) ctx.confirmCancel.style.display = '';
    ctx.confirm.classList.remove('open');
    ctx.confirm.setAttribute('aria-hidden', 'true');
    ctx.syncModalOpen();
    resolve(!!result);
    return true;
  }

  // openConfirmModal backs both confirmAsync (OK + Cancel) and noticeAsync
  // (single OK). It reuses the confirm modal, which is top-level so it shows
  // regardless of overlay-panel state. onOk, when given, runs SYNCHRONOUSLY in
  // the OK click so callers keep the user gesture (e.g. re-enter fullscreen).
  // syncModalOpen() keeps the blocking-modal flags + touch HUD in step so the
  // buttons stay tappable in landscape. Resolves true on OK, false on dismiss.
  function openConfirmModal(message, okText, showCancel, onOk) {
    var text = String(message || '').trim();
    if (!text || !ctx.confirm || !ctx.confirmText || !ctx.confirmOk || !ctx.confirmCancel)
      return Promise.resolve(false);
    closeConfirmModal(false);
    clearStatusMessage(STATUS_TOAST);
    pendingNoticeOnOk = (typeof onOk === 'function') ? onOk : null;
    ctx.confirmText.textContent = text;
    ctx.confirmOk.textContent = okText;
    ctx.confirmCancel.style.display = showCancel ? '' : 'none';
    ctx.confirm.classList.add('open');
    ctx.confirm.setAttribute('aria-hidden', 'false');
    ctx.syncModalOpen();
    return new Promise(function(resolve) {
      pendingConfirmResolve = resolve;
    });
  }

  function confirmAsync(message, okText) {
    return openConfirmModal(message, String(okText || 'confirm').trim() || 'confirm', true, null);
  }

  function noticeAsync(message, okText, onOk) {
    return openConfirmModal(message, String(okText || 'OK').trim() || 'OK', false, onOk);
  }

  if (ctx.confirm) {
    ctx.confirm.addEventListener('mousedown', function(ev) {
      if (ev.target === ctx.confirm) {
        ev.preventDefault();
        closeConfirmModal(false);
      }
    });
  }
  if (ctx.confirmCancel) {
    ctx.confirmCancel.onclick = function(ev) {
      ev.preventDefault();
      closeConfirmModal(false);
    };
  }
  if (ctx.confirmOk) {
    ctx.confirmOk.onclick = function(ev) {
      ev.preventDefault();
      // Run any notice onOk synchronously, inside this click handler, so it
      // still counts as a user gesture (e.g. re-entering fullscreen).
      var onOk = pendingNoticeOnOk;
      pendingNoticeOnOk = null;
      if (onOk) { try { onOk(); } catch (e) {} }
      closeConfirmModal(true);
    };
  }

  function setUploadBusyState(busy) {
    ctx.uploadBusy = !!busy;
    ctx.upload.disabled = ctx.uploadBusy;
    if (ctx.syncCdButtonsState)
      ctx.syncCdButtonsState();
  }

  function syncConfigToggle() {
    var isGlobal = !Module.nqPerModConfig;
    var hoverText = isGlobal ? 'global cfgs enabled' : 'global cfgs disabled';
    if (!ctx.configGlobalBtn) return;
    ctx.configGlobalBtn.classList.toggle('active', isGlobal);
    ctx.configGlobalBtn.setAttribute('aria-pressed', isGlobal ? 'true' : 'false');
    ctx.configGlobalBtn.title = hoverText;
    ctx.configGlobalBtn.setAttribute('aria-label', hoverText);
  }

  function syncFooterLayout() {
    var row;
    var rowWidth;
    var gapPx;
    var requiredWidth;
    var hasJoinCode;
    var isCompact;
    var versionWidth;
    if (!ctx.brandingRow || !ctx.branding || !ctx.joinCodeBtn || !ctx.versionEl)
      return;

    row = ctx.brandingRow;
    hasJoinCode = !ctx.joinCodeBtn.hidden;
    row.classList.toggle('nq-no-join-code', !hasJoinCode);
    if (!hasJoinCode) {
      row.classList.remove('nq-branding-compact');
      return;
    }

    rowWidth = row.clientWidth;
    if (rowWidth <= 0)
      return;

    gapPx = parseFloat(window.getComputedStyle(row).columnGap || '0') || 0;
    versionWidth = Math.ceil(ctx.versionEl.scrollWidth) || 0;
    requiredWidth =
      Math.ceil(ctx.branding.getBoundingClientRect().width) +
      Math.ceil(ctx.joinCodeBtn.getBoundingClientRect().width) +
      versionWidth +
      Math.ceil(gapPx * 2);

    isCompact = row.classList.contains('nq-branding-compact');
    if (!isCompact && requiredWidth > rowWidth) {
      row.classList.add('nq-branding-compact');
      return;
    }
    if (isCompact && requiredWidth <= (rowWidth - 12))
      row.classList.remove('nq-branding-compact');
  }

  function syncJoinCode() {
    var port;
    var label;
    if (!ctx.joinCodeBtn || !ctx.joinCodeValue)
      return;
    port = Number(nqWasmGetConnectedServerListenPort()) | 0;
    if (port < 1 || port > 65535)
      port = 0;
    ctx.joinCodePort = port;
    ctx.joinCodeBtn.hidden = port <= 0;
    if (port > 0) {
      ctx.joinCodeValue.textContent = String(port);
      label = 'join code ' + port + ' (click to copy)';
      ctx.joinCodeBtn.title = label;
      ctx.joinCodeBtn.setAttribute('aria-label', label);
    }
    syncFooterLayout();
  }

  function copyJoinCode() {
    var port;
    var joinCode;

    function tryLegacyCopy(text) {
      var writeText = navigator.clipboard && navigator.clipboard.writeText;
      if (typeof writeText !== 'function')
        return Promise.resolve(false);
      return writeText.call(navigator.clipboard, text).then(function() {
        return true;
      }, function() {
        return false;
      });
    }

    syncJoinCode();
    port = ctx.joinCodePort | 0;
    if (port <= 0)
      return;
    joinCode = String(port);

    tryLegacyCopy(joinCode).then(function(copied) {
      if (copied) showInfoMessage('Join code copied to clipboard.', 1600);
      else showWarningMessage('Could not copy join code. Copy it manually.', 2200);
    });
  }

  Object.assign(ctx, {
    isUserFile,
    isCdFile,
    isCdDir,
    getBaseGameDir,
    getDefaultDirForConfig,
    getUploadDir,
    getCurrentUploadSettings,
    ensureCdDirs,
    setTabsOpen,
    setTabsOpenWidth,
    showInfoMessage,
    showWarningMessage,
    showErrorMessage,
    confirmAsync,
    noticeAsync,
    closeConfirmModal,
    clearStatusMessage,
    setUploadBusyState,
    syncConfigToggle,
    syncJoinCode,
    syncFooterLayout,
    copyJoinCode
  });

  if (ctx.configGlobalBtn) {
    ctx.configGlobalBtn.addEventListener('click', function() {
      Module.nqPerModConfig = !Module.nqPerModConfig;
      nqSavePerModConfig(Module.nqPerModConfig);
      if (Module.nqPerModConfig)
        showInfoMessage('Switched to per-mod cfg files.', 2200);
      else
        showInfoMessage('Switched to shared cfg files for all mods.', 2400);
      ctx.currentDir = getDefaultDirForConfig();
      if (ctx.panel.classList.contains('open')) ctx.refresh();
    });
  }
})();
