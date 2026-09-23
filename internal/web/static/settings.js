'use strict';

const el = (id) => document.getElementById(id);

let status = { configured: false, authenticated: false };

function banner(text, cls) {
  const b = el('banner');
  b.hidden = !text;
  b.className = `banner ${cls || ''}`;
  if (text) el('banner-text').textContent = text;
}

// --- gate ------------------------------------------------------------------

function renderGate() {
  const gate = el('gate');
  const box = el('settings');
  el('logout').hidden = !status.authenticated;

  if (status.authenticated) {
    gate.hidden = true;
    box.hidden = false;
    return;
  }
  gate.hidden = false;
  box.hidden = true;
  box.textContent = '';

  gateShow(status, { help: 'ui.gate.settings' });
}



// --- settings --------------------------------------------------------------

// clockCard is the unit's own clock: what it reads, how far out it is, and
// whether the box keeps it right by itself.
//
// Here rather than on the system page because it is the aggregate's clock, not
// the box's — the box gets its time from NTP like any other machine, and this
// is about what it does with it. It is not a register-backed setting either:
// the switch lives in this box's configuration.
function clockCard() {
  const card = document.createElement('section');
  card.className = 'card';
  const label = document.createElement('div');
  label.className = 'card-label';
  label.textContent = t('ui.clock');
  card.append(label);

  const drift = Math.abs(clock.drift_seconds || 0);
  const driftText = drift < 60
    ? t('ui.drift.sec', num(drift))
    : drift < 5400
      ? t('ui.drift.min', Math.round(drift / 60))
      : t('ui.drift.hours', num(drift / 3600, 1));

  // Three clocks side by side, because the question is which one is wrong.
  // Each is kept as its distance from this browser's, and all three count on
  // together — a snapshot a few seconds old would otherwise make the unit look
  // behind when it is not.
  const readAt = Date.now();
  const unitWall = wallDigits(clock.unit_clock);
  const boxWall = wallDigits(clock.system_time);
  const offsets = {
    unit: unitWall ? unitWall.getTime() + (clock.unit_clock_age_seconds || 0) * 1000 - readAt : null,
    box: boxWall ? boxWall.getTime() - readAt : null,
    reader: 0,
  };
  const show = (offset) => (offset === null ? t('ui.clock.unreadable')
    : new Date(Date.now() + offset).toLocaleString(locale(), { dateStyle: 'short', timeStyle: 'medium' }));

  const facts = document.createElement('div');
  facts.className = 'facts';
  const ticking = [];
  for (const [name, key, cls] of [
    [t('ui.clock.unit'), 'unit'],
    [t('ui.clock.box', zoneOf(clock.system_time)), 'box'],
    [t('ui.clock.reader'), 'reader'],
    [t('ui.drift'), null, drift > 300 ? 'bad' : drift > 60 ? 'warn' : 'good'],
  ]) {
    const dl = document.createElement('dl');
    dl.className = 'fact';
    const dt = document.createElement('dt');
    dt.textContent = name;
    const dd = document.createElement('dd');
    dd.textContent = key ? show(offsets[key]) : driftText;
    if (cls) dd.className = cls;
    if (key) ticking.push([dd, offsets[key]]);
    dl.append(dt, dd);
    facts.append(dl);
  }
  card.append(facts);
  clearInterval(clockTicker);
  clockTicker = setInterval(() => {
    for (const [dd, offset] of ticking) dd.textContent = show(offset);
  }, 1000);

  // Said here as well as on the overview: this is where somebody comes to set
  // the unit's clock, and the one thing to know first is that the unit is
  // right and Freeway Pi is not.
  const near = (a, b) => a !== null && b !== null && Math.abs(a - b) < 5 * 60 * 1000;
  if (near(offsets.unit, offsets.reader) && offsets.box !== null && !near(offsets.box, offsets.reader)) {
    const h = Math.round(Math.abs(offsets.box) / 3600000);
    const off = h >= 1 ? t(h === 1 ? 'ui.hours' : 'ui.hours.plural', h)
      : t('ui.minutes.n', Math.round(Math.abs(offsets.box) / 60000));
    const hint = document.createElement('p');
    hint.className = 'setting-help warn';
    hint.textContent = t('ui.clock.boxzone', off) + ' ';
    const go = document.createElement('a');
    go.href = 'system.html#clock';
    go.textContent = t('ui.clock.gozone');
    hint.append(go);
    card.append(hint);
  }

  const on = document.createElement('input');
  on.type = 'checkbox';
  on.className = 'switch';
  on.id = 'clock-auto';
  on.checked = !!clock.auto_sync;

  const row = document.createElement('div');
  row.className = 'setting';
  const text = document.createElement('div');
  text.className = 'setting-text';
  const lbl = document.createElement('label');
  lbl.className = 'setting-label';
  lbl.htmlFor = 'clock-auto';
  lbl.textContent = t('ui.clock.auto');
  const help = document.createElement('p');
  help.className = 'setting-help';
  help.textContent = t('ui.clock.auto.help');
  text.append(lbl, help);
  const ctl = document.createElement('div');
  ctl.className = 'setting-control';
  ctl.append(on);
  row.append(text, ctl);
  card.append(row);

  const actions = document.createElement('div');
  actions.className = 'save-row';
  const feedback = document.createElement('span');
  feedback.className = 'save-feedback';

  on.addEventListener('change', async () => {
    on.disabled = true;
    feedback.className = 'save-feedback';
    feedback.textContent = '';
    try {
      // Turning it on means writes every few hours with nobody watching, so
      // it asks the same question a single write does.
      const answer = await clockRequest('api/clock', 'PUT', { auto_sync: on.checked });
      if (!answer) {
        on.checked = !on.checked;
        return;
      }
      feedback.className = 'save-feedback good';
      feedback.textContent = answer.note || t('ui.saved');
      clock.auto_sync = on.checked;
    } catch (err) {
      on.checked = !on.checked;
      feedback.className = 'save-feedback bad';
      feedback.textContent = err.message;
    } finally {
      on.disabled = false;
    }
  });

  const now = document.createElement('button');
  now.className = 'ghost';
  now.textContent = t('ui.clock.now');
  now.addEventListener('click', async () => {
    now.disabled = true;
    const original = now.textContent;
    now.textContent = t('ui.setting.clock');
    feedback.className = 'save-feedback';
    feedback.textContent = '';
    try {
      const answer = await clockRequest('api/control/clock', 'POST', {});
      if (!answer) return;
      feedback.className = 'save-feedback good';
      feedback.textContent = answer.note || '';
    } catch (err) {
      feedback.className = 'save-feedback bad';
      feedback.textContent = err.message;
    } finally {
      now.disabled = false;
      now.textContent = original;
    }
  });

  actions.append(now, feedback);
  card.append(actions);
  return card;
}

