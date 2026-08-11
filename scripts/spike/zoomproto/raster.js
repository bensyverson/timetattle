/*
  raster.js — software-rasterises the prototype's canvas calls to PNGs, so the
  layout can be LOOKED AT without a browser. Run it with:

      node scripts/spike/zoomproto/raster.js [z ...]
      OUT_DIR=/tmp CENTER_DAY=212 CENTER_HOUR=10 node scripts/spike/zoomproto/raster.js 0.55 0.92

  Approximate on purpose — gradients interpolate per column, patterns draw as
  plain diagonals, paths fill as their bounding box, text draws as a rule — so
  do not judge the LOOK of anything here. It exists to catch GEOMETRY, and it
  has earned its place twice: it is how the duration ticks were caught hanging
  out of the bottom of their block, and how a focused lane at Level 3 was found
  to be a 400px empty box with four small marks adrift in the middle of it.
  Neither is visible in any assertion, and neither would have been found before
  Ben opened the file.
*/
'use strict';
const fs = require('fs');
const zlib = require('zlib');
const PROTO = require('path').join(__dirname, 'index.html');
const W = 1240, H = 620;

function parseColour(s) {
  if (!s || typeof s !== 'object' && typeof s !== 'string') return null;
  if (typeof s === 'object') return s;
  s = s.trim();
  let m;
  if ((m = s.match(/^#([0-9a-f]{6})$/i))) {
    const v = parseInt(m[1], 16);
    return [v >> 16 & 255, v >> 8 & 255, v & 255, 1];
  }
  if ((m = s.match(/^hsl\(\s*([\d.]+)\s+([\d.]+)%\s+([\d.]+)%\s*\)$/))) return hsl(+m[1], +m[2] / 100, +m[3] / 100, 1);
  if ((m = s.match(/^rgba?\(([^)]+)\)$/))) {
    const p = m[1].split(',').map(Number);
    return [p[0], p[1], p[2], p.length > 3 ? p[3] : 1];
  }
  return [255, 0, 255, 1];
}
function hsl(h, s, l, a) {
  const c = (1 - Math.abs(2 * l - 1)) * s, x = c * (1 - Math.abs((h / 60) % 2 - 1)), m = l - c / 2;
  let r, g, b;
  if (h < 60) [r, g, b] = [c, x, 0]; else if (h < 120) [r, g, b] = [x, c, 0];
  else if (h < 180) [r, g, b] = [0, c, x]; else if (h < 240) [r, g, b] = [0, x, c];
  else if (h < 300) [r, g, b] = [x, 0, c]; else [r, g, b] = [c, 0, x];
  return [Math.round((r + m) * 255), Math.round((g + m) * 255), Math.round((b + m) * 255), a];
}

function makeSurface(w, h) {
  const buf = new Uint8Array(w * h * 3);
  return {
    w, h, buf,
    px(x, y, c, alpha) {
      x |= 0; y |= 0;
      if (x < 0 || y < 0 || x >= w || y >= h) return;
      const a = alpha * (c[3] === undefined ? 1 : c[3]);
      if (a <= 0) return;
      const i = (y * w + x) * 3;
      buf[i] = buf[i] * (1 - a) + c[0] * a;
      buf[i + 1] = buf[i + 1] * (1 - a) + c[1] * a;
      buf[i + 2] = buf[i + 2] * (1 - a) + c[2] * a;
    },
  };
}

function makeCtx(surf) {
  let path = null;
  const c = {
    globalAlpha: 1, fillStyle: '#000', strokeStyle: '#000', lineWidth: 1, font: '', textAlign: 'left',
    clip_: null,
    _rect(x, y, w, h, style) {
      if (!isFinite(x) || !isFinite(y) || !isFinite(w) || !isFinite(h)) return;
      const cl = c.clip_;
      const x0 = Math.max(Math.round(x), cl ? cl[0] : 0), x1 = Math.min(Math.round(x + w), cl ? cl[2] : surf.w);
      const y0 = Math.max(Math.round(y), cl ? cl[1] : 0), y1 = Math.min(Math.round(y + h), cl ? cl[3] : surf.h);
      for (let yy = y0; yy < y1; yy++) for (let xx = x0; xx < x1; xx++) {
        let col;
        if (style && style.__grad) {
          const t = Math.max(0, Math.min(1, (xx - style.x0) / Math.max(0.001, style.x1 - style.x0)));
          const s0 = style.stops[0], s1 = style.stops[style.stops.length - 1];
          col = [s0.c[0] + (s1.c[0] - s0.c[0]) * t, s0.c[1] + (s1.c[1] - s0.c[1]) * t, s0.c[2] + (s1.c[2] - s0.c[2]) * t, 1];
        } else if (style && style.__pattern) {
          col = ((xx + yy) % style.pitch < 1) ? [105, 115, 127, 1] : [24, 29, 37, 1];
        } else col = style || [255, 0, 255, 1];
        surf.px(xx, yy, col, c.globalAlpha);
      }
    },
    clearRect(x, y, w, h) { c._rect(x, y, w, h, [0, 0, 0, 1]); },
    fillRect(x, y, w, h) { c._rect(x, y, w, h, parseStyle(c.fillStyle)); },
    strokeRect(x, y, w, h) {
      const s = parseStyle(c.strokeStyle);
      c._rect(x, y, w, 1, s); c._rect(x, y + h - 1, w, 1, s);
      c._rect(x, y, 1, h, s); c._rect(x + w - 1, y, 1, h, s);
    },
    beginPath() { path = { minX: Infinity, minY: Infinity, maxX: -Infinity, maxY: -Infinity }; },
    _pt(x, y) { if (!path) c.beginPath(); path.minX = Math.min(path.minX, x); path.maxX = Math.max(path.maxX, x); path.minY = Math.min(path.minY, y); path.maxY = Math.max(path.maxY, y); },
    moveTo(x, y) { c._pt(x, y); }, lineTo(x, y) { c._pt(x, y); },
    arcTo(x1, y1, x2, y2) { c._pt(x1, y1); c._pt(x2, y2); },
    rect(x, y, w, h) { c._pt(x, y); c._pt(x + w, y + h); },
    closePath() {}, setLineDash() {}, scale() {}, setTransform() {}, translate() {},
    fill() { if (path && isFinite(path.minX)) c._rect(path.minX, path.minY, path.maxX - path.minX, path.maxY - path.minY, parseStyle(c.fillStyle)); },
    stroke() {
      if (!path || !isFinite(path.minX)) return;
      const s = parseStyle(c.strokeStyle);
      const w = path.maxX - path.minX, h = path.maxY - path.minY;
      if (w < 1.5) c._rect(path.minX, path.minY, 1, h, s);
      else if (h < 1.5) c._rect(path.minX, path.minY, w, 1, s);
      else { c._rect(path.minX, path.minY, w, 1, s); c._rect(path.minX, path.maxY - 1, w, 1, s); c._rect(path.minX, path.minY, 1, h, s); c._rect(path.maxX - 1, path.minY, 1, h, s); }
    },
    clip() { if (path && isFinite(path.minX)) c.clip_ = [Math.round(path.minX), Math.round(path.minY), Math.round(path.maxX), Math.round(path.maxY)]; },
    save() { c._stack = c._stack || []; c._stack.push(c.clip_); },
    restore() { if (c._stack && c._stack.length) c.clip_ = c._stack.pop(); },
    fillText(t, x, y) {
      // draw text as a faint underline so labels are locatable
      const s = parseStyle(c.fillStyle);
      const a = c.globalAlpha; c.globalAlpha = a * 0.55;
      c._rect(c.textAlign === 'right' ? x - String(t).length * 5.5 : x, y + 1, String(t).length * 5.5, 1, s);
      c.globalAlpha = a;
    },
    createLinearGradient(x0, y0, x1) { return { __grad: true, x0, x1, stops: [], addColorStop(p, col) { this.stops.push({ p, c: parseColour(col) }); } }; },
    createPattern() { return { __pattern: true, pitch: 5 }; },
    measureText: (t) => ({ width: String(t).length * 6 }),
  };
  function parseStyle(s) { return (s && s.__grad) || (s && s.__pattern) ? s : parseColour(s); }
  return c;
}

function png(surf) {
  const raw = Buffer.alloc((surf.w * 3 + 1) * surf.h);
  let o = 0;
  for (let y = 0; y < surf.h; y++) { raw[o++] = 0; for (let x = 0; x < surf.w * 3; x++) raw[o++] = surf.buf[y * surf.w * 3 + x]; }
  const chunk = (type, data) => {
    const len = Buffer.alloc(4); len.writeUInt32BE(data.length);
    const td = Buffer.concat([Buffer.from(type), data]);
    const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(td) >>> 0);
    return Buffer.concat([len, td, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(surf.w, 0); ihdr.writeUInt32BE(surf.h, 4);
  ihdr[8] = 8; ihdr[9] = 2; ihdr[10] = 0; ihdr[11] = 0; ihdr[12] = 0;
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk('IHDR', ihdr), chunk('IDAT', zlib.deflateSync(raw)), chunk('IEND', Buffer.alloc(0))]);
}
let crcTable = null;
function crc32(buf) {
  if (!crcTable) { crcTable = []; for (let n = 0; n < 256; n++) { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; crcTable[n] = c; } }
  let c = 0xffffffff;
  for (let i = 0; i < buf.length; i++) c = crcTable[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  return c ^ 0xffffffff;
}

// ---- boot the prototype against the raster surface ----
const html = fs.readFileSync(PROTO, 'utf8');
const script = html.match(/<script>\n([\s\S]*)\n<\/script>/)[1];
const surf = makeSurface(W, H);
const mainCtx = makeCtx(surf);
const schedSurf = makeSurface(W, 92);
const schedCtx = makeCtx(schedSurf);

function el(tag, ctxFor) {
  const e = {
    tagName: (tag || 'div').toUpperCase(), style: {}, children: [], classList: { add() {}, remove() {}, toggle() {} },
    width: 0, height: 0, value: '', textContent: '', innerHTML: '', _handlers: {},
    getContext: () => ctxFor || makeCtx(makeSurface(64, 64)),
    getBoundingClientRect: () => ({ width: W, height: e.__h || H, left: 0, top: 0 }),
    addEventListener(k, f) { this._handlers[k] = f; },
    appendChild(c) { this.children.push(c); return c; },
    append(...cs) { this.children.push(...cs); },
    setPointerCapture() {}, select() {},
    get firstChild() { return this.children[0] || el('i'); },
  };
  return e;
}
const byId = {};
byId.view = el('canvas', mainCtx);
byId.schedule = el('canvas', schedCtx); byId.schedule.__h = 92;
byId.stage = el('div');
const g = {
  document: { createElement: (t) => el(t), createTextNode: (t) => ({ text: t }), getElementById: (id) => (byId[id] || (byId[id] = el('div'))), addEventListener() {} },
  window: { devicePixelRatio: 1, addEventListener() {} },
  navigator: {}, localStorage: { getItem: () => null, setItem() {} },
  requestAnimationFrame: (f) => { g.__pending = f; },
  ResizeObserver: class { observe() {} },
  console, Math, Date, JSON, Object, Array, String, Number, Float64Array, Int32Array, Uint8Array, Map, Set, Infinity, parseFloat, isFinite,
};
const out = {};
new Function('__g', '__out', 'with (__g) { "use strict";' + script + '\n; Object.assign(__out, { setZoom, draw, view, dayT, focus: (i) => { focusLane = i; } }); }')(g, out);
if (g.__pending) g.__pending();

const shots = process.argv.slice(2).map(Number);
for (const z of (shots.length ? shots : [0, 0.15, 0.4, 0.55, 0.75, 0.92])) {
  surf.buf.fill(0);
  out.setZoom(z, null);
  if (z > 0.7) out.focus(1);
  if (process.env.CENTER_DAY) {
    const d = +process.env.CENTER_DAY, h = +(process.env.CENTER_HOUR || 10);
    out.view.t0 = out.dayT[d] + (h - 5) * 3600000 - out.view.span / 2;
  }
  out.draw();
  const name = require('path').join(process.env.OUT_DIR || require('os').tmpdir(), 'zoomproto-z' + String(z).replace('.', '_') + '.png');
  fs.writeFileSync(name, png(surf));
  console.log('wrote', name, 'span', (out.view.span / 86400000).toFixed(2) + 'd');
}
