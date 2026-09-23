'use strict';

const el = (id) => document.getElementById(id);

// The register's mask is Sunday first, matching holding register 43. People
// read a week starting on Monday, so the translation happens once, here, and
// nowhere else.
// The letters and the names are looked up when a row is drawn: at the moment
// this file loads the catalogue has not arrived.
const DAYS = [
  { key: 'mon', bit: 1 << 1 },
  { key: 'tue', bit: 1 << 2 },
  { key: 'wed', bit: 1 << 3 },
  { key: 'thu', bit: 1 << 4 },
  { key: 'fri', bit: 1 << 5 },
  { key: 'sat', bit: 1 << 6 },
  { key: 'sun', bit: 1 << 0 },
];

let data = null;
let overrideClock = false;

function banner(text, cls, action) {
  const b = el('banner');
  b.hidden = !text;
  b.className = `banner ${cls || ''}`;
  el('banner-text').textContent = text || '';
  const act = el('banner-action');
  act.hidden = !action;
  if (action) {
    act.textContent = action.label;
    act.onclick = () => action.run(act);
  }
  el('banner-override').hidden = !action || !action.allowOverride;
}

// --- gate ------------------------------------------------------------------

gateSetUp({ help: 'ui.gate.programs', unlocked: refresh });

// --- rendering -------------------------------------------------------------

function pad(n) { return String(n).padStart(2, '0'); }


function renderClock() {
  if (!data) return;

  if (data.clock_ok || overrideClock) {
    if (overrideClock && !data.clock_ok) {
      banner(t('ui.clock.wrong.still'), 'warn', null);
    } else {
      banner('');
    }
    return;
  }
  const mins = Math.round(Math.abs(data.clock_drift_seconds) / 60);
  const off = mins >= 60 ? `${Math.round(mins / 60)} timer` : `${mins} minutter`;

  // Once the unit has shown that it acknowledges a clock write and ignores it,
  // offering the button again is offering a thing that does not work.
  if (data.clock_settable === false) {
    const why = data.clock_drift_is_dst
      ? t('ui.clock.dst.programs')
      : t('ui.clock.drift.programs', off);
    banner(
      t('ui.clock.panel.only', why),
      'warn',
      { label: t('ui.edit.anyway'), allowOverride: false, run: () => { overrideClock = true; renderClock(); render(); } },
    );
    return;
  }

  banner(
    t('ui.clock.drift.programs', off),
    'warn',
    {
      label: t('ui.clock.set'),
      allowOverride: true,
      run: async (button) => {
        const original = button.textContent;
        button.disabled = true;
        button.textContent = t('ui.setting.clock');
        try {
          if (await clockRequest('api/control/clock', 'POST', {})) await refresh();
        } catch (err) {
          // Fire and forget was how this failed silently: the banner cleared
          // and the clock stayed wrong.
          banner(t('ui.clock.set.failed', err.message), '', null);
        } finally {
          button.disabled = false;
          button.textContent = original;
        }
      },
    },
  );
}

// whyNothingRuns explains an idle schedule in terms of the unit's own clock,
// which is the clock that decides.
function whyNothingRuns() {
  if (!data || data.running) return '';
  const configured = data.week.filter((p) => p.days !== 0 && p.function !== 0);
  if (configured.length === 0 && data.year.every((p) => p.function === 0)) {
    return t('ui.program.none.set');
  }
  const drift = data.clock_drift_seconds || 0;
  const unitNow = new Date(Date.now() - drift * 1000);
  const hhmm = `${pad(unitNow.getHours())}:${pad(unitNow.getMinutes())}`;
  if (Math.abs(drift) > 300) {
    return t('ui.program.unit.time', hhmm);
  }
  return t('ui.program.none.cover', hhmm);
}

function render() {
  el('content').hidden = false;
  el('gate').hidden = true;
  // Coil 43 reports, it does not control: it says whether a program is in
  // effect at this moment. Offering it as a switch is what produced a server
  // device failure from the unit.
  el('running-dot').className = `dot ${data.running ? 'ok' : ''}`;
  el('running-label').textContent = data.running
    ? t('ui.program.running')
    : t('ui.program.idle');
  // "Nothing is running" is a puzzle on its own when a programme plainly
  // exists. The unit decides by its own clock, so that is the clock to
  // explain it with.
  el('running-why').textContent = whyNothingRuns();
  renderClock();
  renderWeek();
  renderYear();
}

