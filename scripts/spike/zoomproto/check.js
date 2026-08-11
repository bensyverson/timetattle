/*
  check.js — headless checks over the half of the zoom prototype that is pure
  computation: the synthetic generator, the Level 0 kernel, and the intensity
  ramp. Run it with:

      node scripts/spike/zoomproto/check.js

  It does NOT re-implement anything. It reads index.html, slices out sections
  §1–§5 (everything up to the first line that touches the DOM) and runs the
  prototype's own functions, so a figure printed here is a figure the browser
  will draw. A second implementation of the kernel would be a second answer to
  "what does this band say", which is the failure mode this repository keeps
  writing down; there is exactly one implementation and it lives in the HTML.

  Every number quoted in the prototype's source comments and in the task note
  comes from this script. Re-run it after touching the generator or the kernel.
*/
'use strict';
const fs = require('fs');
const path = require('path');

const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
const script = html.match(/<script>\n([\s\S]*)\n<\/script>/)[1];
const marker = script.indexOf('§6  RENDER');
if (marker < 0) throw new Error('could not find §6 in index.html — has the section layout changed?');
const core = script.slice(0, script.lastIndexOf('/* ====', marker));

const ctxObj = {};
// eslint-disable-next-line no-new-func
new Function('with (this) { ' + core + '\n; Object.assign(__out, {LANES,SOURCES,N_DAYS,DAY,HOUR,MIN,T,DEFAULTS,LAYERS,observed,gaps,hours,blocks,ticksOf,view,recomputeRamps,kernelWidthDays,kernelAt,spanOf,zOfDays,intensityColour,DOMAIN_T0,DOMAIN_T1,axisAlphas,AXIS_SCALES}); }')
  .call({ __out: ctxObj, document: undefined, window: undefined });
const {
  LANES, N_DAYS, DAY, HOUR, T, DEFAULTS, observed, gaps, hours, blocks, ticksOf,
  view, recomputeRamps, kernelWidthDays, kernelAt, spanOf, zOfDays, intensityColour,
  DOMAIN_T0, DOMAIN_T1,
} = ctxObj;

let failures = 0;
function assert(ok, label, detail) {
  console.log((ok ? '  ok   ' : '  FAIL ') + label + (detail ? '  — ' + detail : ''));
  if (!ok) failures++;
}
function head(t) { console.log('\n' + t); }

/* ---- 1. the synthetic corpus has the shapes feel-tuning needs -------- */
head('SHAPE OF THE SYNTHETIC CORPUS');
let nBlocks = 0, contested = 0, obsZero = 0, worked = 0, over10 = 0, over12 = 0;
let unobsDays = 0, gapDays = 0, maxDay = 0, totalH = 0;
for (let l = 0; l < LANES.length; l++) {
  nBlocks += blocks[l].length;
  for (const b of blocks[l]) if (b.contestedIn || b.contestedOut) contested++;
}
for (let d = 0; d < N_DAYS; d++) { if (!observed[d]) unobsDays++; if (gaps[d].length) gapDays++; }
for (let l = 0; l < LANES.length; l++) for (let d = 0; d < N_DAYS; d++) {
  const h = hours[l][d];
  totalH += h;
  maxDay = Math.max(maxDay, h);
  if (observed[d]) { if (h === 0) obsZero++; else { worked++; if (h >= 10) over10++; if (h >= 12) over12++; } }
}
console.log(`  ${LANES.length} lanes × ${N_DAYS} days · ${nBlocks} blocks · ${totalH.toFixed(0)}h total`);
console.log(`  lane-days: ${worked} worked, ${obsZero} observed-zero · ${over10} at 10h+, ${over12} at 12h+, peak ${maxDay.toFixed(2)}h`);
console.log(`  absence: ${unobsDays} unobserved days, ${gapDays} days carrying a within-day gap`);
console.log(`  disagreement: ${contested} blocks with a contested extent`);
assert(over12 >= 20, 'the overwork register has something in it', `${over12} lane-days at 12h+`);
assert(obsZero > 100 && unobsDays > 10, 'observed-zero and unobserved are both well populated');
assert(contested > 20, 'source disagreement is present');

