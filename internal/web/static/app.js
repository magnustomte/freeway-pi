'use strict';

// No framework and no build step. The page is small enough that the DOM is the
// state container, and every dependency is something that would eventually want
// patching on a box nobody maintains.

// Away and Long away are deliberately absent. The manual advises against them
// on a unit with a heat pump: they drop the fans to 30 and 20 per cent and save
// no energy, so offering them would be offering a mistake.
// Overpressure and boost are absent: both are timed modes with a duration, and
// both have a card of their own that can ask for it and show what is left. A
// button here could only turn them on for however long they ran last time.
// The labels are looked up when the buttons are built, not here: at the moment
// this file loads the catalogue has not arrived.
const MODES = [
  ['normal', false],
  ['max_heating', false],
  ['max_cooling', false],
  ['stop', true],
];

const el = (id) => document.getElementById(id);
const nb = (v, d = 1) => num(v, d);
const fmtTemp = (v) => (v === null || v === undefined ? '–' : `${nb(v)} °C`);

let state = null;
let pending = {};
let pendingTimer = null;
// A message that has to outlive the next redraw: a command that failed, or a
// clock write waiting for the minute to turn. The banner is redrawn with every
// state that arrives, several times a second when something is changing, and
// a message that lived only until then was gone before anybody could read it.
let held = null; // { text, cls, until }

function hold(text, cls, seconds) {
  held = { text, cls, until: Date.now() + seconds * 1000 };
  banner(text, cls, null);
}

// --- rendering -------------------------------------------------------------

function buildModes() {
  const box = el('modes');
  for (const [value, danger] of MODES) {
    const label = t(`ui.mode.${value}`);
    const b = document.createElement('button');
    b.className = danger ? 'mode danger' : 'mode';
    b.type = 'button';
    b.textContent = label;
    b.dataset.mode = value;
    b.setAttribute('aria-pressed', 'false');
    b.addEventListener('click', () => requestMode(value, label, danger));
    box.append(b);
  }
}

function render() {
  if (!state) return;
  const s = state;
  const shown = { ...s, ...pending };

  el('v-fresh').textContent = fmtTemp(s.temperatures.fresh_air);
  el('v-supply').textContent = fmtTemp(s.temperatures.supply);
  el('v-extract').textContent = fmtTemp(s.temperatures.extract);
  el('v-waste').textContent = fmtTemp(s.temperatures.waste);
  // The level in effect goes where the fans are, not only under the slider:
  // the house is the answer to "what is it doing".
  el('ahu-fan-level').textContent = `${s.fan_actual} %`;

  el('setpoint').textContent = nb(shown.setpoint);

  const fan = el('fan');
  if (document.activeElement !== fan) fan.value = shown.fan_setpoint;
  el('fan-value').textContent = `${shown.fan_setpoint} %`;
  el('fan-foot').textContent =
    s.fan_actual !== s.fan_setpoint
      ? t('ui.fan.running', s.fan_actual)
      : '';

  for (const b of document.querySelectorAll('.mode')) {
    b.setAttribute('aria-pressed', String(b.dataset.mode === shown.mode));
  }

  renderLink(s);
  renderOverpressure(s, shown);
  renderBoost(s, shown);
  renderChips(s);
  renderOutputs(s);
  renderConnection(s);
  renderReadings(s);
  renderFreshness(s);
}

function renderLink(s) {
  const link = s.link || { status: 'ok', label: '' };
  el('unit-name').textContent = s.name || t('ui.aggregate.default');
  // An SVG element's className is an SVGAnimatedString, not a string, so it
  // has to be set as an attribute rather than assigned.
  el('link-dot').setAttribute('class', `dot ${link.status}`);
  // The label carries the meaning and the colour only reinforces it, so the
  // status is readable without relying on telling red from green.
  el('link-label').textContent = link.label || '';
}

