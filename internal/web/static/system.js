'use strict';

const el = (id) => document.getElementById(id);

let sys = null;
let notifyCfg = null;

function banner(text, cls) {
  const b = el('banner');
  b.hidden = !text;
  b.className = `banner ${cls || ''}`;
  if (text) el('banner-text').textContent = text;
}

// --- small builders --------------------------------------------------------

// skeleton fills a panel with shimmering bars while its figures are on their
// way. Counting pending updates shells out to apt, which takes about fifteen
// seconds on this hardware; a page that renders empty and fills in later reads
// as broken rather than as busy.
function skeleton(node, bars = 3) {
  if (!node) return;
  node.textContent = '';
  const box = document.createElement('div');
  box.className = 'loading';
  box.setAttribute('aria-busy', 'true');
  box.setAttribute('aria-label', 'Laster');
  for (let i = 0; i < bars; i++) {
    const bar = document.createElement('div');
    bar.className = 'bar';
    box.append(bar);
  }
  node.append(box);
}

function skeletons() {
  skeleton(el('health'), 5);
  skeleton(el('updates'), 5);
  skeleton(el('notify-state'), 4);
  skeleton(el('gateway'), 3);
  skeleton(el('unit'), 5);
  skeleton(el('host'), 4);
  skeleton(el('network'), 4);
}

function rows(parent, pairs) {
  parent.textContent = '';
  const box = document.createElement('div');
  box.className = 'facts';
  for (const [label, value, cls] of pairs) {
    if (value === null || value === undefined) continue;
    const dl = document.createElement('dl');
    dl.className = 'fact';
    const dt = document.createElement('dt');
    dt.textContent = label;
    const dd = document.createElement('dd');
    dd.textContent = value;
    if (cls) dd.className = cls;
    dl.append(dt, dd);
    box.append(dl);
  }
  parent.append(box);
}

const bytes = (n) => {
  if (!n) return '–';
  const u = ['B', 'kB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${num(n, i === 0 ? 0 : 1)} ${u[i]}`;
};

function duration(seconds) {
  if (!seconds) return '–';
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d) return `${d} d ${h} t`;
  if (h) return `${h} t ${m} min`;
  return `${m} min`;
}

// The local variable here used to be called t, which shadowed the t() that
// looks messages up — a name that worked only for as long as nothing in this
// function needed a message.
const when = (iso) => {
  if (!iso || iso.startsWith('0001')) return t('ui.never');
  const at = new Date(iso);
  const days = Math.floor((Date.now() - at) / 86400000);
  const shown = stamp(iso);
  return days > 1 ? t('ui.daysago', shown, days) : shown;
};

// --- sections --------------------------------------------------------------

function renderHealth() {
  const h = sys.host;
  const th = h.throttled;
  const facts = [
    [t('ui.sys.uptime'), duration(h.uptime_seconds)],
    [t('ui.sys.load'), h.load_average ? num(h.load_average, 2) : '–'],
    [t('ui.sys.memory'), t('ui.sys.memory.of', bytes(h.memory_used_bytes), bytes(h.memory_total_bytes))],
    [t('ui.sys.disk'), t('ui.sys.disk.free', bytes(h.disk_free_bytes), bytes(h.disk_total_bytes))],
    [t('ui.sys.temperature'), h.temperature_c ? `${num(h.temperature_c, 1)} °C` : '–'],
  ];
  // Under-voltage corrupts memory cards, throttles the processor and restarts
  // the box at random, and every one of those looks like a different bug until
  // somebody blames the charger. The count is what makes it actionable: one
  // event at boot is a different problem from thirty an hour.
  const p = sys.supply;
  if (p && p.watched) {
    let text = t('ui.sys.power.ok');
    let cls = 'good';
    if (p.now) {
      text = t('ui.sys.power.now');
      cls = 'bad';
    } else if (p.events === 1) {
      text = t('ui.sys.power.once', stamp(p.last));
      cls = p.recent ? 'bad' : 'warn';
    } else if (p.events > 1) {
      text = t('ui.sys.power.events', p.events, stamp(p.last));
      cls = p.recent ? 'bad' : 'warn';
    }
    facts.push([t('ui.sys.power'), text, cls]);
  } else if (th) {
    // Older kernels answer through the firmware word instead. Kept as the
    // fallback it always was, not as the only source it used to be.
    const now = th.under_voltage_now ? t('ui.sys.undervoltage.now') : th.throttled_now ? t('ui.sys.throttled.now') : null;
    const ever = th.under_voltage_ever ? t('ui.sys.undervoltage.ever') : th.throttled_ever ? t('ui.sys.throttled.ever') : null;
    facts.push([t('ui.sys.power'), now || ever || t('ui.sys.power.ok'), now ? 'bad' : ever ? 'warn' : 'good']);
  } else {
    facts.push([t('ui.sys.power'), t('ui.sys.power.unwatched'), '']);
  }
  rows(el('health'), facts);
}

function renderUpdates() {
  const u = sys.host.updates;
  const eolDays = u.distro_eol ? Math.round((new Date(u.distro_eol) - Date.now()) / 86400000) : null;
  const staleDays = u.last_success && !u.last_success.startsWith('0001')
    ? Math.floor((Date.now() - new Date(u.last_success)) / 86400000) : null;

  // The count is cached because it is slow. Saying when it was taken is the
  // difference between a stale number and a wrong one.
  const counted = u.checking ? t('ui.upd.counting')
    : u.error ? '–'
    : t('ui.upd.count', u.pending, u.security);

  rows(el('updates'), [
    [t('ui.upd.pending'), counted, u.checking ? '' : u.security > 0 ? 'warn' : 'good'],
    [t('ui.upd.auto'), u.unattended_upgrades ? t('ui.on') : t('ui.off.caps'), u.unattended_upgrades ? 'good' : 'bad'],
    [t('ui.upd.checked'), staleDays === null ? t('ui.unknown') : when(u.last_success),
      staleDays !== null && staleDays > 7 ? 'bad' : ''],
    [t('ui.upd.reboot'), u.reboot_required
      ? (u.reboot_because && u.reboot_because.length
        ? t('ui.upd.reboot.because', u.reboot_because.join(', ')) : t('ui.yes'))
      : t('ui.no'), u.reboot_required ? 'warn' : ''],
    // A box that stopped receiving security updates two years ago looks
    // exactly like one that is fine, unless something says so.
    [t('ui.upd.support'), eolDays === null ? t('ui.unknown')
      : eolDays > 0 ? t('ui.upd.support.until', dateOf(u.distro_eol), eolDays)
      : t('ui.upd.support.expired', dateOf(u.distro_eol)),
      eolDays === null ? '' : eolDays <= 0 ? 'bad' : eolDays < 180 ? 'warn' : 'good'],
    [t('ui.error'), u.error ? t('ui.upd.counterror', u.error) : null, 'bad'],
  ]);

  renderUpdateDetail(u);
  renderUpdateActions(u);
}

// renderUpdateDetail lists what is actually waiting, in a table with a heading
// that says what the list is. "17 ventende" tells nobody whether this is a
// kernel or a font, and a bare list of package names floating above a button
// tells them almost as little.
function renderUpdateDetail(u) {
  const box = el('update-detail');
  box.textContent = '';

  const apt = (sys.admin && sys.admin.apt) || {};
  if (apt.running) {
    box.append(note(apt.task === 'refresh'
      ? t('ui.upd.looking')
      : t('ui.upd.running')));
    if (aptLog) box.append(logBox(aptLog));
    return;
  }
  if (aptLog && aptDone) {
    box.append(note(aptFailed
      ? t('ui.upd.failed')
      : aptTask === 'refresh' ? t('ui.upd.refreshed') : t('ui.upd.done')));
    box.append(logBox(aptLog));
    if (aptFailed || aptTask !== 'refresh') return;
  }

  if (u.checking && !u.packages) {
    box.append(note(t('ui.upd.counting.long')));
    return;
  }
  if (!u.packages || u.packages.length === 0) {
    if (!u.error) box.append(note(t('ui.upd.uptodate')));
    return;
  }

  const heading = document.createElement('div');
  heading.className = 'card-label';
  heading.textContent = t('ui.upd.available', u.packages.length);
  box.append(heading);

  const table = document.createElement('table');
  table.className = 'packages';

  const thead = document.createElement('thead');
  const hrow = document.createElement('tr');
  for (const [text, cls] of [[t('ui.upd.package'), ''], [t('ui.upd.version'), 'num'], [t('ui.upd.kind'), 'upd-kind']]) {
    const th = document.createElement('th');
    th.textContent = text;
    if (cls) th.className = cls;
    hrow.append(th);
  }
  thead.append(hrow);
  table.append(thead);

  const tbody = document.createElement('tbody');
  for (const p of u.packages) {
    const tr = document.createElement('tr');
    if (p.security) tr.className = 'security';

    const name = document.createElement('td');
    name.className = 'pkg-name';
    name.textContent = p.name;

    const version = document.createElement('td');
    version.className = 'num';
    version.textContent = p.version || '–';

    // The word as well as the colour: a security update must not be something
    // you only notice if you can tell this red from that grey.
    const kind = document.createElement('td');
    kind.className = 'upd-kind';
    kind.textContent = p.security ? t('ui.upd.security') : t('ui.upd.ordinary');

    tr.append(name, version, kind);
    tbody.append(tr);
  }
  table.append(tbody);
  box.append(table);
}

