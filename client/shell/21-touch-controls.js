// nq-touch-controls: mobile touch HUD visibility and runtime control
(function() {
  var overlay = document.getElementById('nq-touch-overlay');
  var buttonsRoot = document.getElementById('nq-touch-buttons');
  var bindableSlots = [1, 2, 3, 4, 5, 6, 7, 8, 9];
  if (!overlay || !buttonsRoot) return;

  var moduleRef = (typeof Module !== 'undefined') ? Module : (window.Module = {});
  var storageKey = 'nexquake.touch.layout.v1';
  var dragState = null;
  var touchIdleMs = 2500;
  var lastTouchMs = Date.now();
  var touchHeld = false;
  var boundBySlot = {};
  var customLayout = {};
  var bindingGlyphMap = {
    '+forward': '↑',
    '+back': '↓',
    '+moveleft': '←',
    '+moveright': '→',
    'togglemenu': 'Q',
    'toggleconsole': '~'
  };

  function clamp01(v) {
    if (!Number.isFinite(v)) return 0;
    if (v < 0) return 0;
    if (v > 1) return 1;
    return v;
  }

  function point(x, y) {
    return {
      x: clamp01(x),
      y: clamp01(y)
    };
  }

  function displayXFromLayoutX(x, flip) {
    x = clamp01(x);
    return flip ? (1 - x) : x;
  }

  function layoutXFromDisplayX(x, flip) {
    x = clamp01(x);
    return flip ? (1 - x) : x;
  }

  function buildDefaultLayout(buttonBySlot, layoutSlots, fixedMenuAnchor) {
    var w = Math.max(window.innerWidth || 0, 1);
    var h = Math.max(window.innerHeight || 0, 1);
    var css;
    var size;
    var margin;
    var crossStep;
    var edgePad;
    var mLeftX;
    var mRightX;
    var mTopY;
    var cx;
    var cy;
    var touch4Y;
    var midY;
    var offset;
    if (buttonBySlot[layoutSlots[0]]) {
      try { css = window.getComputedStyle(buttonBySlot[layoutSlots[0]]); } catch (e) {}
    }
    size = parseFloat(css && css.width);
    if (!(Number.isFinite(size) && size > 0)) size = 62;
    margin = size * 0.58;
    crossStep = size * 0.90;
    edgePad = (size * 0.5) + 8;
    mLeftX = clamp01(edgePad / w);
    mRightX = clamp01(1 - (edgePad / w));
    mTopY = clamp01(edgePad / h);
    fixedMenuAnchor.left = mLeftX;
    fixedMenuAnchor.right = mRightX;
    fixedMenuAnchor.top = mTopY;
    cx = 1 - ((margin + crossStep) / w);
    cy = 1 - ((margin + crossStep) / h);
    touch4Y = clamp01(cy - (crossStep / h));
    midY = clamp01(mTopY + ((touch4Y - mTopY) * 0.5));
    offset = Math.max(0, midY - mTopY);

    return {
      1: point(cx, cy + (crossStep / h)),
      2: point(cx + (crossStep / w), cy),
      3: point(cx - (crossStep / w), cy),
      4: point(cx, touch4Y),
      5: point(mRightX, midY),
      6: point(mRightX - offset, mTopY),
      7: point(mLeftX + offset, mTopY),
      8: point(mLeftX, midY),
      9: point(mLeftX, clamp01(midY + ((1 - midY) * 0.5)))
    };
  }

  function loadLayout(storageKey, layoutSlots, defaultLayout, customLayout) {
    var raw;
    var parsed;
    var merged = {};

    try { raw = localStorage.getItem(storageKey); } catch (e) {}
    if (raw) {
      try { parsed = JSON.parse(raw); } catch (e2) {}
    }

    layoutSlots.forEach(function(slot) {
      var base = defaultLayout[slot] || { x: 0.5, y: 0.5 };
      var val = parsed && parsed[slot];
      var x = Number(val && val.x);
      var y = Number(val && val.y);
      var hasX = Number.isFinite(x);
      var hasY = Number.isFinite(y);
      customLayout[slot] = hasX && hasY;
      merged[slot] = {
        x: hasX ? clamp01(x) : base.x,
        y: hasY ? clamp01(y) : base.y
      };
    });

    return merged;
  }

  function normalizeBinding(binding) {
    return (binding || '')
      .split(';', 1)[0]
      .trim()
      .toLowerCase()
      .replace(/\s+/g, ' ');
  }

  function ensureLandscapeFullscreen() {
    var root = document.documentElement;
    var fullscreenEl = document.fullscreenElement || document.webkitFullscreenElement;
    var requestFullscreen = root.requestFullscreen || root.webkitRequestFullscreen;
    var request;
    var lockRequest;

    if (!fullscreenEl && requestFullscreen) {
      try {
        request = requestFullscreen.call(root, { navigationUI: 'hide' });
      } catch (e1) {
        try {
          request = requestFullscreen.call(root);
        } catch (e2) {}
      }
      if (request && request.catch)
        request.catch(function() {});
    }

    try {
      if (screen.orientation && screen.orientation.lock) {
        lockRequest = screen.orientation.lock('landscape');
        if (lockRequest && lockRequest.catch)
          lockRequest.catch(function() {});
      }
    } catch (e3) {}
  }

  function prepareOverlayMode() {
    try {
      if (screen.orientation && screen.orientation.unlock)
        screen.orientation.unlock();
    } catch (e1) {}
  }

  var isTouchInput = (function() {
    if (window.matchMedia && window.matchMedia('(pointer: coarse)').matches)
      return true;
    if ('ontouchstart' in window && screen.width <= 1024)
      return true;
    return false;
  })();
  moduleRef.nqIsTouchInput = isTouchInput;

  function syncBodyTouchClasses() {
    if (!document || !document.body)
      return;
    document.body.classList.toggle('nq-touch-input', !!isTouchInput);
    if (isTouchInput)
      document.body.classList.toggle('nq-touch-portrait', !isLandscapeOrientation());
    else
      document.body.classList.remove('nq-touch-portrait');
  }
  syncBodyTouchClasses();

  var fixedMenuAnchor = { left: 0.04, right: 0.96, top: 0.05 };
  var buttonBySlot = {};

  Array.prototype.slice.call(
    buttonsRoot.querySelectorAll('.nq-touch-btn[data-touch-slot]')
  ).forEach(function(btn) {
    var slot = Number(btn.getAttribute('data-touch-slot')) | 0;
    buttonBySlot[slot] = btn;
  });

  var menuBackButton = document.getElementById('nq-touch-menu-m1');
  var menuOverlayButton = document.getElementById('nq-touch-menu-m2');
  var layoutSlots = bindableSlots.filter(function(slot) {
    return !!buttonBySlot[slot];
  });
  var defaultLayout = buildDefaultLayout(buttonBySlot, layoutSlots, fixedMenuAnchor);
  var layout = loadLayout(storageKey, layoutSlots, defaultLayout, customLayout);

  moduleRef.nqTouchButtonElementIds = bindableSlots.map(function(slot) {
    return buttonBySlot[slot] ? buttonBySlot[slot].id : ('nq-touch-missing-slot' + slot);
  });
  moduleRef.nqTouchButtonElementIds.push(menuBackButton ? menuBackButton.id : 'nq-touch-missing-menu-m1');

  moduleRef.nqTouchDragActive = false;
  moduleRef.nqTouchControlsVisible = false;
  moduleRef.nqTouchMenuMode = !!moduleRef.nqTouchMenuMode;
  moduleRef.nqTouchMenuLayoutMode = !!moduleRef.nqTouchMenuLayoutMode;
  moduleRef.nqTouchFlip = !!moduleRef.nqTouchFlip;

  if (!isTouchInput) {
    overlay.style.display = 'none';
    return;
  }

  moduleRef.nqTouchActive = true;
  var resumeOverlayPending = false;

  overlay.addEventListener('contextmenu', function(e) { e.preventDefault(); });
  ['orientationchange', 'resize'].forEach(function(type) {
    window.addEventListener(type, onViewportChange, { passive: true });
  });
  ['touchstart', 'touchmove', 'touchend', 'touchcancel'].forEach(function(type) {
    document.addEventListener(type, onTouchActivity, { passive: true, capture: true });
  });

  layoutSlots.forEach(function(slot) {
    bindDragHandlers(buttonBySlot[slot], slot);
  });

  if (menuOverlayButton) {
    ['pointerdown', 'pointerup', 'pointercancel', 'touchstart', 'touchend'].forEach(function(type) {
      menuOverlayButton.addEventListener(type, function(ev) {
        ev.stopPropagation();
      }, true);
    });
    menuOverlayButton.addEventListener('click', function(ev) {
      var ctx = moduleRef.nqOverlayCtx;
      var opening;
      if (!ctx || typeof ctx.setPanelOpen !== 'function' || !ctx.panel)
        return;
      ev.preventDefault();
      ev.stopPropagation();
      opening = !ctx.panel.classList.contains('open');
      ctx.setPanelOpen(opening);
      if (opening && moduleRef.ccall && moduleRef.calledRun)
        nqWasmExecCommand('menu_options');
      syncFixedButtons();
      syncVisibility();
    }, true);
  }

  applyLayout();
  refreshBoundVisibility();
  syncFixedButtons();
  syncVisibility();

  function isLandscapeOrientation() {
    if (window.matchMedia)
      return window.matchMedia('(orientation: landscape)').matches;
    return window.innerWidth >= window.innerHeight;
  }

  function tryOpenOverlayAfterResume() {
    var ctx = moduleRef.nqOverlayCtx;
    if (!ctx || typeof ctx.setPanelOpen !== 'function' || !ctx.panel)
      return false;
    if (!ctx.panel.classList.contains('open'))
      ctx.setPanelOpen(true);
    return true;
  }

  document.addEventListener('visibilitychange', function() {
    if (document.visibilityState === 'hidden') {
      resumeOverlayPending = true;
      return;
    }
    syncVisibility();
  }, { passive: true });

  window.addEventListener('pageshow', function() {
    syncVisibility();
  }, { passive: true });

  moduleRef.nqTouchEnsureLandscapeFullscreen = ensureLandscapeFullscreen;
  moduleRef.nqTouchPrepareOverlayMode = prepareOverlayMode;
  moduleRef.nqTouchControlsRefresh = syncVisibility;

  function saveLayout() {
    try { localStorage.setItem(storageKey, JSON.stringify(layout)); } catch (e) {}
  }

  function applyLayout() {
    layoutSlots.forEach(function(slot) {
      var btn = buttonBySlot[slot];
      var cfg = layout[slot] || defaultLayout[slot];
      var displayX;
      if (!btn || !cfg) return;
      displayX = displayXFromLayoutX(cfg.x, moduleRef.nqTouchFlip);
      btn.style.left = (displayX * 100) + '%';
      btn.style.top = (cfg.y * 100) + '%';
      btn.classList.toggle('nq-touch-btn-hidden', boundBySlot[slot] === false);
    });
  }

  // K_AUX13..K_AUX21 (219..227) for slots 1-9
  var slotKeyCodes = [0, 219, 220, 221, 222, 223, 224, 225, 226, 227];

  function getSlotBinding(slot) {
    var key;
    if (!moduleRef.ccall || !moduleRef.calledRun) return null;
    key = slotKeyCodes[slot] || 0;
    return key ? nqWasmGetKeyBinding(key) || '' : '';
  }

  function setSlotGlyph(slot, binding) {
    var btn = buttonBySlot[slot];
    var norm;
    var svg;
    var text;
    if (!btn) return;
    norm = normalizeBinding(binding);
    svg = window.NQ_TOUCH_GLYPHS && window.NQ_TOUCH_GLYPHS[norm];
    if (svg) {
      btn.innerHTML = svg;
      btn.classList.add('nq-touch-btn-glyph');
      return;
    }
    if (!norm) {
      btn.textContent = String(slot);
      btn.classList.add('nq-touch-btn-glyph');
      return;
    }
    text = bindingGlyphMap[norm] || '';
    btn.textContent = text;
    btn.classList.toggle('nq-touch-btn-glyph', !!text);
  }

  function refreshBoundVisibility() {
    var changed = false;
    if (!moduleRef.ccall || !moduleRef.calledRun)
      return;

    bindableSlots.forEach(function(slot) {
      var binding = getSlotBinding(slot);
      var bound;
      if (binding === null)
        return;
      bound = !!binding;

      setSlotGlyph(slot, binding);
      if (boundBySlot[slot] !== bound) {
        boundBySlot[slot] = bound;
        changed = true;
      }
    });

    if (changed)
      applyLayout();
  }

  function syncFixedButtons() {
    var panelOpen;
    var leftX;
    var rightX;

    if (menuBackButton) {
      menuBackButton.textContent = 'Q';
      menuBackButton.classList.add('nq-touch-btn-glyph');
    }

    panelOpen = !!(moduleRef.nqOverlayCtx &&
      moduleRef.nqOverlayCtx.panel &&
      moduleRef.nqOverlayCtx.panel.classList.contains('open'));

    if (menuOverlayButton)
      menuOverlayButton.classList.toggle('active', panelOpen);

    leftX = displayXFromLayoutX(fixedMenuAnchor.left, moduleRef.nqTouchFlip);
    rightX = displayXFromLayoutX(fixedMenuAnchor.right, moduleRef.nqTouchFlip);

    if (menuBackButton) {
      menuBackButton.style.left = (leftX * 100) + '%';
      menuBackButton.style.top = (fixedMenuAnchor.top * 100) + '%';
    }
    if (menuOverlayButton) {
      menuOverlayButton.style.left = (rightX * 100) + '%';
      menuOverlayButton.style.top = (fixedMenuAnchor.top * 100) + '%';
    }
  }

  function onTouchActivity(ev) {
    touchHeld = !!(ev.touches && ev.touches.length);
    lastTouchMs = Date.now();
    syncVisibility();
  }

  function onViewportChange() {
    defaultLayout = buildDefaultLayout(buttonBySlot, layoutSlots, fixedMenuAnchor);
    layoutSlots.forEach(function(slot) {
      if (customLayout[slot]) return;
      layout[slot].x = defaultLayout[slot].x;
      layout[slot].y = defaultLayout[slot].y;
    });
    applyLayout();
    syncVisibility();
    syncBodyTouchClasses();
  }

  function syncVisibility() {
    if (resumeOverlayPending && document.visibilityState === 'visible') {
      resumeOverlayPending = false;
      // An rcon login round-trip backgrounds the tab (the IdP popup); don't pop
      // the overlay on return — the login flow shows its own toast whose OK
      // click restores fullscreen, landing the user back where they started
      // (moduleRef.nqRconLoginActive is set by the rcon shell during login).
      if (!moduleRef.nqRconLoginActive && !tryOpenOverlayAfterResume())
        resumeOverlayPending = true;
    }

    var canvasShown = canvasElement && canvasElement.style.display === 'block';
    var landscape = isLandscapeOrientation();
    var overlayCtx = moduleRef && moduleRef.nqOverlayCtx;
    var panelOpen = !!(overlayCtx && overlayCtx.panel && overlayCtx.panel.classList.contains('open'));
    var editorOpen = !!(overlayCtx && overlayCtx.editor && overlayCtx.editor.classList.contains('open'));
    // The confirm/notice modal is top-level (it can be open with the panel
    // closed — e.g. the rcon login toast), so it must suppress the touch layer
    // on its own, otherwise its buttons aren't tappable in landscape.
    var confirmOpen = !!(overlayCtx && overlayCtx.confirm && overlayCtx.confirm.classList.contains('open'));
    var textEntryOpen = !!(moduleRef && moduleRef.nqTextEntryOpen);
    var blockingModal = panelOpen || editorOpen || confirmOpen;
    var menuMode = !!moduleRef.nqTouchMenuMode;
    var menuLayoutMode = !!moduleRef.nqTouchMenuLayoutMode;
    var visible = moduleRef.nexquakeTouchEnabled !== false && canvasShown && landscape;
    var interactive = visible && !blockingModal;

    overlay.style.display = visible ? 'flex' : 'none';
    moduleRef.nqTouchControlsVisible = interactive;
    overlay.classList.toggle('nq-touch-menu-mode', visible && menuMode);
    overlay.classList.toggle('nq-touch-menu-layout-mode', visible && menuLayoutMode);
    overlay.classList.toggle('nq-touch-flip', visible && !!moduleRef.nqTouchFlip);
    overlay.classList.toggle('nq-touch-modal-open', visible && blockingModal);
    overlay.classList.toggle('nq-touch-text-entry-open', visible && textEntryOpen && !blockingModal);

    if ((!interactive || !menuLayoutMode) && dragState)
      endDrag();

    overlay.classList.toggle('nq-touch-idle', interactive && !touchHeld &&
      !moduleRef.nqTouchDragActive && (Date.now() - lastTouchMs) >= touchIdleMs);

    syncFixedButtons();
    syncBodyTouchClasses();
  }

  function setDragActive(active) {
    moduleRef.nqTouchDragActive = !!active;
    overlay.classList.toggle('nq-touch-layout-edit', !!active);
  }

  function beginDrag(button, slot, pointerId, clientX, clientY) {
    var cfg = layout[slot] || defaultLayout[slot];
    var displayX;
    if (!cfg) return;
    displayX = displayXFromLayoutX(cfg.x, moduleRef.nqTouchFlip);

    dragState = {
      button: button,
      slot: slot,
      pointerId: pointerId,
      offsetX: (displayX * window.innerWidth) - clientX,
      offsetY: (cfg.y * window.innerHeight) - clientY
    };

    setDragActive(true);
    button.classList.add('dragging');
  }

  function endDrag(pointerId) {
    if (!dragState) return;
    if (pointerId != null && dragState.pointerId !== pointerId)
      return;

    dragState.button.classList.remove('dragging');
    customLayout[dragState.slot] = true;
    dragState = null;
    setDragActive(false);
    saveLayout();
  }

  function bindDragHandlers(button, slot) {
    var holdTimer = 0;
    var startX = 0;
    var startY = 0;

    function clearHold() {
      if (!holdTimer) return;
      clearTimeout(holdTimer);
      holdTimer = 0;
    }

    function finishPointer(ev) {
      clearHold();
      endDrag(ev.pointerId);
      try { button.releasePointerCapture(ev.pointerId); } catch (e) {}
    }

    button.addEventListener('pointerdown', function(ev) {
      if (ev.pointerType === 'mouse' && ev.button !== 0)
        return;
      if (!moduleRef.nqTouchMenuLayoutMode)
        return;

      startX = ev.clientX;
      startY = ev.clientY;
      clearHold();
      holdTimer = setTimeout(function() {
        if (!moduleRef.nqTouchMenuLayoutMode)
          return;
        holdTimer = 0;
        beginDrag(button, slot, ev.pointerId, ev.clientX, ev.clientY);
        try { button.setPointerCapture(ev.pointerId); } catch (e) {}
      }, 350);
    }, { passive: true });

    button.addEventListener('pointermove', function(ev) {
      var dx;
      var dy;
      var x;
      var y;
      if (!dragState || dragState.pointerId !== ev.pointerId) {
        if (holdTimer) {
          dx = ev.clientX - startX;
          dy = ev.clientY - startY;
          if ((dx * dx + dy * dy) > 64)
            clearHold();
        }
        return;
      }

      ev.preventDefault();
      x = layoutXFromDisplayX((ev.clientX + dragState.offsetX) / window.innerWidth, moduleRef.nqTouchFlip);
      y = clamp01((ev.clientY + dragState.offsetY) / window.innerHeight);
      if (!layout[dragState.slot])
        return;
      layout[dragState.slot].x = x;
      layout[dragState.slot].y = y;
      applyLayout();
    }, { passive: false });

    ['pointerup', 'pointercancel'].forEach(function(type) {
      button.addEventListener(type, finishPointer, { passive: true });
    });

    button.addEventListener('lostpointercapture', function(ev) {
      clearHold();
      endDrag(ev.pointerId);
    });
  }

  var intervalTick = 0;
  setInterval(function() {
    syncVisibility();
    intervalTick = (intervalTick + 1) % 5;
    if (intervalTick === 0)
      refreshBoundVisibility();
  }, 200);

  // 50-ui.js calls this from syncModalOpen so the touch HUD reacts to modal
  // open/close immediately. (This file runs before 50-ui creates the overlay
  // ctx, so wrapping ctx.syncModalOpen here was never possible.)
  moduleRef.nqTouchSyncVisibility = syncVisibility;
})();