function renderOverpressure(s, shown) {
  // Overpressure is the gap between the two fans, and seeing the numbers makes
  // the function legible rather than magic.
  const fans = s.overpressure_supply && s.overpressure_extract
    ? t('ui.overpressure.fans', s.overpressure_supply, s.overpressure_extract)
    : '';
  renderTimedMode(s, shown, {
    mode: 'overpressure',
    toggle: 'op-toggle',
    minutes: 'op-minutes',
    foot: 'op-foot',
    stored: s.overpressure_minutes,
    detail: fans,
  });
}

function renderBoost(s, shown) {
  renderTimedMode(s, shown, {
    mode: 'boost',
    toggle: 'boost-toggle',
    minutes: 'boost-minutes',
    foot: 'boost-foot',
    stored: s.boost_minutes,
    detail: s.boost_level ? t('ui.boost.both', s.boost_level) : '',
  });
}

// renderTimedMode draws either card. They are the same control: a mode that
// runs for a set time, a duration the unit stores but does not count down, and
// a line saying what it is doing while it does it.
function renderTimedMode(s, shown, c) {
  const on = shown.mode === c.mode;

  const toggle = el(c.toggle);
  toggle.textContent = on ? t('ui.stop') : t('ui.start');
  toggle.classList.toggle('on', on);

  // Left alone while it has focus, so the field does not change under somebody
  // typing in it.
  const minutes = el(c.minutes);
  if (document.activeElement !== minutes) minutes.value = c.stored;

  el(c.foot).textContent = on ? runningFor(s, c.detail) : '';
}

// runningFor says how long is left, or admits that it cannot.
//
// The duration register is the setting, not the time remaining, and the unit
// does not count it down — Freeway Pi holds the time itself. A run that began
// before it was watching has no known end, and saying so is better than
// guessing.
function runningFor(s, detail) {
  const tail = detail ? ` ${detail}` : '';
  if (s.mode_remaining_seconds === null || s.mode_remaining_seconds === undefined) {
    return t('ui.timed.unknown') + tail;
  }
  const left = Math.max(0, Math.round(s.mode_remaining_seconds));
  const m = Math.floor(left / 60);
  const sec = left % 60;
  const clock = m > 0
    ? t('ui.timed.left.min', m, String(sec).padStart(2, '0'))
    : t('ui.timed.left.sec', sec);
  return clock + tail;
}

function renderChips(s) {
  // Icon and label together. The icon is what the eye finds; the label is what
  // makes it unambiguous, and neither carries the meaning alone.
  const chips = [];
  if (s.alarm) chips.push(['alarm', t('ui.chip.alarm'), 'i-alarm']);
  if (s.defrosting) chips.push(['on', t('ui.chip.defrosting'), 'i-defrost']);
  if (s.heating) chips.push(['on', t('ui.chip.heating'), 'i-heating']);
  if (s.cooling) chips.push(['on', t('ui.chip.cooling'), 'i-cooling']);
  if (s.heat_recovery) chips.push(['on', t('ui.chip.recovery'), 'i-recovery']);
  const handled = ['stop', 'boost', 'max_heating', 'max_cooling', 'overpressure', 'defrosting', 'away', 'long_away'];
  for (const f of s.flags || []) {
    if (!handled.includes(f.name)) chips.push(['on', f.label, null]);
  }

  const box = el('chips');
  box.textContent = '';
  for (const [cls, label, icon] of chips) {
    const span = document.createElement('span');
    span.className = `chip ${cls}`;
    if (icon) {
      const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
      svg.setAttribute('class', 'chip-icon');
      svg.setAttribute('aria-hidden', 'true');
      const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
      use.setAttribute('href', `#${icon}`);
      svg.append(use);
      span.append(svg);
    }
    span.append(document.createTextNode(label));
    box.append(span);
  }
}