function logBox(text) {
  const pre = document.createElement('pre');
  pre.className = 'log';
  pre.textContent = text;
  return pre;
}

function renderUpdateActions(u) {
  const box = el('update-actions');
  box.textContent = '';
  const admin = sys.admin || {};

  if (!admin.available) {
    box.append(note(t('ui.upd.notenabled', admin.why || t('ui.unknown'))));
    return;
  }

  // What to do about an update that wants a restart. A choice, because neither
  // answer is right for everybody: a box that restarts itself at four in the
  // morning is wrong for somebody watching a house, and a box that waits for a
  // person who never opens this page stays unpatched for months.
  const policy = select('reboot-policy', [
    ['notify', t('ui.reboot.notify')],
    ['auto', t('ui.reboot.auto')],
  ], admin.reboot_policy || 'notify');
  const at = document.createElement('input');
  at.type = 'time';
  at.id = 'reboot-at';
  at.value = admin.reboot_at || '04:00';

  const atField = field(t('ui.reboot.at'), at);
  atField.hidden = (admin.reboot_policy || 'notify') !== 'auto';
  policy.addEventListener('change', () => { atField.hidden = policy.value !== 'auto'; });

  const policyRow = document.createElement('div');
  policyRow.className = 'times';
  policyRow.append(field(t('ui.reboot.policy'), policy), atField);
  box.append(policyRow);

  const savePolicy = document.createElement('button');

  savePolicy.className = 'ghost';
  savePolicy.textContent = t('ui.save');
  const policyFeedback = document.createElement('span');
  policyFeedback.className = 'save-feedback';
  savePolicy.addEventListener('click', () => save(savePolicy, policyFeedback, () =>
    send('api/system/reboot-policy', 'PUT', {
      policy: policy.value,
      at: policy.value === 'auto' ? at.value : '',
    })));
  box.append(savePolicy, policyFeedback);

  const running = admin.apt && admin.apt.running;
  const feedback = document.createElement('span');
  feedback.className = 'save-feedback';

  // Looking is separate from installing, because after an upgrade the obvious
  // question is whether anything is left — and because the count comes from
  // apt's cached lists, which only a refresh brings up to date.
  const look = document.createElement('button');
  look.className = 'ghost';
  look.textContent = running && admin.apt.task === 'refresh' ? t('ui.upd.looking.short') : t('ui.upd.look');
  look.disabled = running;
  look.addEventListener('click', () => {
    save(look, feedback, async () => {
      const answer = await send('api/system/apt', 'POST', { task: 'refresh' });
      watchApt();
      return answer;
    });
  });

  const go = document.createElement('button');
  go.className = 'primary';
  go.textContent = running && admin.apt.task === 'upgrade' ? t('ui.upd.updating') : t('ui.upd.now');
  go.disabled = running || (!u.checking && u.pending === 0);
  go.addEventListener('click', () => {
    save(go, feedback, async () => {
      const answer = await send('api/system/apt', 'POST', { task: 'upgrade' });
      watchApt();
      return answer;
    });
  });

  box.append(go, look, feedback);
  if (!running && u.pending === 0 && !u.checking) {
    box.append(note(t('ui.upd.nothing')));
  }
}

// watchApt polls while apt works. Everything else on this page refreshes every
// thirty seconds, which is far too slow to watch something happen.
let aptLog = '';
let aptTask = '';
let aptDone = false;
let aptFailed = false;
let aptTimer = null;

function watchApt() {
  if (aptTimer) clearInterval(aptTimer);
  aptTimer = setInterval(async () => {
    let st;
    try {
      st = await send('api/system/apt', 'GET');
    } catch {
      // An upgrade can restart freewayd under us. A failed poll is expected in
      // that case, so it waits rather than declaring failure.
      return;
    }
    aptLog = st.log || '';
    aptTask = st.task || aptTask;
    if (st.running) {
      renderUpdateDetail(sys.host.updates);
      return;
    }
    clearInterval(aptTimer);
    aptTimer = null;
    aptDone = true;
    aptFailed = st.failed;
    // The count is recounted server-side once a job ends, so this comes back
    // for the new figure rather than showing the one from before.
    await refresh().catch(() => {});
  }, 3000);
}

function renderUnit() {
  const b = sys.bus, p = sys.poll, u = sys.unit, hist = sys.history;
  const facts = [
    [t('ui.unit.softwareversion'), u.software_version || '–'],
    [t('ui.unit.link'), u.link_status],
    [t('ui.unit.drift'), `${Math.round(u.clock_drift_seconds)} s`,
      Math.abs(u.clock_drift_seconds) > 300 ? 'warn' : 'good'],
    [t('ui.poll.passes'), t('ui.poll.passes.value', p.passes, p.failures)],
    [t('ui.poll.age'), `${num(p.snapshot_age_seconds)} s`],
    // Retries hide the raw rate, and a line that is degrading shows up here
    // long before anything visibly fails.
    [t('ui.bus.exchanges'), `${b.exchanges ?? 0}`],
    [t('ui.bus.errorrate'), `${num((b.recent_error_rate ?? 0) * 100, 2)} %`],
  ];
  if (hist) {
    const total = Object.values(hist.rows || {}).reduce((a, b2) => a + b2, 0);
    facts.push([t('ui.history'), t('ui.history.value', total.toLocaleString(), bytes(hist.size_bytes))]);
    if (hist.oldest && !hist.oldest.startsWith('0001')) facts.push([t('ui.history.oldest'), when(hist.oldest)]);
  }
  rows(el('unit'), facts);
  el('unit').prepend(unitNameField(sys.unit));
}

// unitNameField lets the unit be renamed, prefilled with whatever it is called
// now.
//
// The placeholder is the model the unit reported, so emptying the field is a
// way back to that rather than a way to a blank label — which is also why the
// field holds the configured name and not the one on screen.
function unitNameField(u) {
  // A .field, like every other text input on the site: the label above in
  // small muted type, the box itself bordered and padded the same way.
  const wrap = document.createElement('label');
  wrap.className = 'field unit-name';

  const label = document.createElement('span');
  label.textContent = t('ui.unit.name.label');

  const row = document.createElement('div');
  row.className = 'unit-name-row';

  const input = document.createElement('input');
  input.type = 'text';
  input.maxLength = 40;
  input.value = u.configured_name || '';
  input.placeholder = u.discovered_name || t('ui.aggregate.default');

  // Not called save: that is the name of the function below, and a local one
  // here would shadow it so that clicking tried to call a button. This has
  // happened once already, in the network section.
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'ghost';
  button.textContent = t('ui.save.name');
  const feedback = document.createElement('span');
  feedback.className = 'save-feedback';

  const apply = () => {
    if (input.value.trim() === (u.configured_name || '')) return;
    save(button, feedback, async () => {
      const answer = await send('api/system/unit-name', 'PUT', { name: input.value.trim() });
      await refresh();
      return answer;
    });
  };
  button.addEventListener('click', apply);
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); apply(); } });

  row.append(input, button, feedback);
  wrap.append(label, row);
  return wrap;
}