let clock = null;
// clockTicker keeps the three clocks on the card counting; one at a time.
let clockTicker = null;

// service is the countdown, kept alongside the two service settings rather than
// among the readings on the overview: how long until the next filter change is
// something you look up when you are already setting the interval, not
// something you want between the temperature and the fan.
let service = null;

function renderSettings(values) {
  const box = el('settings');
  box.textContent = '';
  // First, because a wrong clock makes the timer programmes wrong and nothing
  // else on this page depends on anything.
  if (clock) box.append(clockCard());

  // Grouped by the catalogue id, not by the translated name: matching on the
  // words would break the moment somebody read the page in the other language.
  const groups = new Map();
  for (const v of values) {
    if (!groups.has(v.group_key)) groups.set(v.group_key, []);
    groups.get(v.group_key).push(v);
  }

  for (const [groupKey, items] of groups) {
    const card = document.createElement('section');
    card.className = 'card';
    const label = document.createElement('div');
    label.className = 'card-label';
    label.textContent = items[0].group;
    card.append(label);

    for (const item of items) card.append(settingRow(item));
    if (groupKey === 'setting.group.service' && service) card.append(serviceStatus());
    box.append(card);
  }
}

// serviceStatus is the countdown and the way to restart it.
//
// Setting a new interval does not restart it: the interval and the days already
// run are separate registers, and a new interval only recomputes the countdown
// from the same number of days. So changing the filter needs its own button,
// which is what somebody actually wants when the new filter arrives.
function serviceStatus() {
  // A grid rather than a plain div, so the button gets the same air from its
  // container as the rows above it do, without a margin rule reaching in.
  const box = document.createElement('div');
  box.className = 'service-status';

  const p = document.createElement('p');
  p.className = 'setting-help';
  if (!service.enabled) {
    p.textContent = t('ui.service.off', service.days_since);
  } else {
    const left = service.days_remaining;
    p.textContent = left > 0
      ? t('ui.service.next', left, service.days_since, service.interval_days)
      : t('ui.service.overdue', Math.abs(left), service.days_since, service.interval_days);
  }
  box.append(p);

  const row = document.createElement('div');
  row.className = 'save-row';
  const button = document.createElement('button');
  button.className = 'ghost';
  button.textContent = t('ui.service.done');
  const feedback = document.createElement('span');
  feedback.className = 'save-feedback';

  button.addEventListener('click', async () => {
    button.disabled = true;
    const original = button.textContent;
    button.textContent = t('ui.service.resetting');
    feedback.className = 'save-feedback';
    feedback.textContent = '';
    try {
      const answer = await post('api/settings/service-reset', {});
      feedback.className = 'save-feedback good';
      feedback.textContent = answer.note || 'Nullstilt';
      if (answer.service) service = answer.service;
      // Re-rendered so the countdown above the button agrees with it.
      await refresh();
    } catch (err) {
      feedback.className = 'save-feedback bad';
      feedback.textContent = err.message;
    } finally {
      button.disabled = false;
      button.textContent = original;
    }
  });

  row.append(button, feedback);
  box.append(row);
  return box;
}