function renderWeek() {
  const box = el('week');
  box.textContent = '';
  const shown = data.week.filter((p) => p.days !== 0 || p.function !== 0);
  if (shown.length === 0) {
    box.append(emptyNote(t('ui.program.week.none')));
  }
  for (const p of shown) box.append(weekRow(p));
  el('add-week').hidden = data.week.every((p) => p.days !== 0 || p.function !== 0);
}

function emptyNote(text) {
  const p = document.createElement('p');
  p.className = 'setting-help';
  p.textContent = text;
  return p;
}

function weekRow(program) {
  const row = document.createElement('div');
  row.className = 'program';

  const title = document.createElement('div');
  title.className = 'program-title';
  title.textContent = t('ui.program.week.n', program.index + 1);
  row.append(title);

  const days = document.createElement('div');
  days.className = 'days';
  for (const d of DAYS) {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'day';
    b.textContent = t(`ui.day.${d.key}.short`);
    b.title = t(`ui.day.${d.key}`);
    b.setAttribute('aria-label', t(`ui.day.${d.key}`));
    b.setAttribute('aria-pressed', String((program.days & d.bit) !== 0));
    b.addEventListener('click', () => {
      program.days ^= d.bit;
      b.setAttribute('aria-pressed', String((program.days & d.bit) !== 0));
    });
    days.append(b);
  }
  row.append(days);

  const times = document.createElement('div');
  times.className = 'times';
  const from = timeInput(`${pad(program.start_hour)}:${pad(program.start_minute)}`);
  const to = timeInput(`${pad(program.stop_hour)}:${pad(program.stop_minute)}`);
  times.append(labelled(t('ui.program.from'), from), labelled(t('ui.program.to'), to));
  row.append(times);

  const { select, fan } = functionPicker(program.function);
  row.append(labelled(t('ui.program.does'), select), fan.wrapper);

  const { save, feedback } = saveButton(async () => {
    const [sh, sm] = from.value.split(':').map(Number);
    const [eh, em] = to.value.split(':').map(Number);
    return put('api/programs/week', {
      program: {
        index: program.index, days: program.days,
        start_hour: sh, start_minute: sm, stop_hour: eh, stop_minute: em,
        function: chosenFunction(select, fan.input),
      },
    }, save, feedback);
  });
  row.append(saveRow(save, feedback, (b, f) => remove('week', program.index, t('ui.program.week.n', program.index + 1), b, f)));
  return row;
}

// saveButton builds the button and the line beside it that reports what
// happened, so the outcome appears where the action was taken.
function saveButton(run) {
  const save = document.createElement('button');
  save.className = 'primary program-save';
  save.textContent = t('ui.save');
  const feedback = document.createElement('span');
  feedback.className = 'save-feedback';
  save.addEventListener('click', run);

  if (!clockUsable()) {
    save.disabled = true;
    save.title = t('ui.clock.panel.first');
    feedback.className = 'save-feedback bad';
    feedback.textContent = t('ui.clock.first');
  }
  return { save, feedback };
}

function saveRow(save, feedback, onRemove) {
  const row = document.createElement('div');
  row.className = 'save-row';

  const del = document.createElement('button');
  del.type = 'button';
  del.className = 'ghost remove';
  del.textContent = t('ui.remove');
  // Never disabled by the clock guard: clearing a schedule cannot fire at the
  // wrong time, and it is the way out of a schedule that would.
  del.addEventListener('click', () => onRemove(del, feedback));

  row.append(save, del, feedback);
  return row;
}

