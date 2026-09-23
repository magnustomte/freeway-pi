'use strict';

// Norsk eller engelsk, valgt per nettleser.
//
// Loaded from <head> and not deferred, for the same reason theme.js is: the
// words have to be right before the first paint, not corrected after it.
//
// The pages are authored in Norwegian, and that is the fallback: with no
// script, or with the catalogue unreachable, what is in the markup is a real
// sentence rather than a key. Only a reader who wants the other language waits
// for anything, and for them the content is held back rather than shown in the
// wrong language and swapped.
//
// The daemon decides which language a request is in. Rather than implementing
// the same rules here and hoping the two agree, the page asks for "auto" and
// uses whatever comes back — then writes it to a cookie so every later request,
// including the plain download links, is answered in the same language.

const LANG_KEY = 'freeway-lang';
const LANG_COOKIE = 'freeway-lang';
// The language the markup itself is written in.
const AUTHORED = 'no';

// The catalogue arrives as a blocking script before this one, so it is simply
// here. No fetch, no promise, no window in which t() returns ids — which was a
// real bug three times over: a mode button, a range selector and a chart legend
// all kept the ids they were built with.
const catalogue = (typeof window !== 'undefined' && window.__i18n) || {};
const messages = catalogue.messages || {};
const current = catalogue.lang || AUTHORED;
const available = catalogue.available || [{ code: AUTHORED, name: 'Norsk' }];

// Kept so the cookie matches what the page was rendered in, for the plain
// links — the spreadsheet download has column headings in the same language.
setCookie(current);

function storedLang() {
  try {
    return localStorage.getItem(LANG_KEY) || '';
  } catch {
    return '';
  }
}

function setCookie(lang) {
  // A year, path-wide, and SameSite=Lax: it is a display preference, not a
  // credential, and it has to ride along on ordinary link navigations.
  document.cookie = `${LANG_COOKIE}=${lang};path=/;max-age=31536000;SameSite=Lax`;
}

// t looks a message up. A missing one comes back as its own id, which is ugly
// on screen and findable, rather than silently empty.
function t(id, ...args) {
  const s = messages[id];
  if (s === undefined) return id;
  if (args.length === 0) return s;
  // Only the verbs the catalogue actually uses; this is not printf.
  let i = 0;
  return s.replace(/%(?:\.\d+)?[sdfgq]|%%/g, (m) => {
    if (m === '%%') return '%';
    const v = args[i++];
    if (m.endsWith('f')) {
      const dp = /\.(\d+)/.exec(m);
      return num(v, dp ? Number(dp[1]) : 6);
    }
    if (m.endsWith('q')) return `"${v}"`;
    return String(v);
  });
}

// locale is the BCP 47 tag for formatting dates and numbers.
//
// Norwegian Bokmål rather than the bare "no": it is what the browser actually
// has data for, and it is what decides that a date is 19.09.2026 and not
// 9/19/26.
function locale() {
  return current === 'no' ? 'nb-NO' : 'en-GB';
}

// num writes a number with a fixed count of decimals in the reader's own
// convention: a comma in Norwegian, a point in English.
//
// The one place that decides it. The overview and the charts used to put a
// comma in whatever the language, the system page a point in whatever the
// language, and "%.1f" in a message a point even in Norwegian — so an English
// reader saw a decimal comma in a temperature and a Norwegian one "1.0 t" on
// the page beside it.
function num(v, decimals = 0) {
  return Number(v).toLocaleString(locale(), {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  });
}

// stamp formats an instant in the reader's language, or says never.
function stamp(iso, opts) {
  if (!iso || iso.startsWith('0001')) return t('ui.never');
  return new Date(iso).toLocaleString(locale(), opts || { dateStyle: 'short', timeStyle: 'short' });
}

// dateOf is the day alone.
function dateOf(iso) {
  if (!iso || iso.startsWith('0001')) return t('ui.never');
  return new Date(iso).toLocaleDateString(locale());
}

// i18nReady is kept for callers that want to defer: it is already resolved,
// because there is nothing left to wait for.
const i18nReady = Promise.resolve();

// --- applying to the page ---------------------------------------------------

function applyTo(root) {
  for (const el of root.querySelectorAll('[data-i18n]')) {
    el.textContent = t(el.dataset.i18n);
  }
  for (const [attr, key] of [
    ['placeholder', 'i18nPlaceholder'],
    ['aria-label', 'i18nAriaLabel'],
    ['title', 'i18nTitle'],
  ]) {
    for (const el of root.querySelectorAll(`[data-${attr === 'aria-label' ? 'i18n-aria-label' : 'i18n-' + attr}]`)) {
      el.setAttribute(attr, t(el.dataset[key]));
    }
  }
  const title = document.querySelector('title[data-i18n]');
  if (title) document.title = t(title.dataset.i18n);
  document.documentElement.lang = current;
}

// Static markup is authored in Norwegian; anything else is replaced here,
// before the paint, because this script is blocking too.
if (current !== AUTHORED) applyTo(document);
// And again once the body exists, for the pages whose markup follows this tag.
addEventListener('DOMContentLoaded', () => { if (current !== AUTHORED) applyTo(document); });

// --- the chooser ------------------------------------------------------------

function languageButton() {
  const button = document.createElement('button');
  button.id = 'lang-button';
  button.className = 'icon-button lang-button';
  button.type = 'button';

  button.textContent = current.toUpperCase();
  button.setAttribute('aria-label', t('ui.language'));
  button.title = `${t('ui.language')}: ${nameOf(current)} — ${nameOf(nextLang())}`;

  button.addEventListener('click', () => {
    const next = nextLang();
    try {
      localStorage.setItem(LANG_KEY, next);
    } catch {
      // Not worth telling anybody about: the cookie below still carries the
      // choice for this visit.
    }
    setCookie(next);
    // Reloaded rather than re-rendered. Half this page's text comes from the
    // daemon — alarm names, setting labels, the words under the graphs — and
    // those arrive in the language of the request that fetched them.
    location.reload();
  });

  return button;
}

function nextLang() {
  const codes = available.length ? available.map((l) => l.code) : [AUTHORED];
  const i = codes.indexOf(current);
  return codes[(i + 1) % codes.length];
}

function nameOf(code) {
  const found = available.find((l) => l.code === code);
  return found ? found.name : code.toUpperCase();
}

// Before the theme button, so the two sit together in the corner and the menu
// stays last where people reach for it.
addEventListener('DOMContentLoaded', () => {
  const theme = document.getElementById('theme-button');
  const menu = document.getElementById('menu-button');
  const anchor = theme || menu;
  if (!anchor || !anchor.parentNode) return;
  anchor.parentNode.insertBefore(languageButton(), anchor);
});

// busyTyping reports whether somebody is in a field right now.
//
// Every page refreshes itself on a timer, and a refresh rebuilds the cards —
// so a tick that lands mid-sentence takes the text with it: a wifi passphrase,
// an ssh key, a PIN halfway typed. Nothing on these pages changes so fast that
// it cannot wait for a field to be let go of.
//
// Here because every page loads this file and every page has the problem.
function busyTyping() {
  const a = document.activeElement;
  if (!a || a === document.body) return false;
  return /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName);
}
