'use strict';

// Charts for the measurement history. uPlot is vendored rather than loaded from
// a CDN: the box has to work when the internet does not.

const el = (id) => document.getElementById(id);

// The categorical palette, validated with the data-visualisation validator
// against the surfaces these charts actually render on: every check passes in
// both modes. Slots are assigned in fixed order and never cycled.
//
// Light mode warns that aqua and yellow fall below 3:1 against white, which
// obliges visible labels — the legend under each chart is always present, so
// identity never rests on colour alone.
const PALETTE = {
  light: ['#2a78d6', '#eb6834', '#1baf7a', '#eda100'],
  dark: ['#3987e5', '#d95926', '#199e70', '#c98500'],
};

// The temperature chart shows the four air types, so it follows NS 5575 like
// the house does rather than inventing a second language for the same thing:
// outdoor blue, supply red, extract yellow, exhaust green, in that order.
//
// The validator's verdict, since this is a deliberate departure: every hard
// gate passes in both modes — CVD separation 15.3 light and 11.7 dark against
// a target of 8, normal-vision separation 20.8 and 21.1 against a floor of 15.
// What fails is the dark lightness band, because the standard names yellow and
// yellow is intrinsically light; darkening it to fit the band collapses it into
// the green for protanopia, which is the failure that would actually matter.
// Light mode warns on yellow's contrast, which the legend answers.
const AIR_PALETTE = {
  light: ['#2a78d6', '#e34948', '#eda100', '#008300'],
  dark: ['#4d9bef', '#ff7b6b', '#ffcf4d', '#2fbf72'],
};

const INK = {
  light: { axis: '#52514e', grid: '#e1e0d9' },
  dark: { axis: '#c3c2b7', grid: '#2c2c2a' },
};

const CHARTS = [
  {
    id: 'chart-temp',
    unit: '°C',
    metrics: ['temp_fresh', 'temp_supply', 'temp_extract', 'temp_waste'],
    palette: AIR_PALETTE,
  },
  {
    id: 'chart-pct',
    unit: '%',
    metrics: ['fan_setpoint', 'fan_actual', 'efficiency_supply', 'efficiency_extract'],
  },
  // What a person did to the unit, under what the unit then did about it.
  {
    id: 'chart-overpressure',
    unit: '',
    height: 96,
    metrics: ['overpressure'],
  },
  // Freeway Pi's own, and last: it is not what anybody opens this page for,
  // but it is the only place a connector working loose is visible before it
  // becomes silence. The live figure is the last seven minutes, so without
  // this a burst is gone before it can be looked at.
  {
    id: 'chart-bus',
    unit: '%',
    metrics: ['bus_error_rate'],
  },
  // The supply under the box itself. Short, because it is a band rather than a
  // shape: the only questions are when and how often, and both are answered by
  // where the marks fall on the same time axis as everything above.
  {
    id: 'chart-power',
    unit: '',
    height: 96,
    metrics: ['undervoltage'],
  },
];

const RANGES = [
  { label: t('ui.range.6h'), hours: 6 },
  { label: t('ui.range.24h'), hours: 24 },
  { label: t('ui.range.7d'), hours: 24 * 7 },
  { label: t('ui.range.30d'), hours: 24 * 30 },
  { label: t('ui.range.1y'), hours: 24 * 365 },
];

let hours = 24;
const plots = new Map();

// The chart palettes are drawn by script and cannot read the stylesheet, so the
// choice made in the header has to be consulted here too: the attribute if one
// is set, and the system otherwise.
// The download follows the range buttons, so what comes out is what is on the
// screen rather than a fixed day somebody has to guess at.
function renderDownload() {
  const range = RANGES.find((r) => r.hours === hours);
  const label = range ? range.label : `${hours} t`;
  el('download').href = `api/series.csv?hours=${hours}`;
  el('download-point').href = `api/series.csv?hours=${hours}&decimal=point`;
  el('download-note').textContent = t('ui.download.note', label);
}