async function remove(kind, index, name, button, feedback) {
  const ok = await confirmRemoval(name);
  if (!ok) return;
  // A write to the unit takes a noticeable moment. Without saying so the
  // button looks stuck, and the natural response is to press it again.
  button.disabled = true;
  const original = button.textContent;
  button.textContent = 'Fjerner…';
  feedback.className = 'save-feedback';
  feedback.textContent = '';
  try {
    const res = await fetch(`api/programs/${kind}?index=${index}`, { method: 'DELETE' });
    const answer = await res.json();
    if (!res.ok) throw new Error(answer.error || res.statusText);
    data = answer;
    render();
  } catch (err) {
    feedback.className = 'save-feedback bad';
    feedback.textContent = err.message;
    button.disabled = false;
    button.textContent = original;
  }
}

function confirmRemoval(name) {
  return new Promise((resolve) => {
    const dialog = el('confirm');
    el('confirm-text').textContent =
      t('ui.program.remove.confirm', name);
    el('confirm-ok').onclick = () => { dialog.close(); resolve(true); };
    el('confirm-cancel').onclick = () => { dialog.close(); resolve(false); };
    dialog.showModal();
  });
}

function clockUsable() {
  return !data || data.clock_ok || overrideClock;
}

function renderYear() {
  const box = el('year');
  box.textContent = '';
  const shown = data.year.filter((p) => p.function !== 0 || p.start_month !== 0);
  if (shown.length === 0) box.append(emptyNote(t('ui.program.year.none')));
  for (const p of shown) box.append(yearRow(p));
  el('add-year').hidden = data.year.every((p) => p.function !== 0 || p.start_month !== 0);
}

function yearRow(program) {
  const row = document.createElement('div');
  row.className = 'program';

  const title = document.createElement('div');
  title.className = 'program-title';
  title.textContent = t('ui.program.year.n', program.index + 1);
  row.append(title);

  const today = new Date();
  const dateValue = (d, m, y) =>
    m >= 1 && d >= 1 ? `${y}-${pad(m)}-${pad(d)}` : `${today.getFullYear()}-${pad(today.getMonth() + 1)}-${pad(today.getDate())}`;

  const times = document.createElement('div');
  times.className = 'times year-times';
  const fromDate = dateInput(dateValue(program.start_day, program.start_month, program.start_year));
  const fromTime = timeInput(`${pad(program.start_hour)}:${pad(program.start_minute)}`);
  const toDate = dateInput(dateValue(program.stop_day, program.stop_month, program.stop_year));
  const toTime = timeInput(`${pad(program.stop_hour)}:${pad(program.stop_minute)}`);
  times.append(labelled(t('ui.program.from'), fromDate), labelled(t('ui.program.at'), fromTime),
               labelled(t('ui.program.to'), toDate), labelled(t('ui.program.at'), toTime));
  row.append(times);

  const { select, fan } = functionPicker(program.function);
  row.append(labelled(t('ui.program.does'), select), fan.wrapper);

  const { save, feedback } = saveButton(async () => {
    const [sy, sm, sd] = fromDate.value.split('-').map(Number);
    const [ey, em, ed] = toDate.value.split('-').map(Number);
    const [sh, smin] = fromTime.value.split(':').map(Number);
    const [eh, emin] = toTime.value.split(':').map(Number);
    return put('api/programs/year', {
      program: {
        index: program.index,
        start_day: sd, start_month: sm, start_year: sy, start_hour: sh, start_minute: smin,
        stop_day: ed, stop_month: em, stop_year: ey, stop_hour: eh, stop_minute: emin,
        function: chosenFunction(select, fan.input),
      },
    }, save, feedback);
  });
  row.append(saveRow(save, feedback));
  return row;
}

// --- shared bits -----------------------------------------------------------

function labelled(text, control) {
  const wrap = document.createElement('label');
  wrap.className = 'field';
  const span = document.createElement('span');
  span.textContent = text;
  wrap.append(span, control);
  return wrap;
}

function timeInput(value) {
  const i = document.createElement('input');
  i.type = 'time';
  i.value = value;
  return i;
}

function dateInput(value) {
  const i = document.createElement('input');
  i.type = 'date';
  i.value = value;
  return i;
}