function renderHost() {
  const h = sys.host;
  // The build details live here rather than in the header. A bare commit hash
  // beside the menu reads as an error message to everyone who did not build it.
  const build = [sys.version, sys.built ? t('ui.built', sys.built) : null,
    sys.commit ? `(${sys.commit})` : null].filter(Boolean).join(' ');
  rows(el('host'), [
    [t('ui.hostname'), h.hostname],
    [t('ui.hardware'), h.model || '–'],
    [t('ui.os'), h.os],
    [t('ui.kernel'), `${h.kernel} (${h.arch})`],
    [t('ui.version'), build],
  ]);
}

function note(text) {
  const p = document.createElement('p');
  p.className = 'setting-help';
  p.textContent = text;
  return p;
}

// --- notifications ---------------------------------------------------------

// log is the notification history once it has been fetched, and how many rows
// are on screen. Both survive a refresh, so the list does not collapse or jump
// back to the top every thirty seconds under somebody who is reading it.
let log = null;
let logShown = 20;

// renderNotifyLog draws what the box has reported and where it went.
//
// The delivery line matters as much as the event: a condition raised while no
// channel was configured, or one below the level somebody chose, is exactly the
// thing that looks like nothing ever happened.
function languageNote() {
  return t('ui.notify.language', t(`ui.lang.${notifyCfg.language || 'no'}`));
}

function renderNotifyLog() {
  const box = el('notify-log');
  box.textContent = '';
  if (log === null) {
    skeleton(box, 3);
    loadLog();
    return;
  }
  if (log.error) {
    box.append(note(log.error));
    return;
  }
  if (log.events.length === 0) {
    box.append(note(t('ui.notify.log.none', log.days)));
    return;
  }

  for (const e of log.events.slice(0, logShown)) box.append(logRow(e));

  if (log.events.length > logShown) {
    const more = document.createElement('button');
    more.type = 'button';
    more.className = 'add';
    more.textContent = t('ui.notify.log.more');
    more.addEventListener('click', () => { logShown += 20; renderNotifyLog(); });
    box.append(more);
  }
  box.append(note(t('ui.notify.log.kept', log.retention_days)));
}

function logRow(e) {
  const row = document.createElement('div');
  row.className = 'alarm';

  const head = document.createElement('div');
  head.className = 'alarm-head';
  const name = document.createElement('span');
  // Resolved is not a problem, whatever the severity of what it ended.
  const critical = e.severity === 'critical';
  name.className = `alarm-name ${!e.resolved && critical ? 'bad' : ''}`;
  // Titles are catalogue ids for anything the box raises itself; t() hands
  // back whatever it does not recognise, which covers the rest.
  name.textContent = t(e.title);
  const chip = document.createElement('span');
  chip.className = `chip ${!e.resolved && critical ? 'alarm' : ''}`;
  chip.textContent = e.resolved ? t('ui.notify.log.resolved') : t(`ui.notify.sev.${e.severity}`);
  head.append(name, chip);

  const time = document.createElement('div');
  time.className = 'alarm-time';
  time.textContent = stamp(e.time);
  row.append(head, time);

  if (e.message) {
    const msg = document.createElement('p');
    msg.className = 'setting-help';
    msg.textContent = e.message;
    row.append(msg);
  }

  const how = document.createElement('div');
  how.className = 'alarm-time';
  how.textContent = e.delivered
    ? t('ui.notify.log.delivered', e.detail)
    : t('ui.notify.log.undelivered', t(e.detail || 'ui.unknown'));
  if (!e.delivered) how.classList.add('warn');
  row.append(how);
  return row;
}

// loadLog fetches the history the first time somebody opens it.
//
// Not with the rest of the page: this is a list somebody looks at now and
// again, and it has no business holding up a page opened to check something
// else — least of all on the refresh that runs every thirty seconds.
let loadingLog = false;
async function loadLog() {
  if (loadingLog) return;
  loadingLog = true;
  try {
    log = await send('api/notifications', 'GET');
  } catch (err) {
    // A box with no history file has no log; say so rather than shimmer
    // forever.
    log = { events: [], days: 0, retention_days: 0, error: err.message };
  } finally {
    loadingLog = false;
    renderNotifyLog();
  }
}

function renderNotify() {
  const st = sys.notify;
  rows(el('notify-state'), [
    [t('ui.notify.channels'), st.channels && st.channels.length ? st.channels.join(', ') : t('ui.notify.nochannels'),
      st.channels && st.channels.length ? 'good' : 'warn'],
    [t('ui.notify.sent'), t('ui.notify.sent.value', st.sent, st.failed), st.failed ? 'warn' : ''],
    [t('ui.notify.last'), when(st.last_sent)],
    [t('ui.notify.active'), String(st.active_conditions)],
    [t('ui.notify.lasterror'), st.last_error || null, 'bad'],
  ]);

  // The summary carries the count so the card can say whether there is
  // anything behind it without fetching a thing.
  el('notify-log-summary').textContent = st.logged
    ? t('ui.notify.log.show', st.logged)
    : t('ui.notify.log.empty');
  if (el('notify-log-box').open) {
    renderNotifyLog();
    if (log !== null) loadLog();
  }

  const form = el('notify-form');
  form.textContent = '';
  // Messages go out in one language, fixed when the form was saved — said
  // here, because nothing else on the page would tell anybody.
  const lang = note(languageNote());
  lang.id = 'notify-lang';
  lang.style.marginBottom = '12px';
  form.append(lang);

  form.append(field(t('ui.notify.from'), select('notify-min', [
    ['info', t('ui.notify.info')],
    ['warning', t('ui.notify.warning')],
    ['critical', t('ui.notify.critical')],
  ], notifyCfg.min_severity)));

  const hooks = document.createElement('div');
  hooks.className = 'hooks';
  const title = document.createElement('div');
  title.className = 'card-label';
  title.textContent = t('ui.webhooks');
  hooks.append(title);
  const list = document.createElement('div');
  list.id = 'hook-list';
  hooks.append(list);
  const add = document.createElement('button');
  add.type = 'button';
  add.className = 'add';
  add.textContent = t('ui.webhook.new');
  add.addEventListener('click', () => {
    notifyCfg.webhooks.push({ enabled: true, name: '', url: '', headers: {} });
    renderHooks();
  });
  hooks.append(add);
  form.append(hooks);
  renderHooksInto(list);

  form.append(renderSMTP());
}

// renderSMTP builds the mail form.
//
// Offered because the old adapter had it. A webhook is the sturdier choice —
// nothing to store on the box, and nobody who can withdraw a password and
// leave the alarms silent — so the form says which is which without labouring
// the point.
function renderSMTP() {
  const c = notifyCfg.smtp || (notifyCfg.smtp = { port: 587, starttls: true, to: [] });
  const box = document.createElement('div');
  box.className = 'hooks';

  const title = document.createElement('div');
  title.className = 'card-label';
  title.textContent = t('ui.email');
  box.append(title);

  const on = document.createElement('input');
  on.type = 'checkbox';
  on.className = 'switch';
  on.id = 'smtp-enabled';
  on.checked = !!c.enabled;

  const head = document.createElement('div');
  head.className = 'setting';
  const text = document.createElement('div');
  text.className = 'setting-text';
  const lbl = document.createElement('label');
  lbl.className = 'setting-label';
  lbl.htmlFor = 'smtp-enabled';
  lbl.textContent = t('ui.smtp.enable');
  const help = document.createElement('p');
  help.className = 'setting-help';
  help.textContent = t('ui.smtp.help');
  text.append(lbl, help);
  const ctl = document.createElement('div');
  ctl.className = 'setting-control';
  ctl.append(on);
  head.append(text, ctl);
  box.append(head);

  const fields = document.createElement('div');
  fields.className = 'times';

  const host = input('text', c.host || '', 'smtp.example.net');
  host.addEventListener('input', () => { c.host = host.value.trim(); });

  const port = input('number', c.port || 587, '587');
  port.min = 1;
  port.max = 65535;
  port.addEventListener('input', () => { c.port = Number(port.value) || 0; });

  const from = input('email', c.from || '', 'freeway@example.net');
  from.addEventListener('input', () => { c.from = from.value.trim(); });

  const to = input('text', (c.to || []).join(', '), 'meg@example.net');
  to.addEventListener('input', () => {
    c.to = to.value.split(',').map((x) => x.trim()).filter(Boolean);
  });

  const user = input('text', c.username || '', t('ui.smtp.username'));
  user.autocomplete = 'off';
  user.addEventListener('input', () => { c.username = user.value.trim(); });

  // Blank means "leave what is stored alone". The password is never sent out
  // by the server, so there is nothing here to prefill with, and an empty box
  // that wiped the stored one on every save would be a trap.
  const pass = input('password', '', c.has_password ? t('ui.smtp.password.kept') : t('ui.smtp.password'));
  pass.autocomplete = 'new-password';
  pass.addEventListener('input', () => { c.password = pass.value; });

  const tls = document.createElement('select');
  tls.id = 'smtp-tls';
  for (const [v, label] of [['starttls', t('ui.smtp.starttls')], ['tls', t('ui.smtp.tls')]]) {
    const o = document.createElement('option');
    o.value = v;
    o.textContent = label;
    tls.append(o);
  }
  tls.value = c.starttls ? 'starttls' : 'tls';
  tls.addEventListener('change', () => { c.starttls = tls.value === 'starttls'; });

  fields.append(
    field(t('ui.smtp.server'), host), field(t('ui.smtp.port'), port), field(t('ui.smtp.encryption'), tls),
    field(t('ui.smtp.from'), from), field(t('ui.smtp.to'), to),
    field(t('ui.smtp.username'), user), field(t('ui.smtp.password'), pass),
  );
  fields.hidden = !c.enabled;
  on.addEventListener('change', () => { c.enabled = on.checked; fields.hidden = !on.checked; });
  box.append(fields);
  return box;
}