/* ---- 2. the two invariants the generator must hold ------------------- */
head('GENERATOR INVARIANTS');
let worstDelta = 0;
for (let l = 0; l < LANES.length; l++) {
  const per = new Float64Array(N_DAYS);
  for (const b of blocks[l]) per[b.day] += (b.t1 - b.t0) / HOUR;
  for (let d = 0; d < N_DAYS; d++) worstDelta = Math.max(worstDelta, Math.abs(per[d] - hours[l][d]));
}
assert(worstDelta < 1e-9, 'the band is the sum of its blocks', `max |day − Σblocks| = ${worstDelta.toExponential(2)}h`);

let asserted = 0;
for (let l = 0; l < LANES.length; l++) for (const b of blocks[l]) {
  if (!observed[b.day]) asserted++;
  for (const g of gaps[b.day]) if (b.t0 < g.t1 && b.t1 > g.t0) asserted++;
}
assert(asserted === 0, 'no block is asserted where nothing was recorded', `${asserted} violations`);

/* ---- 3. the Level 0 kernel ------------------------------------------- */
head('LEVEL 0 KERNEL');
const NC = 900;
function sample(fn, z) {
  view.z = z; recomputeRamps();
  const w = kernelWidthDays(), out = new Float64Array(NC);
  for (let x = 0; x < NC; x++) out[x] = fn(0, (x / NC) * N_DAYS, w);
  return out;
}
const sliding = (l, dc, w) => { const v = kernelAt(l, dc, w); return v ? v.hours : 0; };

/* The rejected design, kept only so the claim "hard bins pop" stays a
   measurement. Bin k covers days [k·w, (k+1)·w); a day's weight saturates
   once half of it is inside, which was the most favourable variant tried. */
function hardBin(li, dc, w) {
  const H = hours[li];
  const k = Math.floor(dc / w), d0 = k * w, d1 = (k + 1) * w;
  let acc = 0;
  for (let d = Math.max(0, Math.floor(d0)); d < Math.min(N_DAYS, Math.ceil(d1)); d++) {
    const ov = Math.min(d + 1, d1) - Math.max(d, d0);
    if (ov <= 0 || !observed[d]) continue;
    acc = Math.max(acc, H[d] * Math.min(1, ov / 0.5));
  }
  return acc;
}
function motion(fn) {
  let worstCol = 0, meanAbs = 0, prev = null;
  for (let z = 0; z <= 0.2001; z += 0.002) {
    const s = sample(fn, z);
    if (prev) {
      let l1 = 0;
      for (let x = 0; x < NC; x++) { const d = Math.abs(s[x] - prev[x]); l1 += d; worstCol = Math.max(worstCol, d); }
      meanAbs = Math.max(meanAbs, l1 / NC);
    }
    prev = s;
  }
  return { worstCol, meanAbs };
}
T.kernelStat = 'max';
const mSlide = motion(sliding), mHard = motion(hardBin);
console.log(`  per zoom step of 0.002, across the whole easing window:`);
console.log(`    sliding kernel (shipped)  worst column ${mSlide.worstCol.toFixed(2)}h · mean |Δ| ${mSlide.meanAbs.toFixed(4)}h`);
console.log(`    hard bins      (rejected) worst column ${mHard.worstCol.toFixed(2)}h · mean |Δ| ${mHard.meanAbs.toFixed(4)}h`);
assert(mSlide.meanAbs * 3 < mHard.meanAbs, 'the sliding kernel moves far less of the picture per step',
  `${(mHard.meanAbs / mSlide.meanAbs).toFixed(1)}× less`);