// The fan speed is a function value in its own right on EC fans, so it is an
// entry in the list that reveals a percentage rather than a separate control.
function functionPicker(current) {
  const select = document.createElement('select');
  for (const c of data.choices) {
    const o = document.createElement('option');
    o.value = String(c.value);
    o.textContent = c.label;
    if (c.help) o.title = c.help;
    select.append(o);
  }
  const fanOption = document.createElement('option');
  fanOption.value = 'fan';
  fanOption.textContent = t('ui.fan.level');
  select.append(fanOption);

  const isFan = current >= data.fan_min && current <= data.fan_max;
  select.value = isFan ? 'fan' : String(current);

  const input = document.createElement('input');
  input.type = 'number';
  input.min = data.fan_min;
  input.max = data.fan_max;
  input.step = 5;
  input.value = isFan ? current : 50;

  const wrapper = labelled('%', input);
  wrapper.hidden = !isFan;
  select.addEventListener('change', () => { wrapper.hidden = select.value !== 'fan'; });

  return { select, fan: { input, wrapper } };
}

function chosenFunction(select, fanInput) {
  return select.value === 'fan' ? Number(fanInput.value) : Number(select.value);
}

async function put(path, body, button, feedback) {
  button.disabled = true;
  const original = button.textContent;
  button.textContent = t('ui.saving');
  feedback.className = 'save-feedback';
  feedback.textContent = '';
  try {
    const res = await fetch(path, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...body, acknowledge_clock_drift: overrideClock }),
    });
    const answer = await res.json();
    if (!res.ok) throw new Error(answer.error || res.statusText);
    data = answer;
    render();
    // render() has rebuilt the row, so this element is detached; the message
    // goes to the fresh one instead.
    flashSaved(body.program.index, path.endsWith('week'));
  } catch (err) {
    // Beside the button, not in a banner at the top of a long page that a
    // phone has scrolled past.
    feedback.className = 'save-feedback bad';
    feedback.textContent = err.message;
    button.disabled = false;
    button.textContent = original;
  }
}

function flashSaved(index, isWeek) {
  const box = el(isWeek ? 'week' : 'year');
  const rows = box.querySelectorAll('.save-feedback');
  const row = rows[[...box.querySelectorAll('.program')].findIndex((p) =>
    p.querySelector('.program-title').textContent.endsWith(String(index + 1)))];
  if (!row) return;
  row.className = 'save-feedback good';
  row.textContent = t('ui.saved');
  setTimeout(() => {
    if (row.textContent === t('ui.saved')) {
      row.textContent = '';
      row.className = 'save-feedback';
    }
  }, 4000);
}

el('add-week').addEventListener('click', () => {
  const free = data.week.find((p) => p.days === 0 && p.function === 0);
  if (!free) return;
  free.days = 62;
  free.start_hour = 22;
  free.stop_hour = 7;
  free.stop_minute = 30;
  render();
});

el('add-year').addEventListener('click', () => {
  const free = data.year.find((p) => p.function === 0 && p.start_month === 0);
  if (!free) return;
  const now = new Date();
  free.start_day = now.getDate();
  free.start_month = now.getMonth() + 1;
  free.start_year = now.getFullYear();
  free.stop_day = now.getDate();
  free.stop_month = now.getMonth() + 1;
  free.stop_year = now.getFullYear();
  render();
});

el('banner-override').addEventListener('click', () => {
  overrideClock = true;
  renderClock();
});

el('menu-button').addEventListener('click', () => {
  const menu = el('menu');
  menu.hidden = !menu.hidden;
  el('menu-button').setAttribute('aria-expanded', String(!menu.hidden));
});

// --- wiring ----------------------------------------------------------------

async function refresh() {
  const status = await (await fetch('api/auth/status')).json();
  if (!status.authenticated) {
    gateShow(status, { help: 'ui.gate.programs' });
    el('gate').hidden = false;
    el('content').hidden = true;
    return;
  }
  const res = await fetch('api/programs');
  if (!res.ok) {
    banner((await res.json()).error || 'kunne ikke hente tidsprogram', '');
    return;
  }
  data = await res.json();
  render();
}

refresh().catch((err) => banner(err.message, ''));