function input(type, value, placeholder) {
  const i = document.createElement('input');
  i.type = type;
  i.value = value;
  if (placeholder) i.placeholder = placeholder;
  return i;
}

function renderHooks() { renderHooksInto(el('hook-list')); }

function renderHooksInto(list) {
  list.textContent = '';
  if (notifyCfg.webhooks.length === 0) {
    list.append(note(t('ui.webhook.none')));
  }
  notifyCfg.webhooks.forEach((h, i) => {
    const row = document.createElement('div');
    row.className = 'hook';

    const on = document.createElement('input');
    on.type = 'checkbox';
    on.className = 'switch';
    on.checked = h.enabled;
    on.addEventListener('change', () => { h.enabled = on.checked; });

    const name = document.createElement('input');
    name.type = 'text';
    name.placeholder = t('ui.name');
    name.value = h.name || '';
    name.addEventListener('input', () => { h.name = name.value; });

    const url = document.createElement('input');
    url.type = 'url';
    url.placeholder = 'https://…';
    url.value = h.url || '';
    url.className = 'hook-url';
    url.addEventListener('input', () => { h.url = url.value; });

    const del = document.createElement('button');
    del.type = 'button';
    del.className = 'ghost remove';
    del.textContent = t('ui.remove');
    del.addEventListener('click', () => { notifyCfg.webhooks.splice(i, 1); renderHooks(); });

    row.append(on, name, url, del);
    list.append(row);
  });
}

function field(label, control) {
  const wrap = document.createElement('label');
  wrap.className = 'field';
  const span = document.createElement('span');
  span.textContent = label;
  wrap.append(span, control);
  return wrap;
}

function select(id, options, value) {
  const s = document.createElement('select');
  s.id = id;
  for (const [v, label] of options) {
    const o = document.createElement('option');
    o.value = v;
    o.textContent = label;
    s.append(o);
  }
  s.value = value;
  return s;
}

// --- gateway ---------------------------------------------------------------

function renderGateway() {
  const g = sys.gateway;
  const box = el('gateway');
  box.textContent = '';
  if (!g) {
    box.append(note(t('ui.gateway.disabled')));
    return;
  }

  const on = document.createElement('input');
  on.type = 'checkbox';
  on.className = 'switch';
  on.id = 'gateway-enabled';
  on.checked = g.enabled;
  const enable = document.createElement('div');
  enable.className = 'setting';
  const text = document.createElement('div');
  text.className = 'setting-text';
  const lbl = document.createElement('label');
  lbl.className = 'setting-label';
  lbl.htmlFor = 'gateway-enabled';
  lbl.textContent = t('ui.gateway.on');
  // The label names the switch and the line under it says what is true. A
  // label that said "the gateway is on" stood beside a switch that was off,
  // and "listening on :502" was printed while nothing listened.
  const help = document.createElement('p');
  help.className = 'setting-help';
  const port = (g.listen || '').replace(/^.*:/, '') || '502';
  const describe = () => {
    help.textContent = on.checked && g.enabled
      ? t('ui.gateway.listening', g.listen)
      : on.checked ? t('ui.gateway.pending') : t('ui.gateway.closed', port);
  };
  on.addEventListener('change', describe);
  describe();
  text.append(lbl, help);
  const ctl = document.createElement('div');
  ctl.className = 'setting-control';
  ctl.append(on);
  enable.append(text, ctl);
  box.append(enable);

  const allow = document.createElement('textarea');
  allow.id = 'gateway-allow';
  allow.rows = Math.max(2, g.allow.length + 1);
  allow.value = (g.allow || []).join('\n');
  allow.placeholder = '192.0.2.0/24';
  box.append(field(t('ui.gateway.allow'), allow));

  if (g.suggested && g.suggested.length) {
    const s = note(t('ui.gateway.thisbox', g.suggested.join(', ')));
    box.append(s);
  }

  const clients = document.createElement('div');
  clients.className = 'card-label';
  clients.textContent = t('ui.connected.now');
  box.append(clients);
  if (!g.clients || g.clients.length === 0) {
    box.append(note(t('ui.gateway.noclients')));
  } else {
    for (const c of g.clients) {
      const row = document.createElement('div');
      row.className = 'client';
      const addr = document.createElement('span');
      addr.className = 'client-addr';
      addr.textContent = c.addr;
      const stats = document.createElement('span');
      stats.className = 'setting-help';
      const reqs = c.requests ?? 0;
      const errs = c.errors ?? 0;
      stats.textContent = t('ui.gateway.clientstats', reqs, errs);
      row.append(addr, stats);
      clients.after(row);
      box.append(row);
    }
  }
  const st = g.stats || {};
  box.append(note(t('ui.gateway.totals', st.served ?? 0, st.refused ?? 0)));
}

// --- restart and shut down --------------------------------------------------

function renderPower() {
  const box = el('power-actions');
  box.textContent = '';
  const admin = sys.admin || {};

  if (!admin.available) {
    box.append(note(t('ui.power.notenabled', admin.why || t('ui.unknown'))));
    return;
  }

  const feedback = document.createElement('span');
  feedback.className = 'save-feedback';

  const reboot = powerButton(t('ui.power.restart'), 'reboot', feedback, t('ui.power.restart.warn'));
  const off = powerButton(t('ui.power.off'), 'poweroff', feedback, t('ui.power.off.warn'));

  box.append(reboot, off, feedback);
}

// powerButton looks like every other button on the site and is red, and asks
// once in a dialog before it does anything.
function powerButton(label, what, feedback, warning) {
  const b = document.createElement('button');
  b.className = 'primary danger';
  b.textContent = label;
  b.addEventListener('click', async () => {
    if (!await confirmThen(warning, label)) return;
    save(b, feedback, () => send(`api/system/${what}`, 'POST', { confirm: what }));
  });
  return b;
}

// confirmThen is the same dialog the overview uses for stopping the fans and
// the programme page uses for removing a programme — one markup block per page,
// one helper, so a confirmation looks and behaves the same everywhere.
//
// Promise-returning rather than callback-taking, because the caller here has to
// wait for the answer before it decides whether to disable its button.
function confirmThen(text, confirmLabel) {
  return new Promise((resolve) => {
    const dialog = el('confirm');
    el('confirm-text').textContent = text;
    el('confirm-ok').textContent = confirmLabel;

    let answered = false;
    const finish = (answer) => {
      if (answered) return;
      answered = true;
      dialog.close();
      resolve(answer);
    };
    el('confirm-ok').onclick = () => finish(true);
    el('confirm-cancel').onclick = () => finish(false);
    // Escape closes it without either button being pressed, and a promise left
    // unsettled would leave the caller's button disabled forever.
    dialog.oncancel = () => finish(false);
    dialog.onclose = () => finish(false);

    dialog.showModal();
    // Focus on Avbryt, so a stray Enter cancels rather than reboots.
    el('confirm-cancel').focus();
  });
}