view.z = 0; recomputeRamps();
function bandStat(stat, thr) {
  T.kernelStat = stat;
  const w = kernelWidthDays();
  let peak = 0, n = 0;
  for (let x = 0; x < 4000; x++) {
    const v = kernelAt(0, (x / 4000) * N_DAYS, w);
    if (!v) continue;
    peak = Math.max(peak, v.hours);
    if (v.hours >= thr) n++;
  }
  return { peak, frac: n / 4000 };
}
const bMax = bandStat('max', 10), bMean = bandStat('mean', 10);
T.kernelStat = 'max';
console.log(`  at week width, lane 0: max reads peak ${bMax.peak.toFixed(2)}h and ${(bMax.frac * 100).toFixed(1)}% of the band at 10h+`);
console.log(`                          mean reads peak ${bMean.peak.toFixed(2)}h and ${(bMean.frac * 100).toFixed(1)}% at 10h+`);
assert(bMean.frac === 0 && bMax.frac > 0.1, 'the forbidden mean genuinely erases the bursts max keeps');

/* Max is burst-preserving but it also DILATES: every column within a week of a
   14h day reads 14h, so the year-altitude band paints hot almost everywhere.
   The power mean is the dial between the two poles the design named. */
console.log('  how much of the band paints as overwork (>= the anchor) at week width:');
for (const cfg of [['mean', 1], ['power', 2], ['power', 3], ['power', 4], ['power', 6], ['max', 0]]) {
  T.kernelStat = cfg[0]; T.kernelPower = cfg[1];
  const b = bandStat(cfg[0], T.anchorHours);
  console.log(`    ${(cfg[0] === 'power' ? 'power p=' + cfg[1] : cfg[0]).padEnd(11)} peak ${b.peak.toFixed(2).padStart(5)}h · ${(b.frac * 100).toFixed(1).padStart(5)}% of the band at ${T.anchorHours}h+`);
}
T.kernelStat = 'max'; T.kernelPower = 4;

/* ---- 4. invariant 5, checked on the colours themselves --------------- */
head('INVARIANT 5 — the three absences must not converge');
function L(css) { return parseFloat(css.match(/([\d.]+)%\)/)[1]) / 100; }
function relLum(l) { return l; }  // hsl lightness is a good enough proxy here
const lZero = L(intensityColour(0));
const lOver = L(intensityColour(T.overworkTopHours));
const lAnchor = L(intensityColour(T.anchorHours));
const lHatchGround = 0.121;  // #181d25
console.log(`  observed-zero L=${lZero.toFixed(3)} · anchor(${T.anchorHours}h) L=${lAnchor.toFixed(3)} · overwork(${T.overworkTopHours}h) L=${lOver.toFixed(3)} · hatch ground L=${lHatchGround}`);
assert(lOver - lZero > 0.25, 'a 14h day cannot be mistaken for an idle one',
  `ΔL = ${(lOver - lZero).toFixed(3)}`);
assert(Math.abs(lZero - lHatchGround) < 0.08, 'the hatch is told apart by TEXTURE, not by a value the ramp also uses');
assert(lAnchor > lOver, 'the overwork register darkens past the anchor, as specified');