// renderConnection collects everything about who is talking to what.
//
// The unit's own link belongs beside the gateway's, because they answer the
// same worry from two ends: can this box reach the unit, and is anything else
// reaching it through this box. The Modbus line lived under the house for a
// while, which put a detail about the network in the middle of a picture about
// the air.
// listenLabel turns a listen address into something readable. The configured
// form is ":502", meaning every address on this box, which as a sentence is
// "port 502" rather than a colon and a number.
function listenLabel(listen) {
  if (!listen) return t('ui.port', '502');
  if (listen.startsWith(':')) return t('ui.port', listen.slice(1));
  return listen;
}

// renderOutputs draws what the unit is doing to the air, the way the original
// adapter does: a symbol each for recovery, heating and cooling, with the one
// in effect lit.
//
// Which, not how much. These used to carry a percentage each, unpacked from a
// register that turned out to be the integral term of the temperature
// controller — so the numbers were invented and sat near zero for ever. The
// unit reports the step its temperature control is on and never how hard it is
// working, so there is no percentage to show and none is shown.
//
// All three are always there, and the inactive ones are dimmed rather than
// hidden. Hiding them would make the row move about as the unit changes what it
// is doing, and the point of a glance is that things stay where they were.
//
// No colour. Red for heat and blue for cold would be the obvious choice and is
// wrong here: this picture already uses red and blue for supply and outdoor air
// under NS 5575, and two meanings for one colour in one drawing is worse than
// none. The shape carries it, and emphasis says which is in effect.
function renderOutputs(s) {
  const out = s.output || {};
  for (const [id, value] of [
    ['out-recovery', out.recovery],
    ['out-heating', out.heating],
    ['out-cooling', out.cooling],
  ]) {
    const g = el(id);
    if (!g) continue;
    g.classList.toggle('on', value > 0);
    const text = g.querySelector('text');
    if (text) text.textContent = `${value || 0} %`;
  }
}

function renderConnection(s) {
  const box = el('connection');
  if (!box) return;

  const link = s.link || { status: 'ok', label: '' };
  const g = s.gateway || { enabled: false, clients: [] };
  const clients = g.clients || [];

  // Short words, because the label already says what is being described:
  // "Aggregatet — Tilkoblet aggregat" says it twice.
  const linkWord = (status) => t(`ui.link.${status}`);

  const facts = [
    // The label carries the meaning and the colour only reinforces it, so the
    // status reads without relying on telling red from green.
    [t('ui.the.aggregate'), linkWord(link.status) || link.label || t('ui.unknown'),
      link.status === 'ok' ? 'good' : link.status === 'down' ? 'bad' : 'warn'],
    [t('ui.gateway'), g.enabled ? t('ui.gateway.active', listenLabel(g.listen)) : t('ui.gateway.off'),
      g.enabled ? 'good' : ''],
  ];

  if (g.enabled) {
    // Name and address both. The name is what somebody recognises; the address
    // is what they can act on when they do not recognise it.
    facts.push([t('ui.connected.now'), clients.length === 0
      ? t('ui.none')
      : clients.map((c) => (c.label && c.label !== c.host ? `${c.label} (${c.host})` : c.host)).join(', ')]);
  }

  box.textContent = '';
  const wrap = document.createElement('div');
  wrap.className = 'facts';
  for (const [label, value, cls] of facts) {
    const dl = document.createElement('dl');
    dl.className = 'fact';
    const dt = document.createElement('dt');
    dt.textContent = label;
    const dd = document.createElement('dd');
    dd.textContent = value;
    if (cls) dd.className = cls;
    dl.append(dt, dd);
    wrap.append(dl);
  }
  box.append(wrap);
}

