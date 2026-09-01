---
name: Kraken
description: Self-hosted control panel for a personal game-server fleet — professional telemetry in an abyssal world.
colors:
  abyss-floor: "#050d14"
  abyss-mid: "#071521"
  abyss-panel: "#0a1e2c"
  abyss-void: "#02090E"
  depth-shallow: "#0d3340"
  depth-upper: "#082530"
  depth-lower: "#04141B"
  trench-upper: "#030d16"
  trench-mid: "#04121d"
  upwelling-violet: "#2a2033"
  sodium-lumen: "#ffc266"
  ember-gold: "#d99a3c"
  surface-light: "#FFD79A"
  status-ok-gold: "#d8b46a"
  spectrum-deep-teal: "#1a7a6d"
  caution-violet: "#9d8cff"
  crisis-magenta: "#ff4d9d"
  sand-primary: "#f5dfc1"
  sand-secondary: "#b6a894"
  sand-faint: "#7d7160"
typography:
  headline:
    fontFamily: "Archivo, Segoe UI, sans-serif"
    fontSize: "clamp(19px, 1.5vw, 34px)"
    fontWeight: 700
    letterSpacing: "0.04em"
  title:
    fontFamily: "Archivo, Segoe UI, sans-serif"
    fontSize: "clamp(22px, 1.9vw, 40px)"
    fontWeight: 700
    letterSpacing: "0.05em"
  body:
    fontFamily: "Archivo, Segoe UI, sans-serif"
    fontSize: "clamp(12px, 0.82vw, 16px)"
    fontWeight: 400
  data:
    fontFamily: "Spline Sans Mono, Consolas, monospace"
    fontSize: "clamp(20px, 1.7vw, 40px)"
    fontWeight: 500
  label:
    fontFamily: "Archivo, Segoe UI, sans-serif"
    fontSize: "clamp(11px, 0.72vw, 14px)"
    fontWeight: 600
    letterSpacing: "0.22em"
  rack:
    fontFamily: "Archivo, Segoe UI, sans-serif"
    fontSize: "11px"
    fontWeight: 600
    letterSpacing: "0.26em"
  wire:
    fontFamily: "Spline Sans Mono, Consolas, monospace"
    fontSize: "10px"
    fontWeight: 500
    letterSpacing: "0.08em"
rounded:
  sm: "3px"
  md: "4px"
  lg: "6px"
spacing:
  sm: "clamp(10px, 1.3vh, 20px)"
  md: "clamp(12px, 1.3vh, 20px)"
  lg: "clamp(16px, 1.6vw, 30px)"
  xl: "clamp(18px, 1.8vw, 40px)"
components:
  panel:
    backgroundColor: "{colors.abyss-panel}"
    textColor: "{colors.ice-text}"
    rounded: "{rounded.lg}"
    padding: "clamp(12px, 1.6vh, 24px) clamp(16px, 1.6vw, 30px)"
  chip-running:
    backgroundColor: "rgba(255, 194, 102, 0.09)"
    textColor: "{colors.sodium-lumen}"
    rounded: "{rounded.sm}"
    padding: "3px 10px"
  chip-stopped:
    textColor: "{colors.sand-faint}"
    rounded: "{rounded.sm}"
    padding: "3px 10px"
  button-ghost:
    textColor: "{colors.sand-secondary}"
    rounded: "{rounded.md}"
    padding: "clamp(8px, 1vh, 14px) clamp(12px, 1.2vw, 22px)"
  chip-agent-update:
    backgroundColor: "rgba(157, 140, 255, 0.05)"
    textColor: "{colors.caution-violet}"
    rounded: "{rounded.sm}"
    padding: "3px 9px"
  chip-agent-update-hover:
    backgroundColor: "rgba(157, 140, 255, 0.12)"
    textColor: "{colors.caution-violet}"
    rounded: "{rounded.sm}"
    padding: "3px 9px"
---

# Design System: Kraken

## Overview

**Creative North Star: "The Instrument Room at Depth"**

Kraken is a professional operations surface that happens to sit at the bottom of the sea. The seriousness comes first: dense, tabular, glanceable telemetry engineered for a 10ft read on screens from 1080p to 4K. The atmosphere comes only from light and depth — grounds graduate from shallow blue-green into near-black like a water column, panels are lit faintly from within, and one sodium gold is the only light source on the surface. Nothing is illustrated; no creatures, no metaphor imagery. The sea is a palette and a physics of light, never a costume.

The pane is singular: one full-bleed screen, no sidebar, no top navigation, no routes. Everything drills in through full-screen overlays and returns. The fleet itself scrolls — identity and event stream stay pinned while the deck between them grows with the estate — but the page never does. Density is a feature — wider viewports show more, not bigger.

**Key Characteristics:**
- One light source: sodium gold carries "alive"; its absence carries "stopped".
- Depth gradients instead of decoration; glow instead of ornament.
- Live data everywhere: charts breathe, numbers tick, staleness is visible.
- 10ft legibility: primary numerals are huge, labels are tracked caps, everything tabular.

## Colors

A two-family palette: abyssal blue-green grounds and a single living sodium gold, with violet and magenta held in reserve as semantics. The grounds stay cold and the light stays warm, so temperature alone separates "alive" from "everything else".

