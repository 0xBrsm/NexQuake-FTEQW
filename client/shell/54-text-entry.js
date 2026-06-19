// nq-ui: touch text entry bridge
(function() {
  if (!Module || !Module.nqOverlayCtx) return;
  var ctx = Module.nqOverlayCtx;
  var textEntryValue = '';

  function setTextEntryValue(value) {
    value = String(value || '');
    ctx.textEntryInput.value = value;
    textEntryValue = value;
  }

  function moveTextEntryCaretToEnd() {
    var end = textEntryValue.length;
    try {
      ctx.textEntryInput.setSelectionRange(end, end);
    } catch (e) {}
  }

  function normalizeTextEntryValue(value) {
    var normalized = '';
    var i, code;
    value = String(value || '');
    for (i = 0; i < value.length; i++) {
      code = value.charCodeAt(i);
      if (code >= 32 && code <= 127)
        normalized += value.charAt(i);
    }
    return normalized;
  }

  function syncTextEntryValueFromGame() {
    var gameValue;
    if (Module.nqConsoleTextEntryOpen) {
      setTextEntryValue('');
      return;
    }
    gameValue = normalizeTextEntryValue(nqWasmGetTextInputValue());
    if (gameValue === textEntryValue)
      return;
    setTextEntryValue(gameValue);
    if (document.activeElement === ctx.textEntryInput)
      moveTextEntryCaretToEnd();
  }

  function focusTextEntryInput() {
    ctx.textEntryInput.focus({ preventScroll: true });
  }

  function syncTextEntryMode() {
    if (!Module.nqTextEntryOpen)
      return;
    ctx.textEntryInput.placeholder = 'tap to type';
    if (Module.nqConsoleTextEntryOpen)
      setTextEntryValue('');
    else
      syncTextEntryValueFromGame();
    focusTextEntryInput();
  }

  function setTextEntryOpen(open) {
    open = !!open;
    Module.nqTextEntryOpen = open;
    ctx.textEntry.hidden = !open;
    if (open) {
      setTextEntryValue('');
      syncTextEntryMode();
    } else {
      setTextEntryValue('');
      ctx.textEntryInput.blur();
      ctx.textEntryInput.placeholder = '';
    }
    ctx.syncModalOpen();
  }

  function dismissTextEntry() {
    nqWasmTextInputKey(27);
    setTextEntryOpen(false);
  }

  function requestTextEntry() {
    setTextEntryOpen(Module.nqIsTouchInput);
  }

  ctx.textEntryInput.addEventListener('focus', function() {
    Module.nqTextEntryFocused = true;
  });
  ctx.textEntryInput.addEventListener('blur', function() {
    Module.nqTextEntryFocused = false;
  });

  ctx.textEntry.addEventListener('click', function(ev) {
    if (ev.target === ctx.textEntryInput || ctx.textEntryForm.contains(ev.target))
      return;
    ev.stopImmediatePropagation();
    ev.preventDefault();
    if (Module.nqMessageTextEntryOpen)
      dismissTextEntry();
    else if (Module.nqTextEntryFocused)
      ctx.textEntryInput.blur();
    else
      setTextEntryOpen(false);
  });

  ctx.textEntryForm.addEventListener('click', function() {
    if (!Module.nqTextEntryFocused)
      focusTextEntryInput();
  });

  ctx.textEntryInput.addEventListener('input', function() {
    var previousValue = textEntryValue;
    var nextValue = normalizeTextEntryValue(ctx.textEntryInput.value);
    var i, code;

    if (ctx.textEntryInput.value !== nextValue)
      ctx.textEntryInput.value = nextValue;
    if (nextValue === previousValue)
      return;

    if (nextValue.indexOf(previousValue) === 0) {
      for (i = previousValue.length; i < nextValue.length; i++) {
        code = nextValue.charCodeAt(i);
        if (code >= 32 && code <= 127)
          nqWasmTextInputKey(code);
      }
    } else if (previousValue.indexOf(nextValue) === 0) {
      for (i = 0; i < previousValue.length - nextValue.length; i++)
        nqWasmTextInputKey(127);
    } else {
      for (i = 0; i < previousValue.length; i++)
        nqWasmTextInputKey(127);
      for (i = 0; i < nextValue.length; i++) {
        code = nextValue.charCodeAt(i);
        if (code >= 32 && code <= 127)
          nqWasmTextInputKey(code);
      }
    }
    textEntryValue = nextValue;
  });

  ctx.textEntryForm.addEventListener('submit', function(ev) {
    ev.preventDefault();
    nqWasmTextInputKey(13);
    setTextEntryValue('');
    if (!Module.nqMessageTextEntryOpen)
      setTextEntryOpen(false);
  });

  ctx.requestTextEntry = requestTextEntry;
  ctx.closeTextEntry = function() { setTextEntryOpen(false); };
  ctx.dismissTextEntry = dismissTextEntry;
  ctx.syncTextEntryMode = syncTextEntryMode;
  ctx.syncTextEntryValueFromGame = syncTextEntryValueFromGame;
})();