function renderReadings(s) {
  const tm = s.temperatures;
  const m = s.machine || {};
  const rows = [
    [t('ui.reading.after.recovery'), fmtTemp(tm.supply_after_recovery)],
    // The decision behind every other number here: the unit does not heat the
    // room, it aims the supply air at a temperature and the room follows. The
    // range comes with it, because the target only means something against the
    // limits it is allowed — at 13,0 with a floor of 13,0 the controller has
    // asked for everything it has and is still not satisfied.
    ...(tm.supply_target > 0 ? [[t('ui.reading.supply.target'),
      tm.supply_min > 0 && tm.supply_target <= tm.supply_min
        ? t('ui.reading.target.min', fmtTemp(tm.supply_target))
        : tm.supply_max > 0 && tm.supply_target >= tm.supply_max
          ? t('ui.reading.target.max', fmtTemp(tm.supply_target))
          : t('ui.reading.target.range', fmtTemp(tm.supply_target),
              fmtTemp(tm.supply_min), fmtTemp(tm.supply_max))]] : []),
    // The panel's own sensor, which is the temperature of the room somebody is
    // standing in rather than of the air in a duct. On a house whose only room
    // sensor is the panel, the mean the unit works from is the same number, so
    // it is only shown when it differs.
    [t('ui.reading.panel'), fmtTemp(tm.panel)],
    ...(Math.abs(tm.room_mean - tm.panel) >= 0.05
      ? [[t('ui.reading.room.mean'), fmtTemp(tm.room_mean)]] : []),
    [t('ui.reading.humidity'), `${s.humidity} %`],
    ...(s.humidity_mean ? [[t('ui.reading.humidity.mean'), `${s.humidity_mean} %`]] : []),
    // Zero on a unit with no CO2 sensor, which is most of them.
    ...(s.co2 ? [[t('ui.reading.co2'), `${s.co2} ppm`]] : []),
    [t('ui.reading.recovery.supply'), `${s.efficiency.supply} %`],
    [t('ui.reading.recovery.extract'), `${s.efficiency.extract} %`],
    [t('ui.reading.fan'), `${s.fan_setpoint} / ${s.fan_actual} %`],
    [t('ui.reading.overpressure.minutes'), `${s.overpressure_minutes} min`],
    [t('ui.reading.machine'), m.family || t('ui.reading.unknown.machine', m.family_code)],
    // Zero on a unit that never had one written, which is not rare.
    ...(m.serial ? [[t('ui.serial'), String(m.serial)]] : []),
    [t('ui.unit.softwareversion'), String(s.software_version)],
  ];
  const box = el('readings');
  box.textContent = '';
  for (const [label, value] of rows) {
    const dl = document.createElement('dl');
    dl.className = 'reading';
    const dt = document.createElement('dt');
    dt.textContent = label;
    const dd = document.createElement('dd');
    dd.textContent = value;
    dl.append(dt, dd);
    box.append(dl);
  }
}

function renderFreshness(s) {
  const age = Math.round(s.age_seconds);
  const box = el('freshness');
  box.textContent = age < 15 ? t('ui.fresh.now')
    : age < 90 ? t('ui.fresh.sec', age)
    : t('ui.fresh.min', Math.round(age / 60));
  box.classList.toggle('stale', s.stale);
  document.body.classList.toggle('stale', s.stale);

  // Whether the unit is answering at all is link.js's strip now, on every page
  // rather than only this one — so saying it here as well would stack the same
  // sentence twice. What is left here is what only this page can act on.
  if (held && Date.now() < held.until) {
    banner(held.text, held.cls, null);
    return;
  }
  held = null;
  if (Math.abs(s.unit_clock_drift_seconds) > 300) {
    const h = Math.round(Math.abs(s.unit_clock_drift_seconds) / 3600);
    const off = h >= 1
      ? t(h === 1 ? 'ui.hours' : 'ui.hours.plural', h)
      : t('ui.minutes.n', Math.round(Math.abs(s.unit_clock_drift_seconds) / 60));
    // Two clocks disagree, and the drift alone cannot say which is wrong. The
    // person reading this has a third. If the unit agrees with it, the unit is
    // right and Freeway Pi is out — almost always a timezone left on the
    // image's default. Then the unit's timer programmes are firing on time,
    // and the button that sets the unit's clock from Freeway Pi's would be the
    // one thing on the page that made it wrong.
    if (unitAgreesWithReader(s)) {
      banner(t('ui.clock.boxzone', off), 'warn',
        { label: t('ui.clock.gozone'), action: () => { location.href = 'system.html#clock'; } });
      return;
    }
    // clock_settable is null until somebody has tried. Once the unit has shown
    // that it acknowledges the write and ignores it, the button is replaced by
    // where the clock can actually be set.
    if (s.clock_settable === false) {
      // An hour out is the summer-time change, which happens twice a year and
      // has to be put right by hand. Saying that beats reporting a mysterious
      // hour and leaving somebody to work out why.
      const why = s.clock_drift_is_dst ? t('ui.clock.dst') : t('ui.clock.drift', off);
      // Reached only after a write has been tried and the clock did not move.
      // The board does take one — measured — so this is a fault and not the
      // way it is meant to be.
      banner(t('ui.clock.ignored', why), 'warn', { label: t('ui.try.again'), action: syncClock });
    } else {
      banner(t('ui.clock.drift', off), 'warn', { label: t('ui.clock.set'), action: syncClock });
    }
  } else {
    banner('', '', null);
  }
}

