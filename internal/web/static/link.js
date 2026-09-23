// The one message that belongs on every page: the unit is not answering.
//
// It used to appear only on the overview, because that is the only page that
// reads the state. So pulling the RS-485 plug while somebody was on Settings
// or Data showed them nothing at all — they would go on reading numbers that
// had quietly stopped being true, or change a setting that was never going to
// reach anything.
//
// Its own strip rather than each page's banner. A page's banner carries what
// that page is doing; this carries whether the box is talking to the unit at
// all, and one must not be able to hide the other.

(function () {
  const POLL_MS = 15000;
  let strip = null;

  function ensure() {
    if (strip) return strip;
    strip = document.createElement('div');
    strip.id = 'link-strip';
    strip.className = 'banner bad';
    strip.hidden = true;
    // Above the page's own banner, because it is the more fundamental fact.
    const existing = document.getElementById('banner');
    if (existing && existing.parentNode) {
      existing.parentNode.insertBefore(strip, existing);
    } else {
      const main = document.querySelector('main') || document.body;
      main.insertBefore(strip, main.firstChild);
    }
    return strip;
  }

  // More than one thing can be wrong at once, and a box being starved of power
  // is usually why the other one is. Both are said, power first, rather than
  // letting either hide the other.
  function show(lines) {
    const s = ensure();
    const texts = (Array.isArray(lines) ? lines : [lines]).filter(Boolean);
    if (texts.length === 0) {
      s.hidden = true;
      s.textContent = '';
      return;
    }
    s.hidden = false;
    s.textContent = '';
    for (const text of texts) {
      const p = document.createElement('p');
      p.className = 'banner-line';
      p.textContent = text;
      s.append(p);
    }
  }

  // The supply is its own request because the state needs the unit to have
  // answered, and a supply bad enough to matter is what stops it answering. A
  // message that disappears as the problem gets worse is not a message.
  async function supply() {
    try {
      const r = await fetch('api/power', { headers: { Accept: 'application/json' } });
      if (!r.ok) return null;
      const p = await r.json();
      if (!p.recent) return null;
      // The count is what makes it actionable: one event at boot is a
      // different problem from eighty-eight in an hour, and the wording has to
      // stop short of telling somebody to replace a charger over the first.
      return p.events === 1 ? t('ui.power.strip.once') : t('ui.power.strip', p.events);
    } catch (err) {
      return null;
    }
  }

  async function check() {
    const power = await supply();
    let state;
    let answered = false;
    try {
      const r = await fetch('api/state', { headers: { Accept: 'application/json' } });
      answered = true;
      if (!r.ok) {
        // A 503 is the daemon saying it has not read the unit yet — which is
        // what a box says before its adapter is plugged in. It answered, so it
        // is plainly not unreachable; that was the message this used to show,
        // on the one page that could have explained the real problem.
        show([power, r.status === 503 ? t('ui.link.strip.down') : t('ui.link.unreachable')]);
        return;
      }
      state = await r.json();
    } catch (err) {
      // Only a request that never arrived means the daemon is gone.
      if (answered) return;
      show([power, t('ui.link.unreachable')]);
      return;
    }
    const link = state.link || {};
    let unit = '';
    if (link.status === 'down') {
      unit = t('ui.link.strip.down');
    } else if (state.stale) {
      unit = t('ui.stale');
    } else if (link.status === 'degraded') {
      unit = t('ui.link.strip.degraded');
    }
    show([power, unit]);
  }

  // The scripts load in the head, long before the banner this sits above
  // exists — so the first check waits for the document. i18n.js is a blocking
  // script, so t() is already ready.
  function start() {
    check();
    setInterval(() => { if (!document.hidden) check(); }, POLL_MS);
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start);
  } else {
    start();
  }
  document.addEventListener('visibilitychange', () => { if (!document.hidden) check(); });
})();
