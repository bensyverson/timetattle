# Dashboard image round: three register prompts

2026-08-10. Part of the web dashboard design thread on task p8fuu. The interaction
model is settled there (one continuous timeline, semantic zoom, the additive
protocol of 2026-08-09); what is not settled is the visual register. This document
is the image round's input.

## Process

Decided with Ben on 2026-08-10: rather than converging on a visual design in
prose, we draft one self-contained prompt per candidate register, Ben runs them
on an external diffusion image platform, and brings the results back for joint
review. Each prompt establishes the style, describes the app and its data, and
names two or three key moments to visualize. Run a prompt as-is for a composite
frame, or keep only one "Show:" clause for a focused frame.

Three registers made the cut:

1. **Vignelli annual report** — the working year as a designed print document.
   Sharpened from generic "Swiss" to Vignelli specifically, with pages from *The
   Vignelli Canon* as reference.
2. **Observatory almanac** — time as a phenomenon under instrument observation.
3. **Strip-chart recorder** — the physical trace of a machine that was, or was
   not, recording.

Dropped: **contemporary product** (soft SaaS idiom) — it is the register in which
absence of a mark *means* zero, the exact reading this design exists to prevent.
Held in reserve: **terminal heritage** — continuity with the CLI dashboard; if
the observatory register comes back too cold it can hybridize (dark instrument
ground, monospace numerals).

## What every register must answer

These are the standing demands from the interaction model, restated as visual
questions. When judging generations, look for a register's *native* answer to
each:

- **Four states, four renderings.** Worked, observed-idle, nothing-recorded, and
  overworked must all be distinguishable. Absence of a mark is never the
  rendering for absence of data: unrecorded time gets a texture (hatching or its
  register equivalent), while a day observed with no work stays plain ground.
- **The overwork register** (>8h pushing into saturation/darkness) must stay
  perceptually distinct from both hatching and observed-zero.
- **A durable anchor** — the intensity heat band — persists at every zoom
  altitude.
- **Blocks are conclusions, not assertions.** Work blocks carry evidence ticks,
  and at depth, markers with lead-in/lead-out tails that expose the derivation.

## Type scale note (Vignelli register)

Ben, 2026-08-10, refining the Canon's "two sizes at the most": the system needs
more than two sizes, but keeps the Canon's discipline of *weight for function* —
bold versus regular differentiates heading from body, not size alone. The
working ladder: **tiny** (labels, axis numerals; smaller than body but not half),
**body**, **heading** (bold, roughly 1.5–2x — the Canon's 2x read as an upper
bound, not a target), and possibly **huge** — the dramatic scale contrast.

Open question, deliberately left to the generations: should the system be
type-driven, or should the visualizations be the hero? The hypothesis encoded in
the prompt below: visualization is the hero, and the **huge size is reserved for
key data numerals** — a period's hours stated large is simultaneously Vignelli's
scale drama and data, whereas an editorial headline in a private instrument has
nothing to say. Judge from the frames whether the huge numeral earns its place
or fights the lanes.

Typeface (Ben, same session): **Univers over Helvetica** if we can get it — a
deliberate deviation from Vignelli's own Helvetica devotion, and a good one for
an instrument: Frutiger's numerals stay differentiated at label sizes. Web
reality, settled later: Univers is not on Google Fonts (commercial
Linotype/Monotype); it is on Adobe Fonts, and a localhost-only app can self-host
a licensed copy. Nearest free stand-ins if ever needed: Archivo or Roboto.

## 1. Vignelli annual report

What to judge in the returns: whether the red survives as *signal* (overwork
only) or decays into decoration; whether the ruler hierarchy structures the
lanes without caging them; whether the huge numeral earns its place (the type
question above); how crosshatch sits on warm white at data density.