// --- network ---------------------------------------------------------------

let net = null;
// Which connection the card is showing. Null means the one carrying traffic,
// which is what the page opens on.
let netPick = null;

async function renderNetwork() {
  const query = netPick ? `api/network?connection=${encodeURIComponent(netPick)}` : 'api/network';
  net = await send(query, 'GET');
  const c = net.config;
  const box = el('network');
  box.textContent = '';

  rows(box, [
    [t('ui.net.connection'), `${c.connection} (${c.device})`],
    [t('ui.net.method'), c.dhcp ? 'DHCP' : t('ui.net.static')],
    [t('ui.net.address.now'), c.current_address || '–'],
    [t('ui.net.gateway.now'), c.current_gateway || '–'],
    [t('ui.net.dns.now'), (c.current_dns || []).join(', ') || '–'],
  ]);

  const actions = el('network-actions');
  actions.textContent = '';

  if (net.pending) {
    // The revert is already armed and counting. Confirming is the only thing
    // that matters on this page right now.
    const warn = note(t('ui.net.pending'));
    warn.className = 'setting-help';
    box.append(warn);
    const ok = document.createElement('button');
    ok.className = 'primary';
    ok.textContent = t('ui.net.confirm');
    ok.addEventListener('click', async () => {
      await send('api/network/confirm', 'POST', {});
      await renderNetwork();
    });
    actions.append(ok);
    return;
  }

  if (!c.writable) {
    // Says what to do rather than offering a control that fails.
    box.append(note(t('ui.net.notenabled', c.why)));
    return;
  }

  // With a cable in and wifi up there are two, and only one of them can be
  // the one on screen. Without this the card could only ever edit whichever
  // carried traffic at that moment, so giving the wifi a fixed address meant
  // unplugging the cable to do it.
  const links = c.links || [];
  if (links.length > 1) {
    const pick = select('net-which', links.map((l) => [
      l.connection,
      l.primary ? t('ui.net.primary', `${l.connection} (${l.device})`) : `${l.connection} (${l.device})`,
    ]), c.connection);
    pick.addEventListener('change', () => {
      netPick = pick.value;
      renderNetwork().catch((err) => banner(err.message, ''));
    });
    box.append(field(t('ui.net.which'), pick), note(t('ui.net.which.help')));
  }

  const method = select('net-method', [['dhcp', 'DHCP'], ['static', t('ui.net.static')]], c.dhcp ? 'dhcp' : 'static');
  const addr = document.createElement('input');
  addr.type = 'text';
  addr.id = 'net-address';
  addr.placeholder = '192.0.2.10/24';
  addr.value = c.address || c.current_address || '';
  const gw = document.createElement('input');
  gw.type = 'text';
  gw.id = 'net-gateway';
  gw.placeholder = '192.0.2.1';
  gw.value = c.gateway || c.current_gateway || '';
  const dns = document.createElement('input');
  dns.type = 'text';
  dns.id = 'net-dns';
  dns.placeholder = '192.0.2.1, 192.0.2.2';
  dns.value = (c.dns && c.dns.length ? c.dns : c.current_dns || []).join(', ');

  const staticFields = document.createElement('div');
  staticFields.className = 'times';
  staticFields.append(field(t('ui.net.address'), addr), field(t('ui.net.gateway'), gw), field(t('ui.net.dns'), dns));
  staticFields.hidden = c.dhcp;
  method.addEventListener('change', () => { staticFields.hidden = method.value === 'dhcp'; });

  box.append(field(t('ui.net.method'), method), staticFields);
  box.append(note(t('ui.net.revert', net.revert_after_seconds)));

  // Named for what it is rather than "save": a local called save would shadow
  // the save() below, and clicking would try to call a button.
  const apply = document.createElement('button');
  apply.className = 'primary';
  apply.textContent = t('ui.net.apply');
  const feedback = document.createElement('span');
  feedback.className = 'save-feedback';
  apply.addEventListener('click', () => {
    save(apply, feedback, () => send('api/network', 'PUT', {
      connection: c.connection,
      dhcp: method.value === 'dhcp',
      address: addr.value.trim(),
      gateway: gw.value.trim(),
      dns: dns.value.split(',').map((s) => s.trim()).filter(Boolean),
    }));
  });
  actions.append(apply, feedback);
}

// --- time ------------------------------------------------------------------
//
// The box's own clock, not the unit's. It is what the unit's clock gets set
// from, so a box left on the image's default timezone writes an hour's error
// into every timer programme — and nothing on screen says so, because the
// browser renders times in the reader's zone and they look right.
//
// So the zone is shown as a fact about the box, beside the time the box thinks
// it is, which is the pair that makes the error visible.

// reloadWhenBack waits for Freeway Pi to go down and come back, and reloads.
//
// A new timezone restarts the daemon, because it reads the zone once when it
// starts. The page would otherwise meet a dropped connection and say the box
// had gone away, when it had simply done what it was asked. A fixed pause is
// not enough on a Pi 2, so it asks until there is an answer again.
function reloadWhenBack() {
  const started = Date.now();
  const ask = () => {
    fetch('api/power', { cache: 'no-store' })
      .then((r) => (r.ok ? location.reload() : Promise.reject(new Error(r.statusText))))
      .catch(() => {
        if (Date.now() - started < 60000) setTimeout(ask, 1000);
      });
  };
  // The restart is scheduled a few seconds out; asking before then would find
  // the old daemon still answering and reload into it.
  setTimeout(ask, 5000);
}

async function renderTime() {
  const box = el('time');
  const actions = el('time-actions');
  box.textContent = '';
  actions.textContent = '';

  let tm;
  try {
    tm = (await send('api/system/time', 'GET')).time;
  } catch (err) {
    box.append(note(err.message));
    return;
  }

  rows(box, [
    [t('ui.time.now'), tm.now
      ? stamp(tm.now, { dateStyle: 'short', timeStyle: 'medium' })
      : '–'],
    [t('ui.time.zone'), tm.zone || '–'],
    [t('ui.time.sync'), tm.synchronised ? t('ui.yes') : tm.ntp ? t('ui.time.trying') : t('ui.no')],
    [t('ui.time.server'), tm.server_now || (tm.servers || []).join(', ') || t('ui.time.pool')],
  ]);

  if (!tm.writable) {
    box.append(note(t('ui.net.notenabled', tm.why || '')));
    return;
  }

  // The zone the browser is in, offered as the obvious answer. It is almost
  // always the right one, and typing "Europe/Oslo" from memory is not.
  const guess = Intl.DateTimeFormat().resolvedOptions().timeZone;
  if (guess && guess !== tm.zone) {
    box.append(note(t('ui.time.mismatch', guess)));
  }

  // A list rather than a text field: a zoneinfo name is not something anybody
  // types correctly from memory, and the browser already carries the database.
  // Where it does not, a text field is better than no control at all.
  let zone;
  const known = typeof Intl.supportedValuesOf === 'function'
    ? Intl.supportedValuesOf('timeZone') : null;
  if (known && known.length) {
    // The box's own zone goes in even when this browser has never heard of it,
    // or opening the page would silently offer to change it.
    const all = known.includes(tm.zone) || !tm.zone ? known : [tm.zone].concat(known);
    zone = select('time-zone', all.map((z) => [z, z]), tm.zone || guess || 'UTC');
  } else {
    zone = document.createElement('input');
    zone.type = 'text';
    zone.id = 'time-zone';
    zone.placeholder = guess || 'Europe/Oslo';
    zone.value = tm.zone || '';
  }

  const servers = document.createElement('input');
  servers.type = 'text';
  servers.id = 'time-servers';
  servers.placeholder = t('ui.time.pool');
  servers.value = (tm.servers || []).join(' ');

  const fields = document.createElement('div');
  fields.className = 'times';
  fields.append(field(t('ui.time.zone'), zone), field(t('ui.time.servers'), servers));
  box.append(fields);
  box.append(note(t('ui.time.help')));

  const save1 = document.createElement('button');
  save1.className = 'primary';
  save1.textContent = t('ui.save');
  const feedback = document.createElement('span');
  feedback.className = 'save-feedback';
  save1.addEventListener('click', () => save(save1, feedback, async () => {
    if (zone.value.trim() && zone.value.trim() !== tm.zone) {
      const answer = await send('api/system/time', 'PUT', { zone: zone.value.trim() });
      if (answer.restarting) {
        reloadWhenBack();
        return answer;
      }
    }
    const wanted = servers.value.trim().split(/\s+/).filter(Boolean);
    if (wanted.join(' ') !== (tm.servers || []).join(' ')) {
      await send('api/system/time', 'PUT', { set_ntp: true, servers: wanted });
    }
    await renderTime();
    return { note: t('ui.saved') };
  }));

  if (guess && guess !== tm.zone) {
    const useMine = document.createElement('button');
    useMine.className = 'ghost';
    useMine.textContent = t('ui.time.usemine', guess);
    useMine.addEventListener('click', () => save(useMine, feedback, async () => {
      const answer = await send('api/system/time', 'PUT', { zone: guess });
      if (answer.restarting) {
        reloadWhenBack();
        return answer;
      }
      await renderTime();
      return answer;
    }));
    actions.append(useMine);
  }
  actions.append(save1, feedback);
}