function theme() {
  const chosen = document.documentElement.getAttribute('data-theme');
  if (chosen === 'dark' || chosen === 'light') return chosen;
  return matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function banner(text) {
  const b = el('banner');
  b.hidden = !text;
  if (text) el('banner-text').textContent = text;
}

// --- data ------------------------------------------------------------------

// Series come back with their own timestamps. They are written together and so
// normally align, but an outage can leave one short, and uPlot needs a single
// x axis with nulls where a series has nothing.
function align(seriesList) {
  const stamps = new Set();
  for (const s of seriesList) for (const p of s.points || []) stamps.add(p.t);
  const xs = [...stamps].sort((a, b) => a - b);
  const index = new Map(xs.map((t, i) => [t, i]));

  const ys = seriesList.map((s) => {
    const col = new Array(xs.length).fill(null);
    for (const p of s.points || []) col[index.get(p.t)] = p.v;
    return col;
  });
  return [xs.map((t) => new Date(t).getTime() / 1000), ...ys];
}

async function load() {
  const results = await Promise.all(
    CHARTS.map(async (chart) => {
      const url = `api/series?metric=${chart.metrics.join(',')}&hours=${hours}`;
      const res = await fetch(url);
      const body = await res.json();
      if (!res.ok) throw new Error(body.error || res.statusText);
      return body;
    }),
  );

  const empty = results.every((r) => r.every((s) => !s.points || s.points.length === 0));
  if (empty) {
    banner(
      t('ui.data.empty'),
    );
  } else {
    banner('');
  }

  const resolutions = new Set();
  results.forEach((series, i) => {
    series.forEach((s) => s.points && s.points.length && resolutions.add(s.resolution));
    draw(CHARTS[i], series);
  });
  el('resolution').textContent = resolutions.size ? t('ui.resolution', [...resolutions].join(', ')) : '';
}

// --- drawing ---------------------------------------------------------------

function draw(chart, series) {
  const box = el(chart.id);
  const mode = theme();
  const colours = (chart.palette || PALETTE)[mode];
  const ink = INK[mode];

  const data = align(series);
  // A series that is only ever on or off is drawn as a band: a line wandering
  // between two values invites reading the height, and there is no height to
  // read. Rolled up, a bucket holds the share of the finer buckets under it
  // that were marked, so the band's depth is how often rather than how long.
  const band = series.length > 0 && series.every((s) => s.binary);

  const opts = {
    width: box.clientWidth || 320,
    height: chart.height || 240,
    // uPlot's cursor legend is the hover layer: a crosshair with every series'
    // value at that moment, which is what a reader actually wants from a chart
    // with four lines on it.
    cursor: { y: false, points: { size: 7 } },
    legend: { live: true },
    scales: { x: { time: true }, y: band ? { range: [0, 1] } : {} },
    axes: [
      { stroke: ink.axis, grid: { stroke: ink.grid, width: 1 }, ticks: { stroke: ink.grid } },
      {
        stroke: ink.axis,
        grid: { stroke: ink.grid, width: 1 },
        ticks: { stroke: ink.grid },
        size: 48,
        // A band has no value axis worth reading; the gridlines stay so the
        // marks line up with the charts above.
        show: !band,
        values: (u, vals) => vals.map((v) => `${v}${chart.unit}`),
      },
    ],
    series: [
      { label: t('ui.chart.time') },
      ...series.map((s, i) => ({
        label: s.label,
        stroke: colours[i % colours.length],
        // Thin marks: 2px lines, no fill, no point markers on a dense series.
        width: band ? 1 : 2,
        fill: band ? colours[i % colours.length] + '59' : undefined,
        value: (u, v) => {
          if (v == null) return '—';
          // Under a band, the number is how much of the interval was marked,
          // which at full resolution is simply yes or no.
          if (band) return v === 0 ? t('ui.no') : v === 1 ? t('ui.yes') : `${num(v * 100)} %`;
          return `${num(v, 1)} ${s.unit}`;
        },
      })),
    ],
  };

  const existing = plots.get(chart.id);
  if (existing) existing.destroy();
  box.textContent = '';
  plots.set(chart.id, new uPlot(opts, data, box));
}

function resize() {
  for (const [id, plot] of plots) {
    const box = el(id);
    const chart = CHARTS.find((c) => c.id === id);
    if (box.clientWidth) plot.setSize({ width: box.clientWidth, height: (chart && chart.height) || 240 });
  }
}

// --- wiring ----------------------------------------------------------------

function buildRanges() {
  const box = el('ranges');
  for (const r of RANGES) {
    const b = document.createElement('button');
    b.className = 'range';
    b.type = 'button';
    b.textContent = r.label;
    b.dataset.hours = String(r.hours);
    b.setAttribute('aria-pressed', String(r.hours === hours));
    b.addEventListener('click', () => {
      hours = r.hours;
      for (const other of document.querySelectorAll('.range')) {
        other.setAttribute('aria-pressed', String(Number(other.dataset.hours) === hours));
      }
      refresh();
    });
    box.append(b);
  }
}

function refresh() {
  renderDownload();
  load().catch((err) => banner(t('ui.data.failed', err.message)));
}

el('menu-button').addEventListener('click', () => {
  const menu = el('menu');
  menu.hidden = !menu.hidden;
  el('menu-button').setAttribute('aria-expanded', String(!menu.hidden));
});

buildRanges();
refresh();

// Dark mode is a selected palette rather than an inversion, so the charts are
// rebuilt when the system flips rather than having their colours nudged.
matchMedia('(prefers-color-scheme: dark)').addEventListener('change', refresh);
// And when the choice is made in the header rather than by the system.
addEventListener('themechange', refresh);

let resizeTimer = null;
addEventListener('resize', () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(resize, 120);
});

// Fresh data while the page is open, but only at the coarsest useful rate: the
// unit refreshes its measurements every ten seconds and no chart here is
// narrow enough for that to show.
setInterval(() => {
  // Not while somebody is in a field: this redraws the controls with the
  // charts, and a tick that lands mid-sentence takes what was typed with it.
  if (document.hidden || busyTyping()) return;
  refresh();
}, 60000);
