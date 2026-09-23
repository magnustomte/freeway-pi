'use strict';

// The unit's own alarm log, on a page of its own.
//
// It was on the system page, among the figures about the Raspberry Pi. That put
// the ventilation unit's faults in the one place on the site that is about the
// box rather than about the unit, which is the wrong place to look for them.

const el = (id) => document.getElementById(id);

function banner(text) {
  const b = el('banner');
  b.hidden = !text;
  if (text) el('banner-text').textContent = text;
}

function note(text) {
  const p = document.createElement('p');
  p.className = 'setting-help';
  p.textContent = text;
  return p;
}

const shown = (iso) => (iso && !iso.startsWith('0001') ? stamp(iso) : t('ui.time.unknown'));

// renderActive answers the first question somebody opening this page has: is
// anything wrong right now. The log below answers what has been.
function renderActive(state) {
  const box = el('active');
  box.textContent = '';
  const active = (state && state.active_alarms) || [];

  if (active.length === 0) {
    const ok = document.createElement('div');
    ok.className = 'fact';
    const dt = document.createElement('dt');
    dt.textContent = t('ui.status');
    const dd = document.createElement('dd');
    dd.className = 'good';
    dd.textContent = t('ui.alarm.none.active');
    ok.append(dt, dd);
    box.append(ok);
    return;
  }
  for (const a of active) box.append(alarmRow(a));
}

function renderLog(alarms) {
  const box = el('alarms');
  box.textContent = '';
  const real = (alarms || []).filter((a) => !a.empty);
  if (real.length === 0) {
    box.append(note(t('ui.alarm.none.logged')));
    return;
  }
  for (const a of real) box.append(alarmRow(a));
}

function alarmRow(a) {
  const row = document.createElement('div');
  row.className = 'alarm';

  const head = document.createElement('div');
  head.className = 'alarm-head';
  const name = document.createElement('span');
  name.className = `alarm-name ${a.state === 2 ? 'bad' : ''}`;
  name.textContent = a.name;
  const chip = document.createElement('span');
  chip.className = `chip ${a.state === 2 ? 'alarm' : ''}`;
  // The class number is the unit's own severity and worth keeping: class 1
  // stops the machine and class 3 is a note.
  chip.textContent = a.class ? `${a.state_label} · ${t('ui.alarm.class')} ${a.class}` : a.state_label;
  head.append(name, chip);

  const time = document.createElement('div');
  time.className = 'alarm-time';
  time.textContent = shown(a.time);

  row.append(head, time);
  if (a.help) {
    const help = document.createElement('p');
    help.className = 'setting-help';
    help.textContent = a.help;
    row.append(help);
  }
  return row;
}

async function get(path) {
  const res = await fetch(path);
  const answer = await res.json();
  if (!res.ok) throw new Error(answer.error || res.statusText);
  return answer;
}

el('menu-button').addEventListener('click', () => {
  const menu = el('menu');
  menu.hidden = !menu.hidden;
  el('menu-button').setAttribute('aria-expanded', String(!menu.hidden));
});

gateSetUp({ help: 'ui.gate.alarms', unlocked: refresh });

async function refresh() {
  const status = await get('api/auth/status');
  if (!status.authenticated) {
    gateShow(status, { help: 'ui.gate.alarms' });
    el('gate').hidden = false;
    el('content').hidden = true;
    return;
  }
  el('gate').hidden = true;
  el('content').hidden = false;

  // The state carries what is wrong now; the log carries what has been. A
  // failure in one does not hide the other.
  const [state, alarms] = await Promise.all([
    get('api/state').catch(() => null),
    get('api/alarms').catch(() => null),
  ]);
  renderActive(state);
  renderLog(alarms);
}

refresh().catch((err) => banner(err.message));
setInterval(() => {
  if (document.hidden || busyTyping()) return;
  refresh().catch(() => {});
}, 30000);