// --- pin -------------------------------------------------------------------
//
// Neither PIN crosses the network. The old one is proved exactly as a login
// proves it — HMAC(key, nonce) against the nonce that carried the salt — and
// the new one is turned into the stored secret here, in the browser, against a
// salt the browser picks. The server receives a proof and a key, never a PIN.
//
// The old PIN is required even though this page already sits behind a session.
// A session is a page left open on a phone in a kitchen; it should not be
// enough to change the lock.

function pinField(label, id) {
  const input = document.createElement('input');
  input.type = 'password';
  input.id = id;
  input.inputMode = 'numeric';
  input.autocomplete = 'off';
  input.maxLength = 12;
  return field(label, input);
}

function renderPin() {
  // Not "pin": this page already has an <input id="pin"> for logging in, and
  // el() would return that one. Appending to it put every field inside a void
  // element, where the browser silently drops them — so the card showed a
  // button alone and the button reported an empty PIN.
  const box = el('pin-change');
  const actions = el('pin-change-actions');
  box.textContent = '';
  actions.textContent = '';

  box.append(note(t('ui.pin.help')));

  const oldField = pinField(t('ui.pin.old'), 'pin-old');
  const newField = pinField(t('ui.pin.new'), 'pin-new');
  const againField = pinField(t('ui.pin.again'), 'pin-again');
  const wrap = document.createElement('div');
  wrap.className = 'times';
  wrap.append(oldField, newField, againField);
  box.append(wrap);
  box.append(note(t('ui.pin.card')));

  const change = document.createElement('button');
  change.className = 'primary';
  change.textContent = t('ui.pin.change');
  const feedback = document.createElement('span');
  feedback.className = 'save-feedback';

  change.addEventListener('click', () => {
    const before = oldField.querySelector('input').value;
    const after = newField.querySelector('input').value;
    const again = againField.querySelector('input').value;

    // Checked here so a typo costs nothing and never reaches the rate limiter,
    // which counts a failed change the same as a failed login.
    if (after !== again) {
      feedback.className = 'save-feedback bad';
      feedback.textContent = t('ui.pin.mismatch');
      return;
    }
    if (!/^[0-9]{4,12}$/.test(after)) {
      feedback.className = 'save-feedback bad';
      feedback.textContent = t('ui.pin.shape');
      return;
    }

    save(change, feedback, async () => {
      const p = await send('api/auth/challenge', 'GET');
      const oldKey = deriveKey(before, p.salt, p.iterations);
      // A salt of its own, so two PINs never share one.
      const salt = toHex(crypto.getRandomValues(new Uint8Array(16)));
      const key = deriveKey(after, salt, p.iterations);
      const answer = await send('api/auth/change', 'POST', {
        nonce: p.nonce,
        proof: proveNonce(oldKey, p.nonce),
        salt,
        key: toBase64(key),
      });
      for (const f of [oldField, newField, againField]) f.querySelector('input').value = '';
      return { note: t('ui.pin.changed') };
    });
  });

  actions.append(change, feedback);
}

// --- ssh -------------------------------------------------------------------
//
// The one setting on this page that can leave somebody with no way into their
// own box. So the three choices are not three equal buttons: each says what it
// costs, closing asks for confirmation, and the break-glass that needs no
// network at all is spelled out underneath rather than left in a README.

async function renderSSH() {
  const box = el('ssh');
  const actions = el('ssh-actions');
  box.textContent = '';
  actions.textContent = '';

  let ssh;
  try {
    ssh = (await send('api/ssh', 'GET')).ssh;
  } catch (err) {
    box.append(note(err.message));
    return;
  }

  const temporary = Boolean(ssh.until);
  const state = temporary
    ? t('ui.ssh.temporary', stamp(ssh.until))
    : (ssh.reachable ? t('ui.ssh.open') : t('ui.ssh.closed'));
  // Which account, because "add a key" is half an answer without it — and on
  // an image nobody has renamed anything on, it is not a name somebody guesses.
  rows(box, ssh.user
    ? [[t('ui.ssh.status'), state], [t('ui.ssh.user'), ssh.user]]
    : [[t('ui.ssh.status'), state]]);

  if (!ssh.firewall) {
    // Nothing here can do anything without a ruleset to change, and saying so
    // beats three buttons that quietly achieve nothing.
    box.append(note(t('ui.ssh.nofirewall')));
    return;
  }
  if (!ssh.writable) {
    box.append(note(t('ui.net.notenabled', ssh.why || '')));
    return;
  }

  box.append(note(ssh.policy === 'open' ? t('ui.ssh.help.open') : t('ui.ssh.help.closed')));

  // An open port with no key is a door with no handle. It is the state a box
  // flashed with anything but Raspberry Pi Imager starts in, and without this
  // the card looks like it is working.
  const keys = ssh.keys || [];
  if (keys.length === 0) {
    const warn = note(ssh.reachable ? t('ui.ssh.nokeys.open') : t('ui.ssh.nokeys'));
    warn.className = 'setting-help bad';
    box.append(warn);
  }
  box.append(keyList(ssh, keys));
  // Only when it is shut. "It can also be opened at boot" under "SSH is open"
  // is an answer to a question nobody is asking.
  if (ssh.policy !== 'open') box.append(note(t('ui.ssh.card')));

  const feedback = document.createElement('span');
  feedback.className = 'save-feedback';
  const put = (body, button) => save(button, feedback, async () => {
    const answer = await send('api/ssh', 'PUT', body);
    await renderSSH();
    return answer;
  });

  if (ssh.policy === 'open') {
    const close = document.createElement('button');
    close.className = 'primary danger';
    close.textContent = t('ui.ssh.close');
    close.addEventListener('click', async () => {
      if (!await confirmThen(t('ui.ssh.warn.close'), t('ui.ssh.close'))) return;
      put({ policy: 'closed' }, close);
    });
    actions.append(close, feedback);
    return;
  }

  // Closed. The common need is a few minutes of access, so that button comes
  // first and is the primary one.
  const thirty = document.createElement('button');
  thirty.className = 'primary';
  thirty.textContent = t('ui.ssh.open30');
  thirty.addEventListener('click', () => put({ minutes: 30 }, thirty));

  const forever = document.createElement('button');

  forever.className = 'ghost';
  forever.textContent = t('ui.ssh.openperm');
  forever.addEventListener('click', async () => {
    if (!await confirmThen(t('ui.ssh.warn.perm'), t('ui.ssh.openperm'))) return;
    put({ policy: 'open' }, forever);
  });

  actions.append(thirty, forever);
  if (temporary) {
    const shut = document.createElement('button');
    shut.className = 'ghost';
    shut.textContent = t('ui.ssh.shut');
    shut.addEventListener('click', () => put({}, shut));
    actions.append(shut);
  }
  actions.append(feedback);
}