/* ---- 4b. the axis: at rest far more than in motion ------------------- */
head('TIME AXIS — the sanctioned crossfade');
{
  const { axisAlphas, AXIS_SCALES } = ctxObj;
  let inMotion = 0, steps = 0, worstSum = 0, stacked = 0;
  const spans = [];
  let runScale = -1, runStart = 0;
  for (let z = 0; z <= 1.0001; z += 0.001) {
    view.z = z; steps++;
    const a = axisAlphas();
    const lit = a.filter(v => v > 0.02).length;
    if (lit > 1) inMotion++;
    if (lit > 2) stacked++;
    worstSum = Math.max(worstSum, a.reduce((p, c) => p + c, 0));
    const dominant = a.indexOf(Math.max(...a));
    if (dominant !== runScale) { if (runScale >= 0) spans.push([AXIS_SCALES[runScale], (z - runStart).toFixed(3)]); runScale = dominant; runStart = z; }
  }
  spans.push([AXIS_SCALES[runScale], (1 - runStart).toFixed(3)]);
  console.log('  each scale is dominant over: ' + spans.map(([n, w]) => `${n} ${w}`).join(' · '));
  console.log(`  in crossfade at ${(100 * inMotion / steps).toFixed(1)}% of zoom positions; three scales lit at once at ${(100 * stacked / steps).toFixed(1)}%`);
  assert(inMotion / steps < 0.2, 'the axis is at rest far more than it is in motion', `${(100 * inMotion / steps).toFixed(1)}% in motion`);
  assert(stacked === 0, 'never more than two scales are lit at once');
  assert(worstSum < 1.05, 'the crossfade conserves ink — no double-drawn axis', `peak total alpha ${worstSum.toFixed(3)}`);

  // and what the design's own rule would do without the absolute floor
  const savedFloor = T.axisFadeMinZ;
  T.axisFadeMinZ = 0;
  let narrowest = 1;
  const b = [zOfDays(T.axisYearsToMonths), zOfDays(T.axisMonthsToDays), zOfDays(T.axisDaysToHours)];
  const edges = [0, b[0], b[1], b[2], 1];
  for (let i = 0; i < 3; i++) narrowest = Math.min(narrowest, T.axisFade * Math.min(edges[i + 1] - edges[i], edges[i + 2] - edges[i + 1]));
  T.axisFadeMinZ = savedFloor;
  console.log(`  fraction-of-segment alone would make the narrowest crossfade ${narrowest.toFixed(4)} of zoom (floor in use: ${savedFloor})`);
  assert(narrowest < savedFloor, 'the absolute floor is doing real work on a corpus this short');
}

/* ---- 5. what the shipped windows mean in spans ----------------------- */
head('SHIPPED LOD SCHEDULE');
for (const k of Object.keys(DEFAULTS.layers)) {
  const w = DEFAULTS.layers[k];
  console.log(`  ${k.padEnd(8)} arrives z=${w.zIn.toFixed(3)} (${(spanOf(w.zIn) / DAY).toFixed(2)}d in frame) → full z=${w.zFull.toFixed(3)} (${(spanOf(w.zFull) / DAY).toFixed(2)}d)`);
}
console.log(`  axis   years→months z=${zOfDays(T.axisYearsToMonths).toFixed(4)} · months→days z=${zOfDays(T.axisMonthsToDays).toFixed(4)} · days→hours z=${zOfDays(T.axisDaysToHours).toFixed(4)}`);

/* ---- 6. worst-case frame ------------------------------------------- */
head('WORST-CASE FRAME');
let mostBlocks = 0, span = spanOf(DEFAULTS.layers.blocks.zFull);
for (let t = DOMAIN_T0; t < DOMAIN_T1 - span; t += 2 * DAY) {
  let n = 0;
  for (let l = 0; l < LANES.length; l++) for (const b of blocks[l]) if (b.t1 > t && b.t0 < t + span) n++;
  mostBlocks = Math.max(mostBlocks, n);
}
let ticks = 0;
for (const b of blocks[0].slice(0, 300)) { const tk = ticksOf(b); ticks += tk.focus.length + tk.transcripts.length + tk.git.length + 1; }
console.log(`  at blocks-fully-in (${(span / DAY).toFixed(2)}d span): ${mostBlocks} blocks on screen, ~${(ticks / 300).toFixed(1)} ticks each → ~${Math.round(mostBlocks * ticks / 300)} marks`);
assert(mostBlocks * (ticks / 300) < 4000, 'a Level 2 frame stays well inside a few thousand marks');

console.log('\n' + (failures ? failures + ' CHECK(S) FAILED' : 'all checks passed'));
process.exit(failures ? 1 : 0);
