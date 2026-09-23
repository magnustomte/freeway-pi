'use strict';

// Light, dark, or whatever the system says.
//
// Loaded from <head> and not deferred, on purpose: the attribute has to be on
// <html> before the first paint, or the page renders in the system's colours and
// then flips to the chosen one. A blocking script is the cost of not flashing,
// and on a LAN it is a kilobyte from the same box.
//
// The stored choice is a convenience and never required. Private browsing, a
// cleared site, and a locked-down profile all make localStorage throw or come
// back empty, so every read and write is wrapped and the page works without it.

const THEMES = ['system', 'light', 'dark'];
const THEME_KEY = 'freeway-theme';

// The status-bar colour behind a standalone app's clock, which cannot be a
// variable: iOS reads the attribute, not the stylesheet.
const THEME_COLOR = { light: '#f4f6f8', dark: '#0f1720' };

function storedTheme() {
  try {
    const v = localStorage.getItem(THEME_KEY);
    return THEMES.includes(v) ? v : 'system';
  } catch {
    return 'system';
  }
}

function systemIsDark() {
  return matchMedia('(prefers-color-scheme: dark)').matches;
}

// applyTheme is the only thing that runs before the page exists, so it touches
// nothing but the root element.
function applyTheme(choice) {
  const root = document.documentElement;
  if (choice === 'system') {
    root.removeAttribute('data-theme');
  } else {
    root.setAttribute('data-theme', choice);
  }
  const dark = choice === 'dark' || (choice === 'system' && systemIsDark());
  // Both meta tags are replaced by one without a media attribute: a media
  // query cannot know about a choice made in the page.
  for (const m of document.querySelectorAll('meta[name="theme-color"]')) m.remove();
  const meta = document.createElement('meta');
  meta.name = 'theme-color';
  meta.content = dark ? THEME_COLOR.dark : THEME_COLOR.light;
  document.head.append(meta);

  // Anything that draws its own colours rather than reading the stylesheet —
  // the charts — needs telling. Dispatched on window and not on document,
  // since a listener may be registered before the body exists.
  dispatchEvent(new CustomEvent('themechange', { detail: { dark } }));
}

applyTheme(storedTheme());

// --- the button ------------------------------------------------------------

// One icon per state, so the button says which of the three is in force rather
// than only that a choice exists.
const THEME_ICONS = {
  // A sun.
  light: 'M12 4V2M12 22v-2M4 12H2M22 12h-2M5.6 5.6 4.2 4.2M19.8 19.8l-1.4-1.4'
    + 'M18.4 5.6l1.4-1.4M4.2 19.8l1.4-1.4',
  // A crescent.
  dark: 'M20 14.5A8.5 8.5 0 0 1 9.5 4a8.5 8.5 0 1 0 10.5 10.5z',
  // A half-filled circle: neither one nor the other.
  system: 'M12 3a9 9 0 0 0 0 18z',
};

// Looked up when drawn, not when this file loads: the catalogue is fetched and
// has not arrived yet at that point, and a label captured then would be a
// message id for the rest of the visit.
const label = (choice) => t(`ui.theme.${choice}`);

function themeButton() {
  const button = document.createElement('button');
  button.id = 'theme-button';
  button.className = 'icon-button';
  button.type = 'button';

  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('width', '20');
  svg.setAttribute('height', '20');
  svg.setAttribute('aria-hidden', 'true');
  const ring = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
  ring.setAttribute('cx', '12');
  ring.setAttribute('cy', '12');
  ring.setAttribute('r', '5');
  const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
  svg.append(ring, path);
  button.append(svg);

  const draw = (choice) => {
    const light = choice === 'light';
    // The sun keeps its circle and its rays; the other two are one path.
    ring.setAttribute('fill', choice === 'system' ? 'none' : light ? 'currentColor' : 'none');
    ring.setAttribute('stroke', choice === 'light' ? 'none' : 'currentColor');
    ring.setAttribute('stroke-width', '1.8');
    ring.setAttribute('r', choice === 'light' ? '4' : '8.5');
    ring.style.display = choice === 'dark' ? 'none' : '';

    path.setAttribute('d', THEME_ICONS[choice]);
    path.setAttribute('fill', choice === 'system' ? 'currentColor' : 'none');
    path.setAttribute('stroke', choice === 'system' ? 'none' : 'currentColor');
    path.setAttribute('stroke-width', '1.8');
    path.setAttribute('stroke-linecap', 'round');

    // The label names the state it is in and the title says what a press does,
    // so a screen reader is not left guessing which.
    const next = THEMES[(THEMES.indexOf(choice) + 1) % THEMES.length];
    button.setAttribute('aria-label', `${t('ui.theme')}: ${label(choice)}`);
    button.title = `${t('ui.theme')}: ${label(choice)} — ${t('ui.theme.press', label(next).toLowerCase())}`;
  };

  let choice = storedTheme();
  draw(choice);

  button.addEventListener('click', () => {
    choice = THEMES[(THEMES.indexOf(choice) + 1) % THEMES.length];
    applyTheme(choice);
    draw(choice);
    try {
      localStorage.setItem(THEME_KEY, choice);
    } catch {
      // Not worth telling anybody about: the theme still changed, it just will
      // not be remembered.
    }
  });

  // On "system", the page has to follow the system as it changes. The listener
  // stays attached for the other two as well, and applyTheme ignores it.
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    if (choice === 'system') applyTheme('system');
  });

  return button;
}

// The button goes before the menu, because the menu is the last thing in the
// corner on every page and moving it would move the thing people reach for.
addEventListener('DOMContentLoaded', () => {
  const menu = document.getElementById('menu-button');
  if (!menu || !menu.parentNode) return;
  menu.parentNode.insertBefore(themeButton(), menu);
});