// keyList shows who may log in, and lets somebody say who else may.
//
// Fingerprints, not keys: a public key is not a secret, but there is nothing
// useful to do with one here and a wall of base64 tells nobody anything.
//
// Pasting a key into a page served over plain HTTP is fine. The key is public,
// and anybody who could substitute one already has the PIN this sits behind.
function keyList(ssh, keys) {
  const wrap = document.createElement('div');
  wrap.append(rowsOf(keys.map((k) => [
    k.comment || k.type || t('ui.ssh.key'),
    k.fingerprint,
    k.fingerprint,
  ])));

  const input = document.createElement('input');
  input.type = 'text';
  input.id = 'ssh-key';
  input.autocomplete = 'off';
  input.spellcheck = false;
  input.placeholder = 'ssh-ed25519 …';

  const add = document.createElement('button');

  add.className = 'ghost';
  add.textContent = t('ui.ssh.key.add');
  const feedback = document.createElement('span');
  feedback.className = 'save-feedback';
  add.addEventListener('click', () => save(add, feedback, async () => {
    const answer = await send('api/ssh/keys', 'POST', { key: input.value.trim() });
    input.value = '';
    await renderSSH();
    return answer;
  }));

  const row = document.createElement('div');
  row.className = 'times';
  row.append(field(t('ui.ssh.key.paste'), input), add, feedback);
  wrap.append(row);
  return wrap;
}

// rowsOf draws the installed keys, each with a way to take it away again.
function rowsOf(entries) {
  const list = document.createElement('div');
  if (entries.length === 0) return list;
  for (const [label, fingerprint] of entries) {
    const row = document.createElement('div');
    row.className = 'wifi-row';
    const name = document.createElement('span');
    name.className = 'wifi-name';
    name.textContent = label;
    const fpr = document.createElement('span');
    fpr.className = 'wifi-meta';
    fpr.textContent = fingerprint;
    const del = document.createElement('button');
    del.className = 'ghost remove';
    del.textContent = t('ui.remove');
    del.addEventListener('click', async () => {
      if (!await confirmThen(t('ui.ssh.key.remove.confirm', label), t('ui.remove'))) return;
      await send('api/ssh/keys', 'POST', { fingerprint });
      await renderSSH();
    });
    row.append(name, fpr, del);
    list.append(row);
  }
  return list;
}

// --- wifi ------------------------------------------------------------------
//
// Freeway Pi wants a cable. But a ventilation unit does not always stand where
// an ethernet socket does, and preferring something is not a reason to leave
// somebody with no way to set the box up — so wifi works, and this is where it
// is configured. At flashing time Raspberry Pi Imager has already asked for the
// network, which is why the installer does not.
//
// The card stays hidden on a box with no radio rather than showing a control
// that can never do anything.

let wifi = null;
// The SSID whose password field is open. One at a time, so the card does not
// turn into a column of empty password boxes.
let joining = null;

async function renderWiFi() {
  const card = el('wifi-card');
  const box = el('wifi');
  const actions = el('wifi-actions');

  // Two very different reasons for an empty card, and only one of them should
  // hide it. No radio is the normal case and the card has nothing to say; a
  // request that failed is a fault, and silently showing nothing at all is how
  // somebody ends up looking for a setting that is right there.
  try {
    wifi = await send('api/wifi', 'GET');
  } catch (err) {
    card.hidden = false;
    actions.textContent = '';
    box.textContent = '';
    box.append(note(err.message));
    return;
  }

  const w = wifi.wifi;
  card.hidden = !w.supported;
  if (!w.supported) return;

  box.textContent = '';
  actions.textContent = '';

  rows(box, [
    [t('ui.wifi.device'), w.device || '–'],
    [t('ui.status'), w.ssid
      ? t('ui.wifi.connected', w.ssid) + (w.signal ? ` · ${w.signal}%` : '')
      : t('ui.wifi.notconnected')],
    [t('ui.wifi.address'), w.address || '–'],
    [t('ui.wifi.country'), w.country || '–'],
  ]);

  if (wifi.pending) {
    // The same armed revert the wired change uses, and the same single
    // confirm: whichever kind of change is counting down, this stops it.
    const warn = note(t('ui.wifi.pending'));
    warn.className = 'setting-help';
    box.append(warn);
    const ok = document.createElement('button');
    ok.className = 'primary';
    ok.textContent = t('ui.net.confirm');
    ok.addEventListener('click', async () => {
      await send('api/network/confirm', 'POST', {});
      await Promise.all([renderWiFi(), renderNetwork()]);
    });
    actions.append(ok);
    return;
  }

  if (!w.writable) {
    box.append(note(t('ui.net.notenabled', w.why)));
    return;
  }

  // Both, not one or the other. How to set wifi up from the card is true
  // whatever the box is doing; where it moves when the cable goes is only true
  // when there is a cable to lose. Choosing between them hid the instructions
  // on exactly the box that has wifi configured and a cable plugged in.
  if (w.address && !w.connected) box.append(note(t('ui.wifi.standby', w.address)));
  box.append(note(t('ui.wifi.cable')));

  // The switch comes before everything else. With it off there is nothing to
  // scan for and nothing to join, and an empty list would blame the hardware
  // for a setting.
  if (!w.radio) {
    box.append(note(t('ui.wifi.radio.off')));
    const on = document.createElement('button');
    on.className = 'primary';
    on.textContent = t('ui.wifi.radio.on');
    const feedback = document.createElement('span');
    feedback.className = 'save-feedback';
    on.addEventListener('click', () => save(on, feedback, async () => {
      const answer = await send('api/wifi/radio', 'POST', {});
      await renderWiFi();
      return answer;
    }));
    actions.append(on, feedback);
    return;
  }

  // The country comes next when it is missing, because until it is set the
  // radio is blocked and everything below it finds nothing.
  if (!w.country) box.append(countryField(box));

  const list = document.createElement('div');
  list.id = 'wifi-list';
  box.append(list);
  if (scanned) drawNetworks(list, scanned);

  const scan = document.createElement('button');

  scan.className = 'ghost';
  scan.textContent = t('ui.wifi.scan');
  const feedback = document.createElement('span');
  feedback.className = 'save-feedback';
  scan.addEventListener('click', async () => {
    scan.disabled = true;
    const was = scan.textContent;
    scan.textContent = t('ui.wifi.scanning');
    feedback.className = 'save-feedback';
    feedback.textContent = '';
    try {
      const answer = await send('api/wifi/scan', 'POST', {});
      scanned = answer.networks || [];
      drawNetworks(list, scanned);
    } catch (err) {
      feedback.className = 'save-feedback bad';
      feedback.textContent = err.message;
    } finally {
      scan.disabled = false;
      scan.textContent = was;
    }
  });
  actions.append(scan, feedback);
}

// The last scan, kept so a thirty-second refresh does not wipe the list out
// from under somebody halfway through typing a password.
let scanned = null;

function countryField(box) {
  const input = document.createElement('input');
  input.type = 'text';
  input.id = 'wifi-country';
  input.maxLength = 2;
  input.placeholder = 'NO';
  input.style.textTransform = 'uppercase';

  const set = document.createElement('button');

  set.className = 'ghost';
  set.textContent = t('ui.wifi.country.save');
  const feedback = document.createElement('span');
  feedback.className = 'save-feedback';
  set.addEventListener('click', () => {
    save(set, feedback, async () => {
      const answer = await send('api/wifi/country', 'POST', { country: input.value.trim().toUpperCase() });
      await renderWiFi();
      return answer;
    });
  });

  const wrap = document.createElement('div');
  wrap.append(note(t('ui.wifi.country.missing')));
  const row = document.createElement('div');
  row.className = 'times';
  row.append(field(t('ui.wifi.country'), input), set, feedback);
  wrap.append(row);
  return wrap;
}

