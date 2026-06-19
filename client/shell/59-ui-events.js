// nq-ui: event wiring and runtime boot
(function() {
  if (!Module || !Module.nqOverlayCtx) return;
  var ctx = Module.nqOverlayCtx;

    var OVERLAY_WIDTH_MAX = 340;
    var OVERLAY_WIDTH_MIN = 280;
    var OPTIONS_MENU_BASE_WIDTH = 320;
    var OPTIONS_MENU_RIGHT_EDGE = 308;
    var OPTIONS_MENU_CLEARANCE = 8;

    function isOverlayOpen() {
      return ctx.panel.classList.contains('open') || ctx.editor.classList.contains('open');
    }

    function isOverlayEl(el) {
      return !!el && (
        ctx.panel.contains(el) ||
        ctx.editor.contains(el) ||
        ctx.textEntry.contains(el) ||
        el === ctx.toggle ||
        el === ctx.closeButton
      );
    }

    function syncOverlayToggleState(panelOpen) {
      document.body.classList.toggle('nq-overlay-panel-open', !!panelOpen);
      ctx.toggle.classList.toggle('active', !!panelOpen);
    }

    function getViewportWidth() {
      var viewport = window.visualViewport;
      if (viewport && viewport.width)
        return viewport.width;
      return window.innerWidth || document.documentElement.clientWidth || 0;
    }

    function getVideoWidth() {
      var width = 0;
      if (!Module || !Module.ccall || !Module.calledRun)
        return 0;
      width = nqWasmGetVideoWidth();
      return width > 0 ? width : 0;
    }

    function getOptionsMenuSafeWidth() {
      var viewportWidth = getViewportWidth();
      var canvasRect;
      var videoWidth;
      var menuRight;
      var menuRightInViewport;
      var available;
      if (!viewportWidth || typeof canvasElement === 'undefined' || !canvasElement || !canvasElement.getBoundingClientRect)
        return 0;
      canvasRect = canvasElement.getBoundingClientRect();
      if (!canvasRect || canvasRect.width <= 0)
        return 0;
      videoWidth = getVideoWidth();
      if (!videoWidth)
        return 0;
      menuRight = ((videoWidth - OPTIONS_MENU_BASE_WIDTH) * 0.5) + OPTIONS_MENU_RIGHT_EDGE;
      if (menuRight < 0)
        menuRight = 0;
      if (menuRight > videoWidth)
        menuRight = videoWidth;
      menuRightInViewport = canvasRect.left + (canvasRect.width * (menuRight / videoWidth));
      available = Math.floor(viewportWidth - menuRightInViewport - OPTIONS_MENU_CLEARANCE);
      return available > 0 ? available : 0;
    }

    function syncOverlayPanelWidth() {
      var viewportWidth = getViewportWidth();
      var width;
      var safeWidth;
      var minDesktopWidth;

      if (Module && Module.nqIsTouchInput) {
        width = Math.floor(viewportWidth);
      } else {
        width = Math.floor(Math.min(OVERLAY_WIDTH_MAX, viewportWidth));
        minDesktopWidth = Math.floor(Math.min(OVERLAY_WIDTH_MIN, viewportWidth));
        if (ctx.panel.classList.contains('open')) {
          safeWidth = getOptionsMenuSafeWidth();
          if (safeWidth > 0)
            width = Math.min(width, safeWidth);
        }
        width = Math.max(width, minDesktopWidth);
      }
      if (!Number.isFinite(width) || width <= 0) {
        ctx.panel.style.removeProperty('width');
        if (ctx.syncFooterLayout)
          ctx.syncFooterLayout();
        return;
      }
      ctx.panel.style.width = width + 'px';
      if (ctx.syncFooterLayout)
        ctx.syncFooterLayout();
    }

    function setPanelOpen(open) {
      if (open) {
        ctx.panel.classList.add('open');
        if (document.pointerLockElement) document.exitPointerLock();
        ctx.refresh();
      } else {
        ctx.panel.classList.remove('open');
        ctx.setTabsOpen(false);
        if (ctx.closeConfirmModal)
          ctx.closeConfirmModal(false);
        ctx.closeEditor();
        closeGameMenuFromOverlayExit();
      }
      syncOverlayPanelWidth();
      if (Module && Module.nexquakeTouchEnabled !== false && Module.nqTouchActive) {
        if (open && typeof Module.nqTouchPrepareOverlayMode === 'function')
          Module.nqTouchPrepareOverlayMode();
        if (!open && typeof Module.nqTouchEnsureLandscapeFullscreen === 'function')
          Module.nqTouchEnsureLandscapeFullscreen();
      }
      ctx.syncModalOpen();
      syncOverlayToggleState(open);
    }

    function dismissOverlayForGameplay() {
      if (isOverlayOpen())
        setPanelOpen(false);
    }

    function openGameOptionsMenu() {
      // Pre-boot the engine has no menu to open, and a queued menu_options
      // would pop the options menu over whatever runs at startup.
      if (!Module || !Module.ccall || !Module.calledRun)
        return;
      nqWasmExecCommand('menu_options');
    }

    function closeGameMenuFromOverlayExit() {
      if (!Module || !Module.ccall || !Module.calledRun)
        return;
      if (!Module.nqTouchMenuMode)
        return;
      nqWasmExecCommand('togglemenu');
      nqWasmExecCommand('togglemenu');
    }

    function isTextInputEl(el) {
      var tag;
      if (!el)
        return false;
      tag = (el.tagName || '').toLowerCase();
      if (tag === 'input' || tag === 'textarea' || tag === 'select')
        return true;
      return !!el.isContentEditable;
    }

    function preventOverlayControlFocus(ev) {
      var focusEl = ev.target;
      if (!focusEl || isTextInputEl(focusEl))
        return;
      ev.preventDefault();
    }

    function stopPropagation(ev) {
      ev.stopPropagation();
    }

    var overlayMouseTargets = [ctx.panel, ctx.toggle, ctx.editor, ctx.textEntry, ctx.closeButton].filter(Boolean);
    ['mousedown', 'mouseup', 'mousemove', 'click', 'dblclick', 'contextmenu'].forEach(function(eventName) {
      overlayMouseTargets.forEach(function(el) {
        el.addEventListener(eventName, stopPropagation);
      });
    });
    overlayMouseTargets.forEach(function(el) {
      el.addEventListener('wheel', stopPropagation, { passive: true });
    });
    window.addEventListener('resize', syncOverlayPanelWidth, { passive: true });
    window.addEventListener('orientationchange', syncOverlayPanelWidth, { passive: true });
    if (window.visualViewport) {
      window.visualViewport.addEventListener('resize', syncOverlayPanelWidth, { passive: true });
      window.visualViewport.addEventListener('scroll', syncOverlayPanelWidth, { passive: true });
    }

    document.addEventListener('keydown', function(ev) {
      var keyEv = /** @type {KeyboardEvent} */ (ev);
      if (keyEv.key !== 'Escape')
        return;
      if (Module.nqTextEntryOpen)
        ctx.dismissTextEntry();
      else if (!isOverlayOpen())
        return;
      else if (!(ctx.closeConfirmModal && ctx.closeConfirmModal(false)))
        ctx.closeEditor() || setPanelOpen(false);
      if (keyEv.cancelable)
        keyEv.preventDefault();
      keyEv.stopImmediatePropagation();
    }, true);

    ['keydown', 'keyup'].forEach(function(eventName) {
      document.addEventListener(eventName, function(ev) {
        if (!ctx.editor.classList.contains('open'))
          return;
        ev.stopImmediatePropagation();
      }, true);
    });

    ctx.panel.addEventListener('mousedown', preventOverlayControlFocus, true);
    ctx.editor.addEventListener('mousedown', preventOverlayControlFocus, true);
    function blurNonTextFocusin(ev) {
      if (ev.target !== ctx.editorText) ev.target.blur();
    }
    ctx.panel.addEventListener('focusin', blurNonTextFocusin);
    ctx.editor.addEventListener('focusin', blurNonTextFocusin);
    ctx.toggle.addEventListener('focus', function() {
      ctx.toggle.blur();
    });
    if (ctx.closeButton) {
      ctx.closeButton.addEventListener('focus', function() {
        ctx.closeButton.blur();
      });
    }

    document.addEventListener('mousedown', function(ev) {
      if (isOverlayEl(ev.target))
        ev.stopImmediatePropagation();
    }, true);

    document.addEventListener('click', function(ev) {
      var target = /** @type {Element|null} */ (ev.target);
      if (ev.target === ctx.toggle || ctx.toggle.contains(ev.target)) {
        ev.preventDefault();
        if (!ctx.panel.classList.contains('open')) {
          setPanelOpen(true);
          openGameOptionsMenu();
        }
        return;
      }
      if (ctx.closeButton && (ev.target === ctx.closeButton || ctx.closeButton.contains(ev.target))) {
        ev.preventDefault();
        if (ctx.panel.classList.contains('open'))
          setPanelOpen(false);
        return;
      }
      if (target.closest('#nq-dir-header') ||
          target.closest('#nq-tabs-wrap') ||
          target.closest('#nq-vfs-list li[data-path]'))
        return;
      ctx.setTabsOpen(false);
    }, true);

    if (typeof canvasElement !== 'undefined' && canvasElement)
      canvasElement.addEventListener('mousedown', dismissOverlayForGameplay, true);

    document.addEventListener('pointerlockchange', function() {
      if (typeof canvasElement !== 'undefined' && document.pointerLockElement === canvasElement)
        dismissOverlayForGameplay();
    });


  function installActionHandlers() {
  ctx.editorSave.onclick = function() {
    if (!ctx.editingFile) return;
    var data = new TextEncoder().encode(ctx.editorText.value);
    try {
      FS.writeFile(ctx.editingFile.backup, data);
      ctx.safeSyncFS();
      ctx.closeEditor();
      ctx.refresh();
    } catch (e) {
      console.error('Save failed:', e);
    }
  };

  ctx.editorCancel.onclick = ctx.closeEditor;
  ctx.upload.onclick = function() { if (!ctx.uploadBusy) ctx.fileInput.click(); };
  if (ctx.joinCodeBtn) {
    ctx.joinCodeBtn.onclick = function(ev) {
      ev.preventDefault();
      ctx.copyJoinCode();
    };
  }

  ctx.cdButtons.forEach(function(btn) {
    btn.onclick = function() {
      ctx.runCdCommand(btn.getAttribute('data-cd-command'))
        .catch(function(err) {
          ctx.showErrorMessage('CD command failed', 3000);
          console.error('CD command failed:', err);
        });
    };
  });

  ctx.fileInput.onchange = function(e) {
    var settings = ctx.getCurrentUploadSettings();
    settings.refreshOnSuccess = true;
    ctx.processUploads(e.target.files, settings).catch(function(err) {
      ctx.showErrorMessage('Upload failed', 3000);
      console.error('Upload queue failed:', err);
    });
    ctx.fileInput.value = '';
  };

  var dragSourcePath = '';
  var dragLi = null;
  var dropTargetBtn = null;

  function clearDropTarget() {
    if (!dropTargetBtn)
      return;
    dropTargetBtn.classList.remove('nq-drop-target');
    dropTargetBtn = null;
  }

  ctx.list.addEventListener('dragstart', function(ev) {
    var li = ev.target.closest('li[data-path]');
    var path = li ? (li.getAttribute('data-path') || '') : '';
    if (!li || !path || ctx.isCdDir(ctx.currentDir))
      return;
    if (ev.target.closest('button'))
      return;

    dragSourcePath = path;
    dragLi = li;
    li.classList.add('nq-dragging');
    ctx.setTabsOpen(true);
    if (ev.dataTransfer) {
      ev.dataTransfer.effectAllowed = 'move';
      try { ev.dataTransfer.setData('text/nq-file-path', path); } catch (e1) {}
      try { ev.dataTransfer.setData('text/plain', path); } catch (e2) {}
    }
  });

  ctx.list.addEventListener('dragend', function() {
    if (dragLi)
      dragLi.classList.remove('nq-dragging');
    dragLi = null;
    dragSourcePath = '';
    clearDropTarget();
  });

  ctx.tabs.addEventListener('click', function(ev) {
    var btn = ev.target.closest('button.nq-tab');
    var dir = btn ? (btn.dataset.dir || '') : '';
    if (!dir || dir === ctx.currentDir)
      return;
    ctx.currentDir = dir;
    ctx.refresh();
  });
  function toggleDirTabs(ev) {
    var rawTarget = /** @type {Node|null} */ (ev.target);
    var target = rawTarget && rawTarget.nodeType === 1
      ? /** @type {Element} */ (rawTarget)
      : (rawTarget && rawTarget.parentElement ? rawTarget.parentElement : null);
    if (!target || ctx.isCdDir(ctx.currentDir) || target.closest('#nq-overlay-close'))
      return;
    ev.preventDefault();
    if (Module && Module.nqIsTouchInput) {
      if (ctx.dirLabel && ctx.dirLabel.contains(target) && typeof ctx.cycleDir === 'function')
        ctx.cycleDir(1);
      return;
    }
    ctx.setTabsOpen(!ctx.tabsWrap.classList.contains('open'));
  }

  if (ctx.dirHeader)
    ctx.dirHeader.addEventListener('click', toggleDirTabs);

  ctx.tabs.addEventListener('dragover', function(ev) {
    var btn;
    var dir;
    if (!dragSourcePath)
      return;
    btn = ev.target.closest('button.nq-tab');
    dir = btn ? (btn.dataset.dir || '') : '';
    if (!btn || !dir) {
      clearDropTarget();
      return;
    }
    ev.preventDefault();
    if (dropTargetBtn !== btn) {
      clearDropTarget();
      dropTargetBtn = btn;
      dropTargetBtn.classList.add('nq-drop-target');
    }
  });

  ctx.tabs.addEventListener('dragleave', function(ev) {
    var rt = ev.relatedTarget;
    if (rt && ctx.tabs.contains(rt))
      return;
    clearDropTarget();
  });

  ctx.tabs.addEventListener('drop', function(ev) {
    var btn = ev.target.closest('button.nq-tab');
    var dir = btn ? (btn.dataset.dir || '') : '';
    var src = dragSourcePath;
    if (!src || !dir)
      return;
    ev.preventDefault();
    if (ev.dataTransfer)
      src = ev.dataTransfer.getData('text/nq-file-path') || ev.dataTransfer.getData('text/plain') || src;
    if (dragLi)
      dragLi.classList.remove('nq-dragging');
    dragLi = null;
    dragSourcePath = '';
    clearDropTarget();
    ctx.moveFileToDir(src, dir);
  });

  ctx.list.addEventListener('click', function(ev) {
    var btn = ev.target.closest('button');
    var cdRow = ev.target.closest('li[data-cd-track-path]');
    var cdTrackPath;
    var cdForceDisabled;
    var li;
    var displayPath;
    if (cdRow && ctx.list.contains(cdRow)) {
      cdTrackPath = cdRow.getAttribute('data-cd-track-path') || '';
      if (cdTrackPath) {
        cdForceDisabled = cdRow.getAttribute('data-cd-track-disabled') === '1';
        if (btn && btn.classList.contains('nq-cd-track-toggle')) {
          ctx.toggleCdTrack(cdTrackPath, cdForceDisabled);
          return;
        }
        if (!btn && !ev.target.closest('.nq-file-actions')) {
          ctx.toggleCdTrack(cdTrackPath, cdForceDisabled);
          return;
        }
      }
    }

    li = ev.target.closest('li[data-path]');
    displayPath = li ? (li.getAttribute('data-path') || '') : '';

    if (!btn) {
      if (!displayPath)
        ctx.setTabsOpen(false);
      return;
    }

    if (!ctx.list.contains(btn))
      return;
    if (!displayPath)
      return;

    if (btn.classList.contains('nq-dl')) {
      Module.exportFile(displayPath);
    } else if (btn.classList.contains('nq-exec')) {
      ctx.execCfgFile(displayPath);
    } else if (btn.classList.contains('nq-del')) {
      ctx.requestDeleteFile(displayPath)
        .catch(function(err) {
          ctx.showErrorMessage('Delete failed', 3000);
          console.error('Delete failed:', err);
        });
    } else if (btn.classList.contains('nq-edit')) {
      ctx.openEditor(displayPath);
    }
  });

  ctx.list.addEventListener('dblclick', function(ev) {
    if (Module && Module.nqIsTouchInput)
      return;
    var li = ev.target.closest('li[data-path]');
    var displayPath = li ? (li.getAttribute('data-path') || '') : '';
    if (!displayPath || ctx.isCdDir(ctx.currentDir))
      return;
    if (ev.target.closest('button'))
      return;
    if (!displayPath.toLowerCase().endsWith('.cfg'))
      return;
    ctx.openEditor(displayPath);
  });
  }

  installActionHandlers();
    ctx.setPanelOpen = setPanelOpen;
    ctx.dismissOverlayForGameplay = dismissOverlayForGameplay;
    ctx.syncOverlayPanelWidth = syncOverlayPanelWidth;
    syncOverlayToggleState(ctx.panel.classList.contains('open'));
    syncOverlayPanelWidth();

    var originalInit = Module.onRuntimeInitialized;
    ctx.currentDir = ctx.getDefaultDirForConfig();
    ctx.setCdEnabled(!!nqGameStarted && ctx.cdPreferredEnabled, false);
    ctx.syncModalOpen();

    Module.onRuntimeInitialized = function() {
      if (originalInit) originalInit.call(Module);
      ctx.refresh();
    };
    Module.nqOverlayUpdateDirs = function() {
      ctx.refresh();
    };
})();