// unitAgreesWithReader compares the unit's clock with the reader's own.
//
// The unit keeps a wall clock with no zone, and the state carries its digits
// with Freeway Pi's offset attached — so the offset is ignored and the digits
// read as local time here, in whatever zone this browser is in. The snapshot
// is up to a poll interval old, which is added back before comparing.
function unitAgreesWithReader(s) {
  const m = /^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2}):(\d{2})/.exec(s.unit_clock || '');
  if (!m || m[1] === '0001') return false;
  const wall = new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]).getTime();
  const read = wall + (s.age_seconds || 0) * 1000;
  return Math.abs(Date.now() - read) < 5 * 60 * 1000;
}

function banner(text, cls, action) {
  const b = el('banner');
  if (!text) {
    b.hidden = true;
    return;
  }
  b.hidden = false;
  b.className = `banner ${cls || ''}`;
  el('banner-text').textContent = text;

  const button = el('banner-action');
  button.hidden = !action;
  if (action) {
    button.textContent = action.label;
    button.onclick = action.action;
  }
}

// --- commands --------------------------------------------------------------

async function send(path, body, optimistic) {
  // Show the intent at once, and let it expire if no answer arrives, so the
  // interface can never get stuck showing something the unit never accepted.
  pending = { ...pending, ...optimistic };
  clearTimeout(pendingTimer);
  pendingTimer = setTimeout(() => { pending = {}; render(); }, 20000);
  render();

  try {
    const res = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || res.statusText);
    // Only a state replaces the state. An endpoint that answers with a note
    // instead — the clock does — would otherwise leave the page drawing
    // temperatures from an object that has none.
    if (data && data.temperatures) state = data;
    return data;
  } catch (err) {
    hold(t('ui.command.failed', err.message), 'bad', 10);
  } finally {
    pending = {};
    clearTimeout(pendingTimer);
    render();
  }
}

function confirmThen(text, run) {
  const dialog = el('confirm');
  el('confirm-text').textContent = text;
  const ok = () => { dialog.close(); run(); };
  el('confirm-ok').onclick = ok;
  el('confirm-cancel').onclick = () => dialog.close();
  dialog.showModal();
}

function requestMode(mode, label, danger) {
  const current = pending.mode ?? state?.mode;
  if (current === mode) return;
  const go = () => send('api/control/mode', { mode }, { mode });
  if (danger) {
    confirmThen(
      t('ui.stop.confirm'),
      go,
    );
    return;
  }
  go();
}

function nudgeSetpoint(delta) {
  if (!state) return;
  const base = pending.setpoint ?? state.setpoint;
  const next = Math.min(30, Math.max(10, Math.round((base + delta) * 10) / 10));
  send('api/control/setpoint', { celsius: next }, { setpoint: next });
}