function drawNetworks(list, networks) {
  list.textContent = '';
  if (networks.length === 0) {
    list.append(note(t('ui.wifi.none')));
    return;
  }
  for (const n of networks) {
    const row = document.createElement('div');
    row.className = 'wifi-row';

    const name = document.createElement('span');
    name.className = 'wifi-name';
    name.textContent = n.ssid;

    const meta = document.createElement('span');
    meta.className = 'wifi-meta';
    const bits = [`${n.signal}%`];
    if (n.open) bits.push(t('ui.wifi.open'));
    else if (n.security) bits.push(n.security);
    if (n.saved) bits.push(t('ui.wifi.saved'));
    meta.textContent = bits.join(' · ');

    const buttons = document.createElement('span');
    buttons.className = 'wifi-buttons';

    if (n.active) {
      const here = document.createElement('span');
      here.className = 'wifi-meta';
      here.textContent = '✓';
      buttons.append(here);
    } else {
      const join = document.createElement('button');
      join.className = 'ghost';
      join.textContent = t('ui.wifi.join');
      const joinFeedback = document.createElement('span');
      joinFeedback.className = 'save-feedback';
      join.addEventListener('click', () => {
        // A network this box already has the passphrase for, or one that needs
        // none, is joined on the spot. Anything else opens a password field.
        if (n.saved || n.open) {
          save(join, joinFeedback, () => join_(n.ssid, ''));
        } else {
          joining = joining === n.ssid ? null : n.ssid;
          drawNetworks(list, networks);
        }
      });
      buttons.append(join, joinFeedback);
    }

    if (n.saved) {
      const forget = document.createElement('button');
      forget.className = 'ghost remove';
      forget.textContent = t('ui.wifi.forget');
      forget.addEventListener('click', async () => {
        if (!await confirmThen(t('ui.wifi.forget.confirm', n.ssid), t('ui.wifi.forget'))) return;
        await send('api/wifi/forget', 'POST', { ssid: n.ssid });
        await renderWiFi();
      });
      buttons.append(forget);
    }

    row.append(name, meta, buttons);
    list.append(row);

    if (joining === n.ssid) list.append(passwordRow(n, list, networks));
  }
}

// Named with a trailing underscore because the buttons above are called join,
// and a local shadowing a function is the mistake this file has made twice.
async function join_(ssid, psk) {
  const answer = await send('api/wifi/join', 'POST', { ssid, psk });
  joining = null;
  // Dropped so the next render does not show the pre-join picture — which
  // network is active has just changed.
  scanned = null;
  await renderWiFi();
  return { note: answer.needs_confirm ? answer.note : t('ui.wifi.joined.nocable') };
}

function passwordRow(n, list, networks) {
  const wrap = document.createElement('div');
  wrap.className = 'wifi-join';

  const pass = document.createElement('input');
  pass.type = 'password';
  pass.autocomplete = 'off';
  pass.placeholder = t('ui.wifi.password');

  const go = document.createElement('button');
  go.className = 'primary';
  go.textContent = t('ui.wifi.join');
  const feedback = document.createElement('span');
  feedback.className = 'save-feedback';

  const attempt = () => save(go, feedback, async () => {
    const answer = await join_(n.ssid, pass.value);
    // Never left lying in the DOM once it has been handed over.
    pass.value = '';
    return answer;
  });

  go.addEventListener('click', attempt);
  pass.addEventListener('keydown', (e) => { if (e.key === 'Enter') attempt(); });

  const cancel = document.createElement('button');
  cancel.className = 'ghost';
  cancel.textContent = t('ui.wifi.cancel');
  cancel.addEventListener('click', () => { joining = null; drawNetworks(list, networks); });

  wrap.append(pass, go, cancel, feedback);
  if (n.open) wrap.prepend(note(t('ui.wifi.open.warning')));
  setTimeout(() => pass.focus(), 0);
  return wrap;
}

// --- saving ----------------------------------------------------------------

async function save(button, feedback, run) {
  button.disabled = true;
  const original = button.textContent;
  button.textContent = t('ui.saving');
  feedback.className = 'save-feedback';
  feedback.textContent = '';
  try {
    const answer = await run();
    feedback.className = 'save-feedback good';
    feedback.textContent = answer.note || t('ui.saved');
  } catch (err) {
    feedback.className = 'save-feedback bad';
    feedback.textContent = err.message;
  } finally {
    button.disabled = false;
    button.textContent = original;
  }
}

async function send(path, method, body) {
  const res = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const answer = await res.json();
  if (!res.ok) throw new Error(answer.error || res.statusText);
  return answer;
}

el('notify-save').addEventListener('click', () => {
  save(el('notify-save'), el('notify-feedback'), async () => {
    notifyCfg.min_severity = el('notify-min').value;
    const answer = await send('api/notify', 'PUT', notifyCfg);
    // Saving is what sets the language, so the note has to follow at once.
    notifyCfg.language = answer.language;
    el('notify-lang').textContent = languageNote();
    return answer;
  });
});

el('notify-test').addEventListener('click', () => {
  save(el('notify-test'), el('notify-feedback'), async () => {
    await send('api/notify/test', 'POST', {});
    return { note: t('ui.sent.check') };
  });
});

el('gateway-save').addEventListener('click', () => {
  save(el('gateway-save'), el('gateway-feedback'), () => send('api/system/gateway', 'PUT', {
    enabled: el('gateway-enabled').checked,
    allow: el('gateway-allow').value.split('\n').map((s) => s.trim()).filter(Boolean),
  }));
});

// --- wiring ----------------------------------------------------------------

el('menu-button').addEventListener('click', () => {
  const menu = el('menu');
  menu.hidden = !menu.hidden;
  el('menu-button').setAttribute('aria-expanded', String(!menu.hidden));
});

// Opening it is what fetches it, and reopening it freshens what is already
// there rather than replacing the list with a shimmer.
el('notify-log-box').addEventListener('toggle', () => {
  if (!el('notify-log-box').open) return;
  renderNotifyLog();
  if (log !== null) loadLog();
});

gateSetUp({ help: 'ui.gate.system', unlocked: refresh });

async function refresh() {
  const status = await (await fetch('api/auth/status')).json();
  if (!status.authenticated) {
    // Worded for whichever this is: no PIN yet, or one to be typed. Saying
    // "unlock" to somebody who has nothing to unlock with is a dead end.
    gateShow(status, { help: 'ui.gate.system' });
    el('gate').hidden = false;
    el('content').hidden = true;
    return;
  }
  el('gate').hidden = true;
  const first = el('content').hidden;
  el('content').hidden = false;
  // Only on the way in. Replacing filled panels with shimmer every thirty
  // seconds would be worse than the blank ones this is here to fix.
  if (first) skeletons();

  const [system, notifyConfig] = await Promise.all([
    send('api/system', 'GET'),
    send('api/notify', 'GET'),
  ]);
  sys = system;
  notifyCfg = notifyConfig;

  renderHealth();
  renderUpdates();
  renderNotify();
  renderGateway();
  const drawing = [
    renderNetwork().catch(() => {}),
    renderWiFi().catch((err) => banner(err.message, '')),
    renderSSH().catch((err) => banner(err.message, '')),
  ];
  renderPin();
  drawing.push(renderTime().catch((err) => banner(err.message, '')));
  renderUnit();
  renderHost();
  renderPower();

  // A link to a card — the overview's clock warning sends people to #clock —
  // arrives while the content is still hidden behind the PIN gate, so the
  // browser's own jump lands nowhere. Jump once the cards above it have been
  // drawn, or they push it back down the page as they fill in.
  if (first && location.hash) {
    Promise.all(drawing).then(() => {
      let target = null;
      try { target = document.querySelector(location.hash); } catch (err) { /* not a selector */ }
      if (target) target.scrollIntoView({ block: 'start' });
    });
  }

  // The count is taken in the background because it is slow. Coming back for
  // it is the difference between "teller…" resolving by itself and sitting
  // there until somebody reloads.
  if (sys.host.updates.checking) {
    clearTimeout(recheck);
    recheck = setTimeout(() => refresh().catch(() => {}), 4000);
  }
  // A job started from another browser should still be watched here.
  if (sys.admin && sys.admin.apt && sys.admin.apt.running && !aptTimer) {
    watchApt();
  }
}

let recheck = null;

refresh().catch((err) => banner(err.message, ''));
// Not while somebody is using the page: refresh() rebuilds every card, and a
// tick that lands mid-sentence takes what was being typed with it.
setInterval(() => {
  if (document.hidden || !sys || busyTyping()) return;
  refresh().catch(() => {});
}, 30000);