### Primary
- **Sodium Lumen** (#ffc266): the only light. Live chart lines and fills, running-state chips and dots, glowing text-shadows on primary numerals, hover accents, focus rings. It always means "alive and healthy". Alpha variants come from `--lumen-rgb`, never a hardcoded triplet.
- **Ember Gold** (#d99a3c): the bottom stop of the one gradient fill in the house, under Sodium
  Lumen on a solid button. It is never used on its own and never on text — it exists only to give
  that fill a direction to fall in, so a lit button reads as a lamp with a top rather than a flat
  swatch. It is the single exception to "alphas come from the token": a gradient stop cannot be an
  alpha of the colour it is falling away from.

### Neutral
- **Abyss Floor** (#050d14): the deepest page ground (bottom of the body gradient).
- **Abyss Mid** (#071521): panel gradient base; the middle of the water column.
- **Abyss Panel** (#0a1e2c): panel gradient top; the shallowest surface on screen.
- **Sand** (#f5dfc1): primary text and numerals (carried the name *Ice Text* through earlier drafts of this file, which fought the warm family it belongs to); carries 30% of the sodium accent, so no white on the page reads as foreign. Never pure white.
- **Sand Secondary** (#b6a894): secondary text — always warm-tinted, never gray.
- **Sand Faint** (#7d7160): labels, tertiary text, stopped-state text.
- **Edge** (rgba(255,194,102,0.16)): the universal 1px border; a gold-tinted hairline, never a gray line.

### Ambient backdrop (carried over from the existing Kraken panel)
- **Abyss Void** (#02090E): the page's deepest ground beneath the depth gradient.
- **Depth Gradient** (#0d3340 0% → #082530 34% → #04141B 64% → #02090E 100%): the fixed water-column backdrop. The stops are uneven on purpose - the top third is the lit shallows and gets a third of the height, while the dark two thirds fall away slowly, which is what makes the page feel deep rather than merely dark.
- **Surface Light** (#FFD79A at low alphas): god rays and the breathing surface glow; backdrop only, never UI chrome.
- **Trench Ground** (#030d16 0% → #04121d 55% → #02090E 100%) with **Upwelling Violet** (#2a2033,
  a radial `100vw 70vh at 50% 130%` fading to transparent by 60%): the *second* backdrop. Every
  full-screen surface that opens over the pane — the drill-in overlay and the whole sheet family —
  drops this in place of the page's water column. Read the two side by side: the page gradient is
  lit from above and travels 0d3340 to void across four stops, while this one starts already dark,
  barely moves, and puts its only light *below the fold*, rising from 130% of the viewport height
  (see The Lit-From-Below Rule).

### Semantic (reserved)
- **Status Gold** (#d8b46a): healthy-zone data (e.g. RAM under 50%); pairs with position/pattern, never color alone. Also 2xx in the audit log.
- **Caution Violet** (#9d8cff): warning zone (50%+ RAM, temp warm band, warning events). Also 4xx in the audit log and the api reference, and the `no auth` mark on an unauthenticated route, and a **closed port on a running server** — the one place the panel is not guarding you, which is the same reading. Also an **agent whose version has fallen behind the panel's**: the node runs, but the panel is holding fixes it cannot execute yet, so something *is* prevented and the test comes out the same way.
- **Crisis Magenta** (#ff4d9d): critical zone only (75%+ RAM, hot band). Never decorative. Also 5xx in the audit log, and the `DELETE` method in the api reference — the same colour the house already spends on destroying a server.
- **Spectrum Deep Teal** (#1a7a6d): the cool end of the heat-spectrum strip (`.heat-ghost` / `.heat-fill`), and the only green on the page. **It is the last survivor of the pre-Kraken teal palette** - every other cool accent migrated to Caution Violet, and this one was missed. It is documented here as what the code actually renders, not as what the palette intends; retiring it is an open decision, because the spectrum strip is the one place a fourth hue earns its keep (it reads left-to-right as a scale, not as a status).

### Named Rules
**The One Light Rule.** Sodium gold is the surface's only light source and always means "alive". Violet and magenta appear only as semantics, never as accents. A stopped thing loses its light; it is never painted crisis-magenta for being off. Losing the light is the whole statement in that case - a stopped server dims and says nothing further. But **a thing that is off is not the same as a thing that blocks something else**: a closed port on a running server is not dim, it is a condition, and it takes Caution Violet. The test is whether anything is prevented. Nothing is prevented by a server you chose to stop; a player is prevented by a closed game port. And a **finished** thing is a third state again: a completed setup step keeps its colour and gives up its glow. It is not live, so it must not pulse; it is not off, so it must not dim. Solid gold with no bloom is what "done" looks like, and it is what lets one glow on the rail mean *here*.
**The Violet Pulse Rule.** Motion in the gold family means *alive* — a live dot, a packet crossing the channel, a meter taking a sample. So a thing that moves to ask for **action** must move in the semantic colour instead, never in the light: the agent-update chip sweeps in Caution Violet, and a gold pulse spent on an errand would make "alive" and "attend to me" the same signal. The finished-step clause above is unchanged — a completed step is not asking for anything, so it still must not pulse at all. Three constraints come with the licence, and they are what keep an attention pulse from becoming an alarm: the element's **geometry must not change** (light moves across or around it, so the pointer never chases a target that is resizing); pointing at it **settles** it (`animation-play-state: paused` — it has your attention, it can stop asking); and it **yields to `prefers-reduced-motion` by holding its lit state**, not by pausing mid-cycle. That last one is the one place the house parts with the ticker family's reduced-motion convention, and deliberately: a ticker paused mid-scroll is still readable, but a sweep frozen mid-pass is just a smear, and the signal has to survive the motion being switched off. A fourth constraint arrived with the chip's states: **the sweep belongs to the idle states only.** "Waiting for you" stops being true the instant the operator acts, so a control that is working, restarting or failed must not still be asking — and the licence is written as a *grant* to the states that sweep, never as a revocation from the ones that don't, because naming two cannot silently miss a third the way un-setting three can. And a fifth arrived when one of those busy states had something to *measure*: **an asking element's quantitative reading gets its own box.** The pushing chip's determinate fill is a second child under the same clip, not the sweep's `::after` doing double duty — one element cannot both ask and measure, and the geometry constraint is satisfied either way because the box that grows is inside the silhouette, not the silhouette itself.

**The Warm Light, Cold Water Rule.** The light is the only warm thing on screen; every ground stays abyssal blue-green. Severity moves *away* from the light on the colour wheel, so an alarm can never read as an accent.
**The Tinted Neutral Rule.** No pure grays anywhere: grounds carry the blue-green hue, text and hairlines carry the warm hue.
**The Token Channel Rule.** Any colour needing an alpha uses its `--*-rgb` channel token. Hardcoded triplets are how a palette half-migrates.
**The Whose-Fault Rule.** Where an HTTP status is shown — the audit log, and the response list on an endpoint row in the api reference — its colour comes from the status *family*, not from a verdict we invent: 2xx Status Gold, 4xx Caution Violet — the caller got it wrong — and 5xx Crisis Magenta, meaning we did. A 4xx is never painted crisis, because a mistyped password is not an outage.
**The Verb-Risk Rule.** Where an HTTP method is shown, it is lit by what the call can *do to you*, never by which verb it happens to be. A read is left unlit in Sand Faint because nothing happens; every method that writes takes Sodium Lumen; `DELETE` alone takes Crisis Magenta, because it is the only one you cannot take back. Three colours, not the five a generated reference reaches for by default — the difference between `POST` and `PUT` is already written in the word, and a hue per verb spends the whole palette saying nothing. A websocket route takes no colour at all, only the dashed edge the house uses for "different in kind".

## Typography

**UI Font:** Archivo (with Segoe UI fallback)
**Data Font:** Spline Sans Mono (with Consolas fallback)

**Character:** A workhorse grotesk carrying names and labels, with a precise mono carrying every live value. Personality lives in scale contrast and tracking, not in display flourish.

### Hierarchy
- **Headline** (700, clamp(19–34px), 0.04em, line-height 1.05): server names. The tight leading is what lets a Rack Label sit directly above the name instead of floating above it.
- **Title** (700, clamp(22–40px), 0.05em): the `h2` of every sheet and of the drill-in (`.depth-title`) — the one place a screen says what it is. The login title is its close cousin (clamp(22–34px), 0.06em). Node names are *not* Title: on a band they are the Hollow Nameplate, and panel titles are Label-scale caps, so this role appears exactly once per screen.
- **Data** (Spline Sans Mono 500, clamp(20–40px)): primary numerals — vitals, player counts. Always `font-variant-numeric: tabular-nums`, often with a soft gold text-shadow.
- **Body** (400, clamp(12–16px)): metadata lines, event text. Mono at weight 300 for machine strings (worlds, ports, versions).
- **Label** (600, clamp(11–14px), 0.18–0.28em, uppercase): section labels and metric names, in Sand Faint.
- **Rack Label** (600, 11px, 0.26em, uppercase, Sand Secondary): the owning node above a server name — `node 01 **behemoth**`, the whole label on the same value as the metadata line beneath it, with the node name at 700 as the only mark separating it from the id. This is the one tracked-caps type that sits *above* a headline rather than beside a value, because a node contains servers and its name is a heading, not a datum. Underlined by a gold hairline at 0.15 alpha whose width fits the text plus a `clamp(20px, 3vw, 60px)` overhang, so the rule stops just past the label and never crosses the card.
- **Wire Token** (Spline Sans Mono 500, 10px, 0.08em, as authored): an HTTP method on an endpoint row. Centred in a fixed 58px pill so that every route beside them holds one left edge down the whole list — the pill is sized for `DELETE` and the short verbs sit centred in it rather than closing the gap. The only type in the house that keeps its uppercase because the source does.

### Named Rules
**The Mono Means Measured Rule.** Spline Sans Mono is reserved for values a machine produced — numbers, addresses, versions, timestamps, and protocol tokens such as an HTTP method or a route. Prose and names never use it. A protocol token is also reproduced exactly and never folded into the house lowercase: `POST /servers` is what goes on the wire, so it is what goes on the page.
**The Hollow Nameplate Rule.** The node name renders as a large outlined letterform: transparent fill, 1.4px gold text-stroke, gold drop-glow, 0.1em tracking. Server names stay solid Archivo 700. The brand wordmark on the login screen earns it too — at the same 1.4px, which the chosen value landed on independently — because that screen has no data to be the lit thing and the name takes its place. Everything else stays solid, the header wordmark included: a fixed 1.4px stroke is 2.9–4.3% of the login mark (`clamp(32.5px, 3vw, 49px)`) but 5.4–8.2% of the header one (`clamp(17px, 1.2vw, 26px)`), so repeating the declaration there would be a heavier design rather than the same one smaller. Two nameplates, no third.

## Layout

One 100vh pane, `body { overflow: hidden }`, no page scroll ever. The pane is a three-row grid (`auto 1fr auto`) whose vertical order is a water column: identity strip on top, the deck in the middle, event stream at the floor. All sizing is fluid via `clamp()` with vw/vh so 4K gains density and detail rather than magnification; 1080p is the composed floor. Grids use `gap`, never margin stacks. Wide content scrolls inside its own container, never the body.

The **deck** is the middle row and the only thing on the page that scrolls: `overflow-y: auto` with `min-height: 0`, `align-content: start`, and a thin gold-tinted scrollbar. It holds every node band (full-width, instrumented) and every server card below them, stacked at `minmax(clamp(230px, 24vh, 360px), auto)` so a card can grow past its floor but never collapse under it. `padding-bottom: 2px` keeps the last card from reading as clipped rather than scrolled.

### Named Rules
**The Pinned Ends Rule.** The identity strip and the event stream are locked by being grid rows *outside* the scroller, never by `position: sticky`. Neither of them has a background: both sit directly on the page ground, which is what keeps the water column unbroken. A sticky element stays inside the scrolling box, so cards would travel visibly *through* the wordmark and the event line — and the only fix would be giving the ends an opaque fill, which is the thing the world is built to avoid. One `1fr` row scrolls; the two `auto` rows cannot. There is no `position: sticky` anywhere in the build, and that is on purpose.

## Elevation & Depth

Depth is atmospheric, not stacked: panels carry a top-lit vertical gradient (Abyss Panel → Abyss Mid) plus a deep soft shadow (`0 18px 50px rgba(0,0,0,0.45)`) and a 1px inner top highlight (`inset 0 1px 0 rgba(255,194,102,0.07)`). Hover raises a surface by 1px translate and warms its border toward the lumen — light response, not shadow growth.

### Shadow Vocabulary
- **Depth** (`0 18px 50px rgba(0,0,0,0.45), inset 0 1px 0 rgba(var(--lumen-rgb), 0.07)`): resting panels. The inset top lip is part of the shadow, not a border - it is the surface catching the light from above.
- **Glow** (`0 18px 50px rgba(0,0,0,0.5), 0 0 30px rgba(var(--lumen-rgb), 0.06)`): hovered/active live surfaces. The ambient shadow *darkens* as the glow arrives, so the surface reads as lit rather than raised.
- **Lifted** (`0 18px 50px rgba(0,0,0,0.51-0.55), inset 0 1px 0 rgba(var(--lumen-rgb), 0.07-0.084)`): overlays that float above a panel - the select picker and the modal card. Same geometry as Depth with both terms scaled ~1.13x, so a floating layer is the resting surface turned up rather than a new material.

### Breakpoints
Two numbers, three queries, and no relationship between any of them. Everything else is fluid:
the layout is composed against the 1080p floor Layout commits to and scales by `clamp()`
above it, so a
breakpoint here is always a specific structural failure being caught, never a device class.
- **1100px (max-width), twice:** the deck reflow. Above it the fleet is a multi-column grid and the node band sits beside it; below it both go to one column. The drill-in body collapses at the same number for the same reason.
- **640px (min-width):** an opt-in, gating the data-directory field's `grid-column: span 2`. It is a `min-width` rather than a `max-width` because `auto-fit` manufactures a second track for a spanning item even when only one track fits - ungated, a 380px viewport got tracks of 210px and 108px and every other row in the sheet was squeezed by a field that only needed room on wide screens.
- **640px (max-width):** the wizard's own two-column config grid (`.cfg-2`) collapsing to one. The same number in the opposite direction, and deliberately not folded into the rule above: one is a field asking for room it can only use when there is room, the other is a fixed two-track grid that has to stop being two tracks. Sharing a number is a coincidence, and writing them as one query would make the next person believe it is a system.

### Named Rules
**The Light-Not-Lift Rule.** Interaction feedback is a change of light (border, glow, brightness), never a bigger drop shadow.
**The Lit-From-Below Rule.** The page's water column is lit from above; anything that opens over
it is lit from beneath. A drill-in and every sheet replace the backdrop with the trench ground,
whose only light is an Upwelling Violet radial rising from 130% of the viewport height — below the
fold, off screen, inferred. That is the whole reason a full-screen surface reads as *further down*
rather than stacked on top, and it is done with a light source rather than a scrim: a scrim would
say "something is in front of this", and the house needs it to say "you have descended".

**The Topmost Closes Last Rule.** When one full-screen surface replaces another, the surface on top closes *after* the one beneath it has finished opening — never in the same tick. Every member of this house wipes with a 0.65s `clip-path` circle, so two of them transitioning at once means the viewer sees through one into the other mid-flight: during the Postgres restart the interstitial and the login screen were both mid-wipe and both partly visible. Open the lower one first, hidden behind the upper, then let the upper wipe away to reveal a screen that is already fully there. The reveal is free; the overlap is the bug.

## Shapes

Quiet rectangles with 6px outer radius (4px for controls, 3px for chips), 1px gold-tinted hairline borders, no other decoration. Capacity and meter bars are 6px tall with 3px radius. Status dots are 7–9px circles that glow when live. No pills except status chips; no cut corners; no outlines heavier than 1px.

## Components

### Panels / Bands
- **Corner Style:** 6px
- **Background:** linear-gradient(180deg, #0a1e2c, #071521)
- **Border:** 1px Edge; **Shadow:** Depth (+ inner top light)
- **Hover (interactive bands):** border to rgba(255,194,102,0.42), Glow shadow, translateY(-1px)

### Status Chips
- **Running:** gold text on rgba(255,194,102,0.09), 1px gold border, glowing 7px dot, uppercase 0.2em label.
- **Stopped:** Sand Faint text, hairline sand border, hollow dot. The whole parent band drops to 72% opacity.

### Node Condition (`.node-cond`)
An extra `.node-meta` line in the band's id cell, stating something true of the node that its
vitals cannot show. A band may carry more than one, so the layout and the key / value / separator
bits are shared: `.nc-k` is the 0.8em/0.18em Archivo caps key in Sand Faint, `.nc-v` the mono 300
values, `.nc-sep` whatever glyph divides them. **Exactly one value per line takes `.nc-v.act` and
its Caution Violet** — the one you would act on. The version being moved *to*, the containers
nobody is tracking; everything else in the line is context, and colouring context spends the
semantic for nothing.

A condition is not the node being unwell, which is why none of this touches Status: the node is
online, and something about it nevertheless needs attention. Two members so far — Agent Drift and
Container Drift below.

### Agent Drift (panel newer than the node's agent)

A **Node Condition line** in the band's id cell, not an object of its own: `agent` as the key, then
the two versions either side of a `&rarr;`, then the action. Only the version being moved **to**
takes `.nc-v.act`; the one the node is on is a fact and stays Sand Faint. It states itself as a line because the id cell is
already where what-this-node-*is* gets said — a badge beside the display name would have spent a
new silhouette on a sentence that fits the ones already there, and the name is the band's loudest
element, so anything docked to it competes with the one thing that identifies the row.

The action is a `.ad-go` chip: 0.76em/0.2em caps in Caution Violet, `3px 9px` on a 3px radius, and
**lit at rest** — a 0.5 violet border on a 0.05 ground. That is a deliberate departure from the
Ghost Button rule, and the reason is that this control exists only when there *is* an update: a
list of eight of them cannot happen, and "unlit until pointed at" would hide the one thing the chip
is there to say. It stays a ghost in every other respect; see the One Committing Control Rule for
why the band's update affordance is not a lamp.

Its motion is a **clipped traversal**, not a throb: a `::after` at `width: 55%` carrying a
`linear-gradient(100deg, …0, …0.34, …0)` violet highlight crosses the chip once per 3.6s
(`translateX(-160%)` → `260%` at the 45% mark, then a long wait) under `overflow: hidden`, so the
light is masked to the chip's own box and the chip's geometry never moves. The long tail matters as
much as the pass: a fleet parked on a second monitor gets a signal it can ignore between sweeps
rather than something strobing at the edge of vision. Hover pauses it, focus takes the house's 2px
violet outline, and `prefers-reduced-motion` drops the sweep and holds the lit state (0.6 border,
0.12 ground). All three behaviours are required by The Violet Pulse Rule.

**The chip's five states.** The line always carries exactly one `st-*` class, which is what lets
the sweep be granted rather than revoked (see above). The chip's label always names what clicking
it *does* — never the state it is in, which the line already says.

| state | class | sweep | chip label | what it means |
| --- | --- | --- | --- | --- |
| rest | `st-rest` | yes | `update` | the panel is newer; the action is available |
| ahead | `st-ahead` | yes | `match panel` | the **agent** is newer, after a panel rollback: the action downgrades it, so `update` would be a lie |
| pushing | `st-pushing` | no | `pushing…` | the Panel is streaming the binary; the chip is disabled and carries a determinate fill |
| restarting | `st-restarting` | no | `restarting…` | the agent took the binary and is rebooting into it; disabled, and held with no timeout |
| failed | `st-failed` | no | `retry` | the reason is on the line and takes the act colour |

**`pushing` measures itself.** The chip carries a second box for it — `.ad-fill`, an
absolutely-positioned first child under the same `overflow: hidden`, with the label moved into a
positioned `.ad-lbl` sibling so it paints above the fill rather than being washed over by it.
Two jobs, two elements: the `::after` is the sweep and says "something is waiting for you", and
the fill is a reading. Its width is `var(--push-pct, 0%)` under `.agent-drift.st-pushing` **only**,
with `transition: width 0.3s linear` — a 17MB push over a switch is two poll ticks, and without the
transition the bar is one frame at 0 and one at 100. Reading the var nowhere else means a stale
`--push-pct` left on the line cannot paint a bar after the push ends, and a preflight that failed
before sizing (`bytes_total === 0`) sets no var at all, falling through to the `0%` default instead
of lying with a full one. Position is the quantitative channel and the label stays qualitative:
a percentage ticking inside a 9px tracked-caps chip is noise, and the operator's actual question is
"is it moving". The Violet Pulse Rule still holds — the chip's geometry never changes, only the
light inside it — and under `prefers-reduced-motion` the fill keeps its width and drops the tween.

Two of those are worth their reasoning. **`ahead` keeps the action** rather than going
information-only: a mixed fleet after a panel rollback genuinely needs it, and the arrow in the
line is already honest about the direction. And **`failed` hands the act colour to the reason** —
the target version gives it up, because one value per line carries the colour and in that state
the reason is the thing to act on. The reason itself has two sources rendered identically: the
job's error (the push refused) and `last_update_error` (the agent rolled back after restarting,
which reaches the panel through the node record and had previously been rendered nowhere at all).

`restarting` is deliberately unbounded. The outcome is not the job's to report: either the drift
line dissolves because the node came back on the new build, or `last_update_error` appears. Both
arrive from the node record, and a timeout here would only let the UI guess ahead of it.

### Container Drift (the panel has lost track of a container)
A Node Condition line reading `containers · 3 running · 1 untracked`, where the surplus takes
`.nc-v.act`. The two numbers come from opposite directions and that is the whole value: `3 running`
is the **agent's own count** of its `kraken.managed` containers, adopted into the node record on
reconcile, and the tracked figure is the servers the *panel* has placed here. Everything else in
the panel reasons from its own rows outward — `reconcileOnce` walks its records and asks the agent
about each — so this is the one place the question runs the other way, and the only way an
untracked container can ever be seen.

It exists because that state is reachable: deleting a server while its node is unreachable removes
the row and leaves the container running, since the agent call is best-effort. Nothing else would
ever notice. Caution Violet is the right band for it by the closed-port test — the node is
perfectly healthy, but something on it is outside what the panel is guarding, and a surplus
container is holding memory and ports the scheduler believes are free.

A **deficit** reads the same way and is not an error either: containers stopped behind the panel's
back. The line names whichever direction is true rather than assuming a surplus.

### Locked Node (cordon)
**"Lock" is the operator's word; `cordon` is the API's.** The panel says lock, `POST
/nodes/{id}/cordon` says cordon, and that split is deliberate — do not reintroduce "cordon" into
the interface. What it means: the scheduler places no new servers here. Nothing already running is
touched, which is the whole reason it is not destructive — no crisis colour, and no confirmation.

Two halves, deliberately different in kind:

- **The mark** — a 21px drawn padlock (`.node-lock`) beside the display name, Caution Violet with
  the house's 8px bloom. An indicator, never a control. Its entire job is that a node you locked
  three days ago and forgot cannot look identical to one you did not, which is why it rides the
  name: that is where the eye already lands when scanning the fleet. The stroke is **1.2 on a
  6-unit body** — about 20%. That number is load-bearing: the first draft was 1.6 at 15px (27%),
  which closed the interior to under 2px and read as a solid blob. A drawn glyph has to keep its
  interior, and going bigger alone does not fix it — SVG scales stroke proportionally, so the size
  and the weight had to move together.
- **The control** — `.lock-node`, a full-size ghost in the node-settings footer sitting beside
  `delete node`, so the two node-lifecycle controls group at one end and the hairline `.acts-split`
  already draws separates both from `close` and `save & apply`. Geometry comes from
  `.cfg-btn.ghost`; `.lock-node` adds only the glyph layout, and `.on` the locked state's Caution
  Violet (0.5 border on a 0.05 ground).

Locked takes the condition colour by the closed-port test: the node is perfectly healthy, and
something is nevertheless prevented. That it is *intended* — a hold you put there yourself, unlike
version drift or a closed port — does not change what the colour is for. It does change the motion:
`.nc-go`'s chip carries no sweep, because a sweep says "something is waiting for you", which is
false of your own hold. Only the agent update sweeps.

**The status word gives way to the mark.** A node band must not say `cordoned` in its meta line and
show a padlock beside its name — the same fact twice in one cell. The glyph wins because it is the
glanceable one; the meta line keeps `online`.

### Metric (vital readout)
Label (tracked caps, Sand Faint) + huge mono value with a gold glow. Unit suffixes (`%`, `/64G`, `Mb/s`, `°C`) render in Status Gold at 0.5em. The chart zone beneath is one of the signature meters below (disk still carries the legacy gold area sparkline).

### Label-Value Row
The smallest readout in the house and the one to reach for first: tracked caps in Sand Faint on
the left, the value as mono tabular `<b>` in Sand on the right, pushed apart by
`justify-content: space-between` on one line. The label is uppercase with 0.12em tracking and the
value explicitly cancels both (`letter-spacing: 0`, `text-transform: none`) — a machine value is
never tracked, per the Mono Means Measured Rule. A `.railed` variant swaps to a
`74px 1fr auto` grid when a run of rows needs its values to line up in a column instead of each
finding its own right edge. Three readouts, one ladder: this row for one fact, **Metric** for a
vital worth a glow, the **dot-matrix meter** for a fact with a history.

### Dot-matrix history meter (signature)
The house chart: a row of dot columns (`--cell: 6px`, `--dot: 2.4px`), each column one sample, scrolling left as new samples arrive; the newest column glows (`.now`). History fades toward the left via mask. Track length is set by the surface, not by a global: **48 columns** on a node band, **72** on a server card, so the wider card buys more history rather than fatter dots. **Zone-coded variant** (`.zone-track`): columns recompute their color live — Status Gold below 50%, Caution Violet 50–75%, Crisis Magenta above 75% — with faint threshold guide lines at 50/75.

`--lvl` drives both the column's fill height *and* its zone color, which is why the sample band matters as much as the styling: a track whose values never leave one zone renders as a single-color block at a single height, and the chart stops being a chart. Both the seeded history and the live walk must span a threshold, and a walk with hard clamping will not do it — it piles up against whichever bound it drifts into. Mean-revert toward the middle of the band instead. The last column carries `.now` permanently, because the tick shifts values *between* columns rather than moving elements.

### Heat spectrum fill (temp)
A 12px rail carrying the full thermal gradient (Spectrum Deep Violet → violet → gold → magenta) as a faint ghost (0.22), with a solid fill clipping to the live value (20–90°C scale) and a glowing edge line; cool/warm/hot labels beneath. The gradient is anchored to the scale, so warming expands the fill into violet/magenta territory.

### Packet channel (network)
A bordered lane between `wan` and `lan` endpoint tags; gold packet dots animate across (translateX keyframes over container-query width), bigger dots = bigger packets and slower travel; return traffic dimmed. Whole-channel rate (`--rate`) scales with live Mb/s.

### Game art layer (server cards)
Each server card carries its game's key art as `.srv-art` — an absolute paint layer (zero layout impact) in **abyssal duotone**: `grayscale(1) sepia(0.5) hue-rotate(125deg) saturate(1.6) brightness(0.55)` at 0.5 opacity under a `.srv-shade` legibility gradient. Stopped servers desaturate fully and drop to 0.25.

### Drill-in overlay (server depth)
Full-screen fixed overlay opening with a circular clip-path plunge from the click point; SURFACE button and Esc return. Layout: live console (left, streaming mono log with severity colors) + side column of press-travel controls (stop/restart, or start when stopped), player roster with kick, vitals, the network ledger, backups, schedules, and the danger block. Per-server data; a stopped server shows a dark room.

### Capacity Bars
6px track in rgba(255,194,102,0.11); fill is a gold gradient with glow. Bars mean quantity-of-capacity, never health.

### Solid Button (the one lit control)
The house's only filled control, and the only place the palette inverts: Abyss Floor text on a
Sodium Lumen → Ember Gold gradient with no border, plus a `0 6px 20px rgba(--lumen-rgb, 0.25)`
lumen-tinted cast beneath it. Everything else on screen is light on dark; this is dark on light,
which is what makes one per surface enough. It appears at two sizes off the same recipe — the
sheet-footer control (`.cfg-btn.solid`, 11px/0.24em, 10x18 padding) and the standalone action
(`.bk-big`, 12px/0.26em at 700, 13px all round) — and both press with `translateY(2px)` on
`:active` rather than a shadow change, because a lamp you push should move.

**The One Committing Control Rule.** One filled control per surface, and it is the one that
commits. A screen with two solid buttons is a screen that has not decided what it is for; every
other action on it takes a ghost. This is the One Light Rule spent on controls instead of data.

Its disabled states are deliberately not the same: the footer control keeps its gold and drops to
`opacity: 0.4` with `filter: none`, while the standalone one goes `grayscale(0.5) brightness(0.7)`.
The first sits in a row of controls where a grey button would read as missing; the second stands
alone, where a still-gold button would read as pressable.

### Ghost Button ("open" affordance)
Tracked-caps label + drawn SVG stroke icon (1.5px, round caps), 1px Edge border, 4px radius. Unlit at rest, always: a list of eight of them lit would spend more light than any screen here earns.

The one documented exception is the agent-update chip, and it earns it by **existing conditionally**: it is rendered only when the panel is newer than the node's agent, so the "eight of them lit" case cannot arise, and an unlit chip would suppress the only fact it was added to carry. The test for any future exception is that same one — not "this action is important" (every action's owner thinks so), but "this control is absent whenever it has nothing to say".

How it lights depends on whether its **parent is the target**. On a server card the whole card is the thing you are opening, so hovering the card fills the button — the button is a label for the card’s own affordance. In a row of records the row is not a target and holds more than one control, so hovering the row only **warms the primary action’s edge** (`color` to Sand Secondary, border to lumen at 0.4) and the fill stays the control’s own to give on its own hover or focus. Pointing at a row is not the same as pointing at its control.

The two-stage form has a specificity trap: `.row:hover .btn` is (0,3,0) and `.btn:hover` only (0,2,0), and hovering the button also hovers the row — so the plain form loses and the button never fills. Write the fill as `.row .btn:hover, .row .btn:focus-visible` (also (0,3,0)) and place it *after* the row rule, so equal specificity plus source order decides it. That ordering also keeps the fill on a focused button whose row happens to be under the pointer.

### Brand Lockup
The glyph and the wordmark on one line, in both places the name appears (`.kr-lock`). The glyph is sized in `em` off the wordmark (`width: 1.095em`, `aspect-ratio: 398 / 429`) rather than in pixels, so the 26px header and the 49px login mark get the same lockup instead of two tunings; the gap is `0.43em` for the same reason. `align-items: baseline`, so the **word** gives the lockup its baseline — an inline-flex box takes its baseline from the first item that has one, and an empty block's baseline is its bottom edge, which rides the wordmark high above anything it should sit level with. The glyph then re-centres against the word with `align-self: center`. It arrives as a `background-image` under a filter chain that starts at `brightness(0)`, so the gold is built from black and does not depend on what color the source file happens to be, plus a `drop-shadow(0 0 14px)` in lumen at 0.4. A `mask-image` would be the exact answer and cannot be used cross-origin: masks are CORS-gated and a blocked mask paints nothing at all, silently.

### Sheet (full-screen overlay family)
The house's one navigation mechanism, since there are no routes. A sheet is `position: fixed; inset: 0` at `z-index: 35`, opening with the same circular `clip-path` plunge from the click point that a drill-in uses (`circle(0%)` → `circle(150%)` at `--ox/--oy`, 0.65s), over a ground of its own: a violet-tinted radial rising from below the fold on the abyssal gradient, so a sheet reads as *deeper* than the pane rather than stacked on it. Two rows, `auto 1fr`: a title bar that stays, and a `.sheet-body` scroller with the house thin gold scrollbar. The body reads a `--measure` token set on the sheet — 1040px by default, 1240 for the spec sheets, 1280 for new server, 1640 for the audit log, and 2000 while the spec code view is open — and is centred in it with `margin-inline: auto`. **The title bar is not capped.** It keeps the full width it has on `.prefs`, the one multi-column sheet, so `surface` and the sheet name land in the same place on every page no matter how wide that page’s content column is: the bar is page chrome, and chrome does not move because the content beneath it got narrower. Centring uses auto margins and never `justify-self: center`, which would size the column shrink-to-fit and let its content decide the measure. `.prefs` takes no measure at all: a two-column split is not a single-column view. Escape dismisses, focus lands only once the plunge finishes, and each sheet remembers what opened it so closing returns there rather than to the pane. Membership in this family *is* the Escape behavior — the login screen is deliberately not a member.

The first-run wizard is a member with **one deliberate omission: it has no `surface` button.** Every other sheet can be left because there is a fleet behind it; on first run there is not one yet. The way out is the footer's own opt-out — "skip for now", "skip & finish" — which says what leaving means instead of offering a way back to a page that does not exist. Its focus landing follows from that: with no `surface` button to catch focus, the plunge ends on the open step's first field, or on its primary action when the step has no fields.

### Modal Card (centred dialog family)
The house's *other* overlay, and deliberately not a member of the Sheet family. A modal card is `position: fixed; inset: 0` with `display: grid; place-items: center` and a 24px gutter, over a full-bleed veil layer that closes on click; the card itself is `width: min(<measure>, 100%)`, 6px radius, 1px border, the panel gradient (`#0a1e2c` → `#071521`) and the **Lifted** shadow. Two members so far: the typed-delete confirmation (`.confirm`, `z-index: 40`, 430px, crisis-tinted border) and SFTP access (`.sftp`, `z-index: 42`, 780px, neutral Edge border). They are the same card at two widths, and the border colour is the only thing severity changes.

**The No-Plunge Rule.** A modal card appears; it does not descend. The circular `clip-path` wipe is the Sheet family's signature and it means *you have gone somewhere* — so a surface you will dismiss in ten seconds without leaving the page must not spend it. The measure follows from the same reasoning: a card is sized to its content, not to a `--measure` token, because it is an interruption rather than a page. 430px holds one sentence and one input; 780px is what the SFTP connection URI needs to sit on one line, and the width was chosen by what wrapped, not by a scale.

**The Dim-What-Is-Lit Rule.** The shared veil is `rgba(2,9,14,0.78)`, and what it dims is the *content*, not the ground. A panel at `#0a1e2c` lands near `#040e15` beneath it — roughly halved — and lit controls fall with it. The grounds barely move: the trench a drill-in and every sheet use (`#030d16`) resolves to about `#020a10`, one or two levels per channel. That is not a failure, it is the point. A near-black ground has no light to take, and it does not need to lose any: the separation comes from every lit thing standing on it going quiet at once. Judge a veil by what happens to the panels and the buttons, never by arithmetic on the backdrop colour — and judge it with the surface it will really open over actually open, since a modal checked against the pane has been checked against the wrong ground.

**The One Sighting Rule.** A secret the panel does not store is shown exactly once, and the interface says so in the same breath. Plaintext lands in a `rgba(caution-rgb, 0.45)`-bordered well — Caution Violet, not Crisis Magenta, because losing it costs a rotation, not an outage — with a copy control and a single line naming why this is the only sighting. Closing the dialog clears the reveal rather than remembering it, which is exactly what reopening does in production. Never render a stored-secret field that *looks* like this one; the border is a promise about the value's lifetime.

### Tab Strip
Radio-driven, no script: caps at 600 with the widest tracking the house spends on a control (0.28em - only the wordmark at 0.34em and section labels at 0.3em go wider) in Sand
Faint, 10x16 padding, over a 1px Edge rule under the whole row. The active tab goes Sodium Lumen
and grows a lumen bottom border pulled onto that rule with `margin-bottom: -1px`, so the strip
reads as one line the active tab has claimed a segment of rather than a box with a lid. Hover only
lifts the label to Sand Secondary — pointing is not choosing.

**The Tabs Don't Travel Rule.** The strip moves between panels; it never changes what the screen is. Page-level movement
belongs to the sheet family, which plunges. A tab is a swap inside one surface, so it gets the
cheapest possible transition (`color 0.15s`) and no motion at all on the panel beneath: the
`:checked ~ .panel` sibling rules simply lay out one panel and not the others, and a panel that is
not chosen is `display: none` rather than hidden, so it costs no layout.

**The Far-End Affordance Rule.** The strip may carry things that are not tabs, and they go to the
far end on `margin-left: auto` where nothing can be mistaken for the next tab in the sequence. A
tab and a far-end affordance must not share a silhouette: tabs are bare tracked caps that mark
themselves with a bottom border on the rule, so an affordance takes the opposite shape — a bordered
4px chip on a 5% lumen ground (`.stn-chip`), lifting to a 0.4 lumen border and a 9% ground on hover
per the Light-Not-Lift Rule. It earns the strip rather than a panel of its own when it is *read
once and left*: SFTP connection details are set when a server is created and then carried to another
client, so a fourth tab would spend a permanent seat on a destination nobody revisits. The chip
also does a tab's job in passing — it shows the live port (`:2022` in Wire mono against a Sand Faint
tracked key), so the strip answers "is SFTP even on?" without being opened.

### Corner Segmented Switch
A two-option toggle parked in the top-right corner of the surface it governs (`.cd-sw`, at 20px
inset): the smallest type in the house (9px caps, 0.2em, `--ui` not mono) in a 4px-radius bordered
pill on a `rgba(3,10,17,0.82)` well with `backdrop-filter: blur(4px)`, options divided by a single
Edge hairline and the selected one carrying Sodium Lumen text on a `--lumen-soft` ground.

**The Quiet Until Asked Rule.** A control that changes *how you are reading* something sits at
0.775 opacity until the pointer is anywhere in the surface it governs, then comes up to full. The
container is the hover target, never the control itself: you notice you want the switch while
looking at the content, not while looking at the corner. The unchosen option is `display: none`, not
`visibility: hidden`: only the chosen serialisation exists, so nothing is laid out twice and the
scroller's height is honest. The first line of the content it governs carries a 96px right pad so
it cannot run under the pill.

### Data Table (subgrid)
Rows of records — the audit log, the user list — are one grid, not a stack of independently laid-out rows. The container declares the column measures once (`minmax(0, 1fr)` for the elastic ones, `max-content` for the ones that must not wrap) and every head and row is `grid-column: 1 / -1; grid-template-columns: subgrid`, so all of them inherit those tracks and nothing can drift out of alignment. Head labels are 9px/0.22em caps in Sand Faint; rows are mono 300 at 12.5px with `tabular-nums`, separated by a `rgba(--ink-3-rgb, 0.14)` hairline, hovering to a 5% lumen wash. Row state is a left bar (`box-shadow: inset 3px 0 0`) in the semantic color — caution on a 4xx, crisis on a 5xx, lumen on "this is the account you are signed in as" — never a filled row background. The discipline is not table-only: the users sheet’s role legend runs a `max-content` label column against a description column the same way — each legend line is `grid-column: 1 / -1; grid-template-columns: subgrid` — so all four role names sit flush on one left spine. As a wrapping flex row, the second line inherited no column edges from the first, and `operator` landed wherever `owner` happened to end.

### Net Table (state ledger)
A run of Label-Value Rows grown into one shared grid — the drill-in’s network block. The
container declares four tracks once (`1fr auto minmax(2.4em, auto) minmax(56px, max-content)`)
and every row is `display: contents`, so the numerals hold a real column, unit suffixes (`udp`,
`local`) hang in a mono 0.82em Sand Faint track of their own, and each tail control — state
pill, copy button, publish flip — stretches to fill the same fourth track and stays matched
if a label’s wording changes. Port state is a real control with no script: a visually-hidden
checkbox and a `label.net-state` pill (10px/0.18em caps, 3px 9px, 3px radius). Below an Edge
hairline (`.dns-sep`), the DNS rows carry inline-editable values (see Form Field) with the
record kind (`dns`, `srv`) in the unit track and the publish flip in the tail.

**The Both-Faces Rule.** A flip control names both of its states in the DOM and stacks them in
one grid cell (`grid-area: 1 / 1`), showing only the live face, so toggling can never resize
the control or reflow its row. Checked wears Status Gold (text plus a 0.4 border), unchecked
wears Caution Violet at 0.45 — the closed-port reading the One Light Rule assigns. The stack
is reused wherever a word must swap in place: the endpoint’s copy button flips
`copy`/`copied` (going Status Gold once done) and the DNS publish control wears
`unpublish`/`publish`. The tracked caps leave a trailing 0.18em inside each word’s text box;
every face cancels it with `margin-right: -0.18em` so the glyphs sit on the pill’s true
optical centre.

### Schedule Row
A standing order in the drill-in’s schedules block: the order’s name in mono 300/13px over
its `action · cron` in an 11px Sand Faint small, with the next firing at the right edge in
Status Gold — the same voice the backup list uses for `scheduled`, because “03:00” here and
“tonight 03:00” there are the same promise. Paused keeps its place and gives up its ink: the
row drops to 0.55 opacity, the firing slot reads `paused` in Sand Faint, and the pause pill
becomes `resume` — an order that is off is still a fact about this server, so it dims rather
than leaves. Every control in the block is a ghost — the `.mini-act` pause/delete pills, the
full-width `new schedule` ghost button — because the backups block above already spends this
surface’s one solid button (`create backup now`), per the One Committing Control Rule. The
add form reveals in place under an Edge hairline: house fields for name, action, and cron,
plus a row of cron-preset chips (mono 10px/0.08em, 3px 8px) that write the expression for you
and light like Filter Chips (lumen text on lumen-soft, a 0.35 lumen border) when the cron
field matches — synced by script rather than `:has()`, because they set a value instead of
filtering a list.

### Filter Chips
Tracked-caps chips that filter a list in place, with no script: a visually-hidden radio inside each label, the chip lit via `:has(:checked)` (lumen text, 0.5 lumen border, lumen-soft fill) and focusable via `:has(:focus-visible)`. The list itself hides non-matching rows with `:has()` on its body. A chip filters, it never re-sorts and never paginates — and when a filter empties a group, the group heading and its count go with it rather than being left behind as a heading over nothing.

### Form Field
Mono 13px on a `rgba(5,13,20,0.6)` well — darker than the panel it sits on, so a field reads as cut into the surface rather than laid on it — with a 1px Edge hairline and 4px radius. Focus warms the border to `rgba(--lumen-rgb, 0.45)` and removes the UA outline. A disabled field drops to Sand Faint and switches its border to **dashed**: the house mark for "this value is real but not yours to change here", the same dashed edge an env-managed setting and a websocket route wear. A disabled `select` also dims its picker icon to 0.4 — the dashed border already says the value is fixed, and a chevron that still looks live argues with it. That is how a spec offering one option locks the control rather than hiding it: the choice stays visible and stays unavailable. A **read-only box** (`.cfg-ro`) wears the same dashed edge and is mono on the same well, but it is `nowrap` with `overflow-x: auto` because it exists to hold one opaque machine value — a DSN, an invite URL — at full measure. It is the wrong control for a value that is really *several facts* in a narrow slot: `nowrap` plus `overflow-x` hides the tail behind a sideways drag, and a summary you have to drag is not a summary. Either give that value a control it fits — the new-server sheet ended up making its OS fact a `select` — or let it wrap with each fact marked `nowrap`, so the separators are the only places a break can land.

**Inline-editable value** (`.dns-ed`): a third answer for a value that is usually *read* and only
occasionally *set* — the network box's hostname and srv service. It renders as plain mono text in
the row; hovering washes it `--lumen-soft`; clicking focuses a `contenteditable="plaintext-only"`
span that puts the house field well under it (`rgba(5,13,20,0.6)` ground, a 1px lumen-0.45 ring
via box-shadow); blurring returns it to text. No pencil icon, no edit mode, no permanent input
box — a fact you can touch, not a form you must operate. Reserve it for single mono facts inside
composed rows; anything with validation, options, or consequence stays a real Form Field.

**The Head-Not-Edge Rule.** The chosen row in a picker is marked at its head, not its edge. With `appearance: base-select` the
option list is ours, and selection there is a leading indicator column: every option reserves a
15px ring in Sand Faint at 0.6, and the chosen one fills its ring with the house tick in Sodium
Lumen. Both marks are `mask` layers over a `background-color` — the ring and the tick are black
SVGs in custom properties on the select, so the shape comes from the mask and the colour from the
token, and neither hex is written a second time. Because the mark is a fixed 15px slot at
`order: -1`, all eight labels align on one axis and the list reads as a roster you scan down. The
checked row gives up both of the things it used to carry — the inset lumen bar and the lumen
label — and goes back to Sand: **the filled ring is the one lit thing in the row.** The trailing
`::checkmark` glyph the browser supplies is not restyled, it is replaced: `content: ''` empties
it, and it is `display: none` on every row that is not checked so the reserved ring shows
through instead.

### Toggle Switch
A 34x18 pill with a 12px knob, mono 13px label to its right, and the whole thing a `<label>`
around a visually-hidden checkbox — the same no-script pattern as the filter chips. Off: a
`rgba(--ink-3-rgb, 0.25)` ground on an Edge hairline with a Sand Faint knob. On: the ground warms
to `rgba(--lumen-rgb, 0.25)`, the border to 0.5 lumen, and the knob slides 16px and becomes the
lit thing — solid Sodium Lumen with a `0 0 8px rgba(--lumen-rgb, 0.7)` glow. **The knob carries
the light, not the track.** That is the One Light Rule at control scale: a switch that is on has
one small bright thing in it, and a switch that is off has none, so a column of them scans as a
count of what is live without reading a single label. Only the ground, border, knob position and
knob colour move; the pill never changes size.

### Command Block
A `<pre>` of mono 300 at 12px on the deepest well in the house (`rgba(2,9,14,0.6)`), holding an
install command you are expected to copy. Every `<b>` inside goes Sodium Lumen: those are the
parts you must substitute — the enrollment token, the CA fingerprint — so the one lit thing in the
block is the thing that is not done yet.

**It wraps, and that is the opposite of what `.cfg-ro` does on purpose.** A read-only box holds one
opaque value and stays `nowrap` with `overflow-x: auto`; a command is several lines that must be
read in full before anyone runs it, so hiding the tail behind a sideways drag would hide the flags.
Continuation lines fold with a **hanging indent** instead — `white-space: pre-wrap` plus
`text-indent: -3ch` against a `calc(14px + 3ch)` left padding — so a folded line is visibly a
continuation rather than a new argument. Two controls, two answers, chosen by whether the value is
one fact or a sequence.

### Destructive Control
Crisis Magenta, at one of three weights chosen by the control’s size, all off the same tokens. A **table-row pill** (`.mini-act.del`) carries the colour on its text and a 0.45 border with no ground — eight washed pills in a list would read as a warning about the table rather than about a row. A **full-size button** (`.cfg-btn.danger`) and a **drill-in control** (`.ctl-delete`) both take text, a 0.35 border and a 0.08 crisis ground. Those last two stay separate rules on purpose: the `.ctl-*` family hovers with `filter: brightness(1.3)` and `.cfg-btn` controls move their own values, so one shared selector would force one base’s idiom onto the other.

Anything irreversible is gated by the typed confirmation, never by a second button alone: the dialog names the thing, states what is lost, and keeps its confirm disabled until the word `delete` is typed — matched case-insensitively, so `DELETE` works. The dialog takes its **noun** from whatever opened it (`[data-confirm-open]`), and so does its warning, because "this removes the world, backups and config" is true of a server and false of a spec. A confirmation that describes the wrong thing is worse than none: it teaches people to click through.

**The Opposite Ends Rule.** A destructive control that shares a row with the surface's committing
one takes the **far end** of it, on `margin-right: auto`, with a hairline
(`rgba(--ink-3-rgb, 0.22)`) between it and the safe cluster: the row's own geometry is what says
these two are not the same kind of thing. It is opt-in per row (`.cfg-actions.acts-split`) and
never the default, because a delete that drifted into the plain `.cfg-actions` would sit one
mis-click from `save & apply` on every sheet in the house. The node-settings footer is the first
member — `delete node · … · close · save & apply` — and its warning is the pattern for writing a
new noun's: everything in it is true of a node and false of a server (the agent keeps running, the
containers keep running, nothing on the host is stopped or deleted, the servers placed here lose
their node, and re-adding needs a fresh enrollment token because the old identity is orphaned).

### Split Read-Only Field (a value you also have to act on)
A `.cfg-ro` that carries an action becomes two cells rather than a control floated over the value
(`.cfg-ro.tok-split`): the value keeps its own `overflow-x` and the action takes a fixed-width
gutter, so a long machine value scrolls *under* a control that stays put. A control that scrolls
out of its own field is worse than no control, which is the whole reason for the split.

The gutter carries **no tint and no seam at rest** — the value is the field's only permanent ink,
and the control claims its cell on hover (`rgba(--lumen-rgb, 0.09)` wash, Sand Faint warming to
Sodium Lumen). That is the Ghost Button rule applied to a cell: an affordance the operator uses
once per token does not get to spend light while it waits. Three details are load-bearing: the
button **fills the cell** (`width/height: 100%`, `min-height: 38px`), so the target is the gutter
rather than an island inside it; the focus ring is **inset** (`outline-offset: -2px`), because the
well is `overflow: hidden` and clips an outset one away entirely; and a glyph-only control drops
`.ns-w`'s `-0.18em` and `letter-spacing`, because that cancel exists for a *text* pill's trailing
space and skews a lone glyph off centre. First member: the enrollment token's refresh
(`.tok-new`), a 17px rotate arc at the house 1.5px stroke.

### Step Rail (first run)
Five stops on one line, and the light travels down it. The current step carries a lumen ring and
the house dot-glow; steps behind it are **solid lumen discs with a `--d0` check and no glow**;
steps ahead are a Sand Faint numeral on a 0.28 ink ring. The connector *after* a step lights to
`rgba(--lumen-rgb, 0.4)` when that step is done and stays 0.18 ink otherwise, so the lit run of
the line is exactly how far you have got. The connector is offset by half the dot (`margin-top:
15px`) to sit on the dot's centre line rather than the centre of the dot-plus-label stack.

**Done is a class, never a positional selector.** A sibling combinator would be cheaper —
"this step is behind whichever one is checked" — and it would be a lie: rotating the bootstrap
password finishes step 2 while step 1 is still the current one, so completion does not follow
document order. Reachability is separate again: the rail's own buttons are live only for steps
that are done, current, or the immediate next one, because a step you have not got to is not a
place to jump to. Under 1100px the labels drop and only the current step keeps its name.

### Strength Meter
Four discrete segments, not a bar with a percentage. A bar invites the reader to optimise a
number; four states say "this is a floor to clear", which is what a password rule is. Segments are
0.18 ink until earned and solid lumen once earned — a strength reading is advice, not a status, so
it never reaches for the caution or crisis bands however weak it is. The **gate and the meter are
deliberately different questions**: twelve characters is the only thing that unlocks the button,
and the four segments are commentary above it. One word per segment (`weak`, `fair`, `good`,
`strong`) and no fallback in the lookup — an empty string for a real score is falsy, so a `||`
default reported the *best* reading for the worst passwords that cleared the floor.

### Interstitial
A full-screen surface for the moments the panel is not running — the restart onto Postgres. Its
own layer at `z-index: 55`, above even the login screen, on the same water column; a 520px centred
card on the resting panel shadow. Motion is **three dots dimming in sequence on the meter band's
2600ms cadence**, not a spinner: the panel is busy, not broken, and the house already says "working"
with rhythm rather than rotation. It gets its own surface rather than being a state of the thing it
interrupts, because the thing it interrupts is exactly what is not running.

### Login Screen
The one screen that is not the single pane, because it is what comes before it. A 408px card centred on its own copy of the water column — the app’s ambient backdrop lives below the pane and cannot show through an overlay above it, so the screen paints the same gradient, surface light and rays for itself. Identity block centred, form left-aligned, one full-measure solid button, and no links at all: a self-hosted panel has no public registration and nothing to recover a password with. Nothing on the screen is alive yet, so the wordmark is the only thing that gets light. **The refusal says one thing for both halves** — "that username and password do not match" — because naming which half was wrong answers "does this account exist" for anyone who asks; both fields then carry the same Caution Violet bar the audit log will give the attempt a second later. Escape does not dismiss it, which is why it is deliberately not a member of the sheet family.

### Endpoint Row (api reference)
A `<details>` with no script behind it. Closed: a Wire Token pill, then the route in mono with every `{placeholder}` dimmed to Sand Faint so the fixed part of the path scans first, then the summary set against the right edge, then any `no auth` or `loopback` mark. Open: the detail block indents to the *route’s* left edge rather than the pill’s, because it belongs to the route — parameters, request body, responses, and the lowest role that may call it. Hover and open share one treatment, a 5% lumen wash, per the Light-Not-Lift Rule. Groups are filtered in place by the chip row, and a group with nothing left to show is removed with its rows rather than left as an empty heading.

### Event Stream
Single mono line per event: time in Sand Faint, actor in Sand, description in Sand Secondary; caution violet only when the event itself is a warning. Synthetic data carries a dashed-border tag.

The floor is a **ticker**, not a list: the events sit on a `.ev-rail` (`width: max-content`) inside the `overflow: hidden` `.events` window and travel right-to-left forever at `evTicker 46s linear infinite`. The easing is the load-bearing choice — `linear`, because a feed that has no beginning and no end must have no acceleration either; any ease would imply a start and a stop that the data does not have. 46s is slow enough that a line crossing the window can be read without chasing it.

**The Doubled Set Rule.** A continuous ticker duplicates its content in the DOM and translates the rail by exactly `-50%`. The seam then lands on an identical frame and the loop is invisible; animating a single set to `-100%` walks the content off one edge and leaves the window empty until it re-enters, which is a gap the eye reads as a stall. The consequences are not optional: the two sets must be **the same set** (in production, one array rendered twice — never two hand-written copies, which drift the moment the data is live), the duplicate carries `aria-hidden="true"` so every event is announced once, and the rail's halves must measure equal or `-50%` misses the seam. Measuring both `.ev-set` widths is the acceptance test, not reading the CSS.

Motion yields twice. `.events:hover` and `:focus-within` set `animation-play-state: paused`, so a line can be held still and read without losing the floor's click-through to the audit log — parking is not the same as cancelling the gesture. `prefers-reduced-motion` pauses the same way rather than with `animation: none`, which matches `.tick-lane .tk` and `.pk` and leaves the events legible in place instead of collapsing the rail.

### Charts (signature)
Every chart is an area chart in the gold family: faint grid lines at rgba(255,194,102,0.09), glowing line, gradient fill to transparent, emphasized endpoint. Charts animate by appending points (2s cadence), respecting `prefers-reduced-motion`.

## Do's and Don'ts

### Do:
- **Do** keep one full-bleed pane: drill-ins are full-screen overlays over the same page, opened from the element itself.
- **Do** give every live value a mono, tabular, generously sized numeral — the 10ft read is the acceptance test.
- **Do** tint every neutral with the blue-green hue (text, borders, grounds).
- **Do** show data staleness ("last ping Ns") and label all mock data synthetic.
- **Do** theme browser surfaces: selection (gold on abyss), focus-visible (2px gold outline), thin scrollbars in gold-tint.
- **Do** scroll the deck, never the page: pin the ends as `auto` grid rows around a `1fr` scroller.
- **Do** declare a table's columns once on the container and inherit them into every head and row with `grid-template-columns: subgrid`.
- **Do** make a multi-step flow's outcome the app's actual state: the node the wizard brings online is the node the fleet then shows, and the specs it imports are the ones the spec sheet lists. A setup that ends somewhere the rest of the product never mentions is describing a different install.
- **Do** centre a capped single-column view with `margin-inline: auto`, and leave the title bar above it full-bleed — chrome holds one position across every page; only content narrows.

### Don't:
- **Don't** use Tailwind — plain CSS only (user-pinned).
- **Don't** add a sidebar, top navigation, or content routes (user-pinned).
- **Don't** introduce sea imagery — creatures, bubbles, portholes, waves. The world is light and depth only. The one exception is the brand mark: Kraken's glyph is an identity, not decoration, and it appears only in the lockup (header and login) at the wordmark's own scale. A creature anywhere else — an empty state, a loading spinner, a background — is still out.
- **Don't** use pure gray, pure white, or pure black anywhere.
- **Don't** reach for a **dashed** border as decoration. It is a semantic mark here — "this value is real but not yours to change" (a disabled field, an env-managed setting) and "different in kind" (a websocket route, a synthetic-data tag). A dashed *divider* inside a `.cfg-ro` was the reason the token field read as unclean: it said something false one pixel from a border that said it honestly. Structural seams take solid `var(--edge)`, the divider every `.cfg-head` already uses.
- **Don't** borrow `.ns-stack` without adding the control's own un-hide rule. `.ns-w` is `visibility: hidden` by default and only `.copy-act` and the net-state radio pair ever reverse it, so a new control that reuses the stack renders a **blank box** — the token refresh shipped exactly that in its first draft. Copy `.copy-act`'s three lines (`.on` visible, `.did` flips to the off-state) and check the glyph actually paints, since geometry and colour both measure fine on an invisible element.
- **Don't** spend magenta or violet outside true semantics; never color a stopped server crisis-magenta.
- **Don't** pulse in gold to ask for something. Gold motion is the house's word for *alive*, so an errand that blinks in the light makes "this is running" and "attend to me" the same signal. An attention pulse takes the semantic colour, moves light across a fixed silhouette rather than resizing one, settles on hover, and survives `prefers-reduced-motion` as a held lit state — see The Violet Pulse Rule.
- **Don't** ship a meter whose sample band sits entirely inside one zone. `--lvl` drives height *and* color, so a track that never crosses a threshold is a solid block of one color at one height — a slab wearing a dot matrix, and in the worst case the loudest color in the palette spent on a resting state. Check the band, not the stylesheet.
- **Don't** sweep for a bare `button` inside any surface that holds a customizable `select`. With `appearance: base-select` the select owns a **real `<button>` in the DOM**, and it is usually first in document order — so `container.querySelector('button')` returns the select's internal button, and calling `.focus()` on it does nothing at all. The wizard opened with focus stranded on `<body>` for exactly this reason. Ask for one of ours (`.cfg-btn`) or for a field, never for a tag.
- **Don't** hand-write the second half of a ticker. The `-50%` loop is only seamless while both halves are byte-identical and equal in width, so the duplicate must be the same source rendered twice and marked `aria-hidden`. Two literal copies survive exactly until the feed is live.
- **Don't** judge a veil by arithmetic on the backdrop colour. The grounds are already near-black and barely move; what carries the separation is every lit panel and control dimming at once. Open the surface it will really sit over and look at the buttons.
- **Don't** put a property that appears in a `transition` list on the receiving end of an *ancestor's* `:has()` toggle. Chrome never schedules the transition, so the value stays pinned at its old computed value however specific the rule — `!important` does not help. Mark the state with an untransitioned property instead. (A `:has()` on the styled element itself, as the filter chips use, is fine.)
