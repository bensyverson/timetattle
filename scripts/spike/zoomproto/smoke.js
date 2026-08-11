/*
  smoke.js — runs the WHOLE page (render included) under a stub DOM and canvas,
  sweeps the zoom across its full range, and drives every tuning control to both
  of its extremes, asserting that no frame throws and that no draw call ever
  receives a non-finite coordinate. Run it with:

      node scripts/spike/zoomproto/smoke.js

  It is the counterpart to check.js: check.js verifies the arithmetic, this
  verifies that the pixels can be produced at all. Between them the prototype
  can be edited without a browser open, which matters because the failure mode
  of a canvas is a silently blank rectangle rather than an error.
*/
'use strict';
const fs = require('fs');
const path = require('path');
const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
const script = html.match(/<script>\n([\s\S]*)\n<\/script>/)[1];

const calls = {};
function ctxStub() {
  const c = {
    canvas: null,
    globalAlpha: 1, fillStyle: '', strokeStyle: '', lineWidth: 1, font: '', textAlign: 'left',
  };
  const noop = (name) => (...a) => {
    calls[name] = (calls[name] || 0) + 1;
    for (const v of a) if (typeof v === 'number' && !isFinite(v)) throw new Error(name + ' got non-finite arg: ' + a.join(','));
  };
  for (const m of ['clearRect', 'fillRect', 'strokeRect', 'beginPath', 'moveTo', 'lineTo', 'closePath', 'arcTo',
    'rect', 'clip', 'save', 'restore', 'fill', 'stroke', 'setLineDash', 'fillText', 'scale', 'setTransform',
    'translate', 'putImageData']) c[m] = noop(m);
  c.createLinearGradient = (...a) => { noop('createLinearGradient')(...a); return { addColorStop() {} }; };
  c.createPattern = () => ({ __pattern: true });
  c.measureText = () => ({ width: 10 });
  return c;
}
function el(tag) {
  const e = {
    tagName: (tag || 'div').toUpperCase(), style: {}, children: [], classList: { add() {}, remove() {}, toggle() {} },
    width: 0, height: 0, value: '', textContent: '', innerHTML: '', type: '', min: '', max: '', step: '', checked: false,
    _handlers: {},
    getContext: () => ctxStub(),
    getBoundingClientRect: () => ({ width: 1240, height: 620, left: 0, top: 0 }),
    addEventListener(k, f) { this._handlers[k] = f; },
    appendChild(c) { this.children.push(c); return c; },
    append(...cs) { this.children.push(...cs); },
    setPointerCapture() {}, select() {}, focus() {},
    get firstChild() { return this.children[0] || el('i'); },
  };
  return e;
}
const byId = {};
const g = {
  document: {
    createElement: el,
    createTextNode: (t) => ({ text: t }),
    getElementById: (id) => (byId[id] || (byId[id] = el('div'))),
    addEventListener() {},
  },
  window: { devicePixelRatio: 2, addEventListener() {} },
  navigator: {},
  localStorage: { getItem: () => null, setItem() {} },
  requestAnimationFrame: (f) => { g.__pending = f; },
  ResizeObserver: class { observe() {} },
  console,
  Math, Date, JSON, Object, Array, String, Number, Float64Array, Int32Array, Uint8Array, Map, Set, Infinity, parseFloat, isFinite,
};
g.globalThis = g;

const out = {};
const fn = new Function('__g', '__out', 'with (__g) { "use strict";' + script +
  '\n; Object.assign(__out, { view, setZoom, draw, requestDraw, resize, LANES, N_DAYS, T, DEFAULTS, spanOf, jumpToLevel, blockAt, updateHover, buildLegend, onTuningChanged, buildPanel, syncPanelValues, saveTuning, loadTuning, jumpTo: jumpToLevel, focusLaneSet: (i) => { focusLane = i; }, cursorSet: (x, y) => { cursor = { x, y, inside: true }; } }); }');
fn(g, out);

// the boot code queued one frame; run it, then sweep
const flush = () => { const p = g.__pending; g.__pending = null; if (p) p(); };
flush();

let frames = 0;
for (let z = 0; z <= 1.00001; z += 0.005) {
  out.setZoom(z, 200 + (z * 800));
  out.draw();
  frames++;
}
for (let i = 0; i < LANEScount(); i++) { out.focusLaneSet(i); out.setZoom(0.9, null); out.draw(); frames++; }
function LANEScount() { return out.LANES.length; }

// hover / hit-testing at several depths
out.cursorSet(600, 200);
for (const z of [0.0, 0.35, 0.55, 0.75, 0.95]) { out.setZoom(z, null); out.draw(); out.updateHover(); }

// non-default tuning: every extreme of every slider, one at a time
const extremes = {
  axisFade: [0.01, 0.6], axisFadeMinZ: [0, 0.06], kernelStat: ['max', 'mean', 'sum'],
  kernelStat2: null, kernelPower: [1, 12], kernelMaxBinDays: [1, 21], anchorHours: [2, 12], overworkTopHours: [9, 20], rampGamma: [0.4, 1.6],
  hatchPitch: [2, 14], hatchContrast: [0.1, 1], edgeSmooth: [0, 1], ribbonMinPx: [8, 60],
  laneMinPx: [12, 80], blockRadius: [0, 6], showContested: [true, false],
  axisYearsToMonths: [60, 430], axisMonthsToDays: [3, 120], axisDaysToHours: [0.2, 10],
  detailWash: [0, 1], markerRowPx: [2, 48],
};
for (const k of Object.keys(extremes)) {
  if (!extremes[k]) continue;
  for (const v of extremes[k]) {
    const save = out.T[k];
    out.T[k] = v;
    for (const z of [0, 0.05, 0.2, 0.4, 0.6, 0.8, 1]) { out.setZoom(z, null); out.draw(); frames++; }
    out.T[k] = save;
  }
}
// degenerate layer windows (zIn === zFull) must not divide by zero
for (const key of Object.keys(out.T.layers)) {
  const s = JSON.stringify(out.T.layers[key]);
  out.T.layers[key] = { zIn: 0.5, zFull: 0.5 };
  for (const z of [0.49, 0.5, 0.51]) { out.setZoom(z, null); out.draw(); frames++; }
  out.T.layers[key] = JSON.parse(s);
}

// the panel: rebuild, resync, persist, and the level presets
out.onTuningChanged();
out.buildPanel();
out.syncPanelValues();
for (const n of [0, 1, 2, 3]) { out.jumpTo(n); out.draw(); frames++; }
console.log('panel rebuild + presets ok');
console.log('frames drawn without throwing:', frames);
console.log('draw calls:', Object.keys(calls).sort().map(k => k + '=' + calls[k]).join(' '));