const put = (path, body) => sendJSON('PUT', path, body);
const post = (path, body) => sendJSON('POST', path, body);

async function sendJSON(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const answer = await res.json();
  if (!res.ok) throw new Error(answer.error || res.statusText);
  return answer;
}

function settingRow(item) {
  const row = document.createElement('div');
  row.className = 'setting';

  const text = document.createElement('div');
  text.className = 'setting-text';
  const name = document.createElement('label');
  name.className = 'setting-label';
  name.textContent = item.label;
  name.htmlFor = `set-${item.key}`;
  text.append(name);
  if (item.help) {
    const help = document.createElement('p');
    help.className = 'setting-help';
    help.textContent = item.help;
    text.append(help);
  }

  const control = document.createElement('div');
  control.className = 'setting-control';

  if (item.type === 'bool') {
    const input = document.createElement('input');
    input.type = 'checkbox';
    input.id = `set-${item.key}`;
    input.className = 'switch';
    input.checked = item.bool;
    input.addEventListener('change', () => save(item, { bool: input.checked }, input));
    control.append(input);
  } else {
    const input = document.createElement('input');
    input.type = 'number';
    input.id = `set-${item.key}`;
    input.value = item.number ?? 0;
    input.min = item.min;
    input.max = item.max;
    input.step = item.step || 1;
    // On change rather than on input: each keystroke would be a write to a
    // unit that takes a noticeable moment per write.
    input.addEventListener('change', () => save(item, { number: Number(input.value) }, input));
    control.append(input);
    if (item.unit) {
      const unit = document.createElement('span');
      unit.className = 'setting-unit';
      unit.textContent = item.unit;
      control.append(unit);
    }
  }

  row.append(text, control);
  return row;
}

async function save(item, value, input) {
  input.disabled = true;
  try {
    const res = await fetch('api/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ key: item.key, ...value }),
    });
    const body = await res.json();
    if (!res.ok) throw new Error(body.error || res.statusText);
    // Redraw from what the unit now holds, not from what was typed: a value it
    // rounded or refused should show as what it actually is.
    renderSettings(body);
    banner('');
  } catch (err) {
    banner(err.message, '');
    await refresh();
  } finally {
    input.disabled = false;
  }
}

// --- wiring ----------------------------------------------------------------

async function refresh() {
  status = await (await fetch('api/auth/status')).json();
  renderGate();
  if (!status.authenticated) return;
  // The state comes along for the service countdown. A failure there is not a
  // reason to withhold the settings, so it only drops the one line.
  const [res, state, clockCfg] = await Promise.all([
    fetch('api/settings'),
    fetch('api/state').then((r) => (r.ok ? r.json() : null)).catch(() => null),
    fetch('api/clock').then((r) => (r.ok ? r.json() : null)).catch(() => null),
  ]);
  if (!res.ok) {
    banner((await res.json()).error || t('ui.settings.failed'), '');
    return;
  }
  service = state ? state.service : null;
  clock = clockCfg;
  renderSettings(await res.json());
}

el('menu-button').addEventListener('click', () => {
  const menu = el('menu');
  menu.hidden = !menu.hidden;
  el('menu-button').setAttribute('aria-expanded', String(!menu.hidden));
});
gateSetUp({ help: 'ui.gate.settings', unlocked: refresh });
el('logout').addEventListener('click', async () => {
  await fetch('api/auth/logout', { method: 'POST' });
  await refresh();
});

refresh().catch((err) => banner(err.message, ''));