```text
A web dashboard for a personal time-intelligence tool, art-directed like a
Massimo Vignelli print program — the Vignelli Canon applied to a screen. Warm
white paper ground. Univers with weight doing the work: bold headings,
regular body, small labels, and one very large size reserved for key numerals,
with generous white space — white space as the silence between elements. Bold
black horizontal rulers in two weights structure the page: heavy rules separate
major sections, hairline rules separate rows, and type hangs from its ruler.
Strict modular grid, flat ink color, no gradients, no shadows, no rounded
corners; a single red accent used sparingly and only as signal.

The visualization is the hero: horizontal timeline swimlanes, one per client.
Each day is a flat rectangle whose ink density encodes hours worked; days
beyond eight hours push into an over-saturated warning red; spans where nothing
was recorded are filled with fine diagonal crosshatch, never left blank; a day
observed with no work stays plain paper. Within days, solid work blocks carry
small evidence tick marks (coding sessions, commits, app focus). A thin summary
heat band runs above the lanes, a narrow client list sits at left, small
numerals mark the month axis, and a single huge Univers figure states the
period's hours.

Show: (1) the year spread — five client lanes across fourteen months, one
three-day burst standing out, one crosshatched unobserved week; (2) a three-day
close-up — work blocks over their day-color ground, tick marks visible, one
crosshatched afternoon gap; (3) one day fully opened — event markers with
lead-in and lead-out tails exposing how a work block was derived, an hour ruler
beneath.
```

## 2. Observatory almanac

What to judge: whether it stays *calm* — the risk is drifting into sci-fi HUD or
neon dataviz; whether hairlines hold up at real data density; whether pale gold
and cyan carry the intensity ramp without a third hue; how hatching reads on a
dark ground (it must not look like a rendering artifact).

```text
A web dashboard for a personal time-intelligence tool, art-directed like an
astronomical observatory's almanac — a quiet instrument chart, not a consumer
app. Near-black charcoal ground; fine hairline traces and tick marks in pale
gold and desaturated cyan; small precise numerals and annotations; archival
scientific-engraving influence; chronograph-like horizontal bands;
instrument-panel calm; matte surfaces, no gloss, no neon, no lens flare.

The tool treats one person's working time as a phenomenon under observation,
reconstructed from machine evidence. Horizontal observation bands, one per
client, run across the frame. Each day is a segment whose brightness encodes
hours worked; days beyond eight hours flare past a printed limit line into a
hotter tone; intervals where the instruments recorded nothing are hatched "no
observation" bands, never empty ground — a day observed idle stays dark ground,
which is a different thing. Within days, derived work blocks carry small tick
marks for the individual observations (coding sessions, commits, app focus)
that produced them. A thin summary trace persists at the top like a reference
line; a narrow list of subjects sits at left; the time axis reads at exactly
one scale, months, in small numerals.

Show: (1) the year chart — five observation bands across fourteen months, one
three-day burst standing out, one hatched unobserved week; (2) a three-day
close-up — blocks above their day ground with observation ticks; (3) one day at
full magnification — markers with lead-in and lead-out tails like error bars,
exposing the derivation, an hour ruler beneath.
```

## 3. Strip-chart recorder

This register is a metaphor donor more than a literal skin: the generations
will look like photographed paper, and the job is to harvest its answers —
above all how it renders *the recorder being off* as something physical (torn
or missing paper), which is the strongest available visual for
nothing-was-recorded. The translation risk is skeuomorphism; judge texture and
absence-rendering, not chrome.

```text
A long horizontal strip-chart recording of one person's working life, as if a
laboratory seismograph had been wired to their computers — instrument output,
not a screen interface. A continuous paper strip with ruled feed lines and
subtle grain, photographed flat under even light. Continuous pen traces in dark
ink; a printed eight-hour limit line, with red pen excursions where a day runs
past it; regions of torn or missing paper where the recorder was off — the
absence is physical, and different from a calm flat trace, which means presence
without engagement; small inked event pips along the traces (coding sessions,
commits, app focus); marginal annotations in pencil; stamped dates along the
feed edge.

The recording covers several parallel channels, one per client, stacked on the
same strip. Dense ink means heavy work; a flat trace means observed idle; torn
paper means nothing was recorded.

Show: (1) a wall of stacked year-long strips, one channel per client, one
three-day burst of dense ink, one torn-out week; (2) a close segment spanning
three days — pen-trace blocks with event pips, one afternoon of missing paper;
(3) a single day's segment under magnification — each pen stroke's lead-in and
run-out visible, pencil marks reconstructing how the work block was inferred,
an hour rule printed beneath.
```

## Generation notes

- Generate wide — 16:9 minimum, 21:9 better; the artifact is a horizontal
  timeline.
- Expect the text and numerals in generations to be gibberish. The shopping
  list is material, palette, line weight, texture, and density — not
  correctness.
- Keep style consistent within a register (same seed or style reference) so the
  comparison is between registers, not between renderings.
- Bring back 3–5 frames per register, including the failures — what a register
  does when it goes wrong is as informative as its best frame.