// syncClock asks and does not wait.
//
// The write is timed to a minute boundary, because the unit zeroes the seconds
// when it applies one, so the daemon answers straight away and does it up to a
// minute later. The banner stays until the drift actually goes, which is the
// only honest confirmation — so the button says what is about to happen rather
// than pretending it is done.
async function syncClock() {
  const button = el('banner-action');
  button.disabled = true;
  button.textContent = t('ui.setting.clock');
  let answer;
  try {
    answer = await clockRequest('api/control/clock', 'POST', {});
  } catch (err) {
    hold(t('ui.command.failed', err.message), 'bad', 10);
    return;
  }
  if (!answer) {
    // Asked, and the answer was not to. The banner is redrawn with the next
    // state; until then the button works again.
    button.disabled = false;
    button.textContent = t('ui.clock.set');
    return;
  }
  // The write waits for the next minute to begin, so the note has to stand
  // until then — and the button with it, or a second press starts a second
  // write while the first is still waiting.
  if (answer.note) hold(answer.note, 'warn', (answer.wait_seconds || 60) + 15);
}

// --- wiring ----------------------------------------------------------------

buildModes();

el('menu-button').addEventListener('click', () => {
  const menu = el('menu');
  menu.hidden = !menu.hidden;
  el('menu-button').setAttribute('aria-expanded', String(!menu.hidden));
});

el('temp-down').addEventListener('click', () => nudgeSetpoint(-0.5));
el('temp-up').addEventListener('click', () => nudgeSetpoint(0.5));

// Only on release. Sending on every pixel of drag would queue dozens of writes
// on a bus that serves one request at a time.
el('fan').addEventListener('change', (e) => {
  const percent = Number(e.target.value);
  send('api/control/fan', { percent }, { fan_setpoint: percent });
});
el('fan').addEventListener('input', (e) => {
  el('fan-value').textContent = `${e.target.value} %`;
});

timedModeToggle('op-toggle', 'op-minutes', 'overpressure');
timedModeToggle('boost-toggle', 'boost-minutes', 'boost');

// The optimistic mode is the one the unit will be in afterwards, so the button
// flips at the press rather than a poll later.
function timedModeToggle(toggle, minutes, mode) {
  el(toggle).addEventListener('click', () => {
    const on = (pending.mode ?? state?.mode) === mode;
    send(
      `api/control/${mode}`,
      { on: !on, minutes: Number(el(minutes).value) },
      { mode: on ? 'normal' : mode },
    );
  });
}

// --- live updates ----------------------------------------------------------

function connect() {
  const es = new EventSource('api/events');
  es.addEventListener('message', (e) => {
    state = JSON.parse(e.data);
    render();
  });
  es.addEventListener('error', () => {
    // EventSource reconnects by itself; say so rather than looking frozen.
    el('freshness').textContent = 'mistet forbindelsen…';
    el('freshness').classList.add('stale');
  });
}

// The age in the header and the overpressure countdown both tick locally, so a
// page that has stopped receiving looks stopped rather than current.
setInterval(() => {
  if (!state) return;
  state.age_seconds += 1;
  if (typeof state.mode_remaining_seconds === 'number') {
    state.mode_remaining_seconds = Math.max(0, state.mode_remaining_seconds - 1);
    // Both cards: only one can be counting, and the other costs nothing to
    // redraw. Boost was added beside overpressure and this line was not.
    const shown = { ...state, ...pending };
    renderOverpressure(state, shown);
    renderBoost(state, shown);
  }
  renderFreshness(state);
}, 1000);

fetch('api/state')
  .then((r) => (r.ok ? r.json() : Promise.reject(new Error(t('ui.notread')))))
  .then((s) => { state = s; render(); })
  .catch((err) => banner(err.message, 'warn', null))
  .finally(connect);
