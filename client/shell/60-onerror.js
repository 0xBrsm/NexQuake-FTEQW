window.onerror = function() {
  // During boot (game not started yet), keep the loader on screen with the
  // failure visible — hiding it left a blank page with feedback only in
  // devtools (the status element lives inside the loader). Errors after
  // startup just log. Keyed off nqGameStarted, not the loader's CSS state,
  // so loader presentation can change without breaking this classification.
  var booting = !nqGameStarted;
  Module.setStatus('exception thrown — reload the page to retry');
  Module.setStatus = function(text) {
    if (text) console.error('[post-exception status] ' + text);
  };
  if (!booting && loaderElement)
    loaderElement.classList.add('hidden');
};
