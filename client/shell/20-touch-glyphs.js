// nq-touch-glyphs: SVG icon map for touch button bindings.
// Keys must match normalizeBinding() output (lowercase, trimmed, single spaces).
// Each value is an inline SVG string using currentColor.
(function() {
  window.NQ_TOUCH_GLYPHS = {
    // --- Common actions ---

    // Crosshair: center dot with gapped reticle lines
    '+attack':
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">' +
        '<circle cx="12" cy="12" r="1.5" fill="currentColor" stroke="none"/>' +
        '<line x1="12" y1="2" x2="12" y2="8"/>' +
        '<line x1="12" y1="16" x2="12" y2="22"/>' +
        '<line x1="2" y1="12" x2="8" y2="12"/>' +
        '<line x1="16" y1="12" x2="22" y2="12"/>' +
      '</svg>',

    // Jump: parabolic arc trajectory with upward chevron at peak
    '+jump':
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">' +
        '<path d="M3 19Q12 -6 21 19"/>' +
        '<polyline points="8,6 12,2 16,6"/>' +
      '</svg>',

    // Next weapon: clockwise cycle arrow
    'impulse 10':
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
        '<polyline points="23 4 23 10 17 10"/>' +
        '<path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/>' +
      '</svg>',

    // Previous weapon: counter-clockwise cycle arrow
    'impulse 12':
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
        '<polyline points="1 4 1 10 7 10"/>' +
        '<path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/>' +
      '</svg>',

    // --- Quake weapons ---

    // impulse 1: Axe – double-bit axe head: concave top/bottom edges (narrow waist at eye),
    // rounded blade tips left and right, overall >< silhouette.
    // Y coords scaled 2x from center (12): old range 7–17 → new range 2–22.
    'impulse 1':
      '<svg viewBox="0 0 24 24" fill="currentColor">' +
        '<path d="M4 2 Q12 8 20 2 Q23 6 23 12 Q23 18 20 22 Q12 16 4 22 Q1 18 1 12 Q1 6 4 2 Z"/>' +
      '</svg>',

    // impulse 2: Shotgun – barrel end-on: circle centered in viewbox + bead sight above.
    // Attributes on elements directly (no SVG-level inheritance) to avoid browser quirks.
    'impulse 2':
      '<svg viewBox="0 0 24 24">' +
        '<circle cx="12" cy="12" r="8" fill="none" stroke="currentColor" stroke-width="3.5"/>' +
        '<circle cx="12" cy="2" r="1.5" fill="currentColor"/>' +
      '</svg>',

    // impulse 3: Super Shotgun – two barrel circles end-on, one bead sight per barrel
    'impulse 3':
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">' +
        '<circle cx="7" cy="14" r="5"/>' +
        '<circle cx="17" cy="14" r="5"/>' +
        '<circle cx="7" cy="7" r="1.5" fill="currentColor" stroke="none"/>' +
        '<circle cx="17" cy="7" r="1.5" fill="currentColor" stroke="none"/>' +
      '</svg>',

    // impulse 4: Nailgun – two nails, thick and stubby
    'impulse 4':
      '<svg viewBox="0 0 24 24" fill="currentColor">' +
        '<rect x="5.5" y="3" width="5" height="2.5" rx="0.5"/>' +
        '<rect x="7" y="5.5" width="2" height="10"/>' +
        '<polygon points="7,15.5 9,15.5 8,20"/>' +
        '<rect x="13.5" y="3" width="5" height="2.5" rx="0.5"/>' +
        '<rect x="15" y="5.5" width="2" height="10"/>' +
        '<polygon points="15,15.5 17,15.5 16,20"/>' +
      '</svg>',

    // impulse 5: Super Nailgun – three nails, thick and stubby, centers at 5/12/19
    'impulse 5':
      '<svg viewBox="0 0 24 24" fill="currentColor">' +
        '<rect x="2.5" y="3" width="5" height="2.5" rx="0.5"/>' +
        '<rect x="4" y="5.5" width="2" height="10"/>' +
        '<polygon points="4,15.5 6,15.5 5,20"/>' +
        '<rect x="9.5" y="3" width="5" height="2.5" rx="0.5"/>' +
        '<rect x="11" y="5.5" width="2" height="10"/>' +
        '<polygon points="11,15.5 13,15.5 12,20"/>' +
        '<rect x="16.5" y="3" width="5" height="2.5" rx="0.5"/>' +
        '<rect x="18" y="5.5" width="2" height="10"/>' +
        '<polygon points="18,15.5 20,15.5 19,20"/>' +
      '</svg>',

    // impulse 6: Grenade Launcher – classic grenade: oval body, neck, safety lever
    'impulse 6':
      '<svg viewBox="0 0 24 24" fill="currentColor">' +
        '<ellipse cx="12" cy="15" rx="7" ry="8"/>' +
        '<rect x="10" y="3" width="4" height="6" rx="1.5"/>' +
        '<rect x="14" y="4" width="4" height="2.5" rx="0.5"/>' +
      '</svg>',

    // impulse 7: Rocket Launcher – curved body, swept side fins, rounded nozzle
    'impulse 7':
      '<svg viewBox="0 0 24 24" fill="currentColor">' +
        '<path d="M12,2 Q15.5,6 15,9 L15,18 L9,18 L9,9 Q8.5,6 12,2 Z"/>' +
        '<polygon points="9,13 5,20 9,19"/>' +
        '<polygon points="15,13 19,20 15,19"/>' +
        '<rect x="10" y="18" width="4" height="3" rx="1"/>' +
      '</svg>',

    // impulse 8: Lightning Gun – zigzag thunderbolt
    'impulse 8':
      '<svg viewBox="0 0 24 24" fill="currentColor">' +
        '<path d="M13 2L5 13h6l-2 9 10-12h-6z"/>' +
      '</svg>',

    // messagemode: outlined single speech bubble for public chat
    'messagemode':
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
        '<path d="M8 3h8a5 5 0 0 1 5 5v5a5 5 0 0 1-5 5H9l-4 3v-3a5 5 0 0 1-3-4.5V8a5 5 0 0 1 5-5z"/>' +
      '</svg>',

    // messagemode2: two overlapping speech bubbles for team chat
    'messagemode2':
      '<svg viewBox="0 0 24 24" fill="currentColor">' +
        '<path d="M14 3h4a4 4 0 0 1 4 4v3a4 4 0 0 1-4 4h-1v3l-2.5-3H14a4 4 0 0 1-4-4V7a4 4 0 0 1 4-4z" opacity="0.55"/>' +
        '<path d="M2 9a5 5 0 0 1 5-5h5a5 5 0 0 1 5 5v3a5 5 0 0 1-5 5H8l-4 4v-4a5 5 0 0 1-2-4V9z"/>' +
      '</svg>'
  };
})();
