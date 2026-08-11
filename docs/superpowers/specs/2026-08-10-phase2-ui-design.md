# netra — Phase 2 Web UI Design Specification

**Date:** 2026-08-10
**Status:** Approved for planning
**Phase:** 2 (Stage 2, "Web UI")
**Supersedes:** the three-line Web UI bullet in `2026-08-07-next-phases.md` §Stage 2

---

## 1. Why this exists

Phase 1 shipped agents, ingest, the full schema and a token-gated read API. It shipped no
UI beyond a minimal `html/template` host-management page, and `ui.go` says so explicitly:
*"no JavaScript, no build step and no design system. The phase-2 UI owns those decisions."*
This spec makes them.

The stated goal is **cohesion**: one button style, one input style, one chart component —
not a new treatment per page. Every decision below exists to make the *cheap* path also
the consistent one.

### Scope

In: the design system, the fleet overview, host detail and its tabs, the events page,
settings, host management, login, and the anchors the Stage 3 LLM feature will attach to.

Out: the alerting engine (Stage 2, separate), OIDC itself (Stage 2, separate), geocoding
and the map (Stage 2), LLM diagnostics (Stage 3). This spec designs *around* them so they
attach without redesign.

---

## 2. Stack

Mirrors `../music`, which matches netra's deployment shape — one static binary, one
container, no asset deploy.

| Choice | Value |
|---|---|
| Framework | React 19 + TypeScript, Vite |
| Location | `ui/`, built to `internal/hub/web/dist` |
| Embedding | `//go:embed` in `internal/hub/web/embed.go`, served with `public, max-age=31536000, immutable` |
| Styling | Tailwind v4 `@theme` block as a CSS-variable registry + hand-written class layer. **No utility classes in components** — this is how music works and it is deliberate |
| Data | plain `fetch` against the existing JSON read API. No data-fetching library |
| Routing | hand-rolled, as music's `src/router.ts` |
| Charts | hand-written inline SVG. No charting library |
| Icons | `lucide-react`, wrapped in one `Icon` component |
| Tests | Vitest + Testing Library |

**Why no chart library.** Every chart in this spec is one of four shapes (sparkline,
stacked sparkline, up/down sparkline, small-multiple line/area). Hand-written SVG is a few
hundred lines total, has no bundle cost, themes from CSS variables for free, and cannot
drift from the design system. A library would be more code, not less.

---

## 3. Design system

### 3.1 Fonts

music's licensed Anthropic Sans and Anthropic Serif, self-hosted, same `@font-face`
mechanics and the same `--font-sans` / `--font-serif` variable names. Serif is reserved
for page and section titles; sans is everything else; `--font-mono` for versions,
addresses, commands and identifiers.

### 3.2 Palette — direction "Clay"

music's tokens ported. Dark values are music's verbatim; the light peer is derived, since
music has no light theme.

```css
/* dark */
--bg:#1f1f1e; --surface:#1b1b1a; --surface-2:#2c2c2a; --raised:#363632;
--border:#323230; --border-strong:#454540;
--ink:#faf9f5; --ink-2:#c9c6bd; --muted:#9c9a92;
--accent:#d97757; --accent-fill:#c25f34; --accent-soft:#2e211a;
--grid:#2a2a28; --axis:#454540;

/* light */
--bg:#f7f5f0; --surface:#fffdf9; --surface-2:#faf8f3; --raised:#fffdf9;
--border:#e7e2d8; --border-strong:#cfc9bb;
--ink:#1f1e1a; --ink-2:#4d4a42; --muted:#807c72;
--accent:#b4531f; --accent-fill:#c25f34; --accent-soft:#fbeee5;
--grid:#eeeae1; --axis:#cfc9bb;
```

### 3.3 The accent/critical rule — load-bearing

The clay accent `#c25f34` and the critical red `#d03b3b` measure **ΔE 7.2 at normal
vision and 2.2 under deuteranopia**. They are not reliably distinguishable, and no
re-stepping fixes it — pushing critical all the way to pink reaches only ΔE 13.5, still
under the 15 floor. This was measured, not estimated.

Therefore, two rules, both mandatory:

1. **Severity is never carried by colour alone.** Every status wears a dot *and* a word
   ("unhealthy", "96 %", "silent"). This is what preserves meaning when accent and
   critical read as the same hue.
2. **The accent never appears as a data or severity fill.** Meters, chart bands, chart
   lines and status marks draw only from the series palette and the status palette. Clay
   is chrome: brand, links, active nav, focus ring, primary button.

### 3.4 Series palette — validated, orange-free

| Slot | Light | Dark | Used for |
|---|---|---|---|
| 1 | `#2a78d6` | `#3987e5` | cpu_user, mem_used, rx |
| 2 | `#1baf7a` | `#199e70` | cpu_system, buffers, tx |
| 3 | `#4a3aa7` | `#9085e9` | cpu_iowait, cached |
| 4 | `#e87ba4` | `#d55181` | cpu_steal, ZFS ARC |

This order clears every colourblindness gate in both themes on the adjacent pairlist
(stacked areas, lines). **Cap: three series in any all-pairs form** (scatter, unordered
marks) — four does not clear the floors.

Orange never appears in a chart, which is also what keeps the clay accent from
impersonating data.

### 3.5 Status palette — fixed, never themed, never a series

`good #0ca30c` · `warning #fab219` · `serious #ec835a` · `critical #d03b3b`

### 3.6 Type scale — role → size, mandatory

The previous scale had six steps and no mapping, and 11 px drifted into content. Fixed:

| Role | Token | Size | Used for |
|---|---|---|---|
| Display | `--text-display` | 28px | host name on detail, hero figure in a stat tile. One per view |
| Title | `--text-title` | 20px | section headings, brand. Serif |
| Body | `--text-body` | 16px | running prose only |
| UI | `--text-ui` | 15px | **default**. buttons, inputs, primary cell values, host names |
| Label | `--text-label` | 14px | secondary content you still read: site, mount, timestamps, hints, table headers, notes |
| Micro | `--text-micro` | 12px | **three uses only**: badges/pills, chart axis ticks, uppercase eyebrow labels |

**12 px is the floor** at any nesting depth. The test: *if you expect the reader to read
it, it is Label or larger.* A role not in this table gets no size of its own.

### 3.7 Other tokens

Spacing `4/8/12/16/24/32`. Radii `--radius-sm 5px`, `--radius-ui 8px`, `--radius-pill`.
Weight is a claim about importance and must be earned: peer values get identical
treatment and are distinguished non-typographically (see traffic, §4.2).

### 3.8 Primitives — the whole inventory

`Button` (primary · secondary · ghost · danger) · `Input`/`Select` (one `.ctl` class) ·
`Segmented` · `Card` · `Table` · `Tabs` · `Badge`/`StatusDot` · `Meter` · `StatTile` ·
`Sparkline` · `StackedSparkline` · `UpDownSparkline` · `ChartPanel` · `Drawer` ·
`AttentionBand` · `EmptyState`.

**Nothing outside this list gets bespoke styling.** A new visual need is a new primitive
with a name, or it is one of these.

---

## 4. Fleet overview

### 4.1 Structure, top to bottom

1. **Attention band** — present only when something is wrong (§4.3)
2. **Stat tiles** — hosts reporting, containers, fleet traffic
3. **Entity tabs** — `Hosts` / `Containers` (what you are looking at)
4. **Toolbar** — filter, scope select, time range, view toggle (how densely)
5. **The list** — table or cards
6. **All-hosts overlay** — two shared-axis charts (§4.4)

### 4.2 The host row

Columns: **Host · CPU · Memory · Traffic · Disk · Uptime**.

| Column | Mark | Rationale |
|---|---|---|
| Host | status dot + name + site | site is Label, not Micro |
| CPU | **stacked sparkline** — user/system/iowait/steal | the silhouette is `cpu_total`; the bands say what kind of busy. 60 % of iowait is a disk problem; 60 % of steal is the neighbour |
| Memory | **stacked sparkline** — used/buffers/cached/ZFS ARC | free is the gap to the ceiling, **never a band** — stacking free makes every host look full. ARC as its own band stops ZFS hosts reading as alarming |
| Traffic | **up/down sparkline** — rx above zero, tx below | asymmetry is visible as a shape. Both rates in **identical** type; only the arrow distinguishes them |
| Disk | **meter** + mount name + `+N` | usage is near-flat over 24 h, so a sparkline carries nothing; "how close to full" is the question |
| Uptime | value, coloured below 300 s | a host that rebooted 4 minutes ago is the most interesting row on the page |

**Sparklines are non-negotiable.** Beszel ships none and Grafana's fleet dashboard uses
bar-gauges; both were considered and rejected. A bar shows one instant, a sparkline shows
the last 24 hours in the same 112 px, and recent history is half of what an overview is
for. Disk is the single exception, for the reason above.

**Disk shows the fullest filesystem, named.** Summing is wrong — a 503 GB root at 68 %
and a 7.8 TB array at 88 % average to a number that would hide a root at 99 %. Root-only
is worse. The percentage is `used / (used + free)` — `df`'s `Use%` — so it agrees with
what an operator sees over SSH. The API deliberately computes no percentage (`used + free
≠ total`, the gap is the root reserve), so the UI owns this definition and states it in
the column-header tooltip.

Because a stacked sparkline puts four series in 112 px, **the hover tooltip carrying band
names and values is required**, not optional.

### 4.3 The attention band

- **Absent when nothing is wrong.** Not a green "all clear" card — a permanently present
  banner is one people stop reading. Replaced by one quiet line: *"All 19 hosts reporting
  · nothing needs attention · checked 41 s ago"*, which still confirms the check ran.
- **No dismiss, no acknowledge.** It clears when the condition clears. Dismissal would let
  the UI show all-clear while something is broken — the one lie a monitoring tool must not
  tell. Acknowledgement belongs to the alerting engine.
- **Sorted by each host's worst condition**, not by condition count.
- **Grouped per host past two conditions** — worst shown, `+N more` expandable. A
  cascading host (disk full → services failing → load climbing) otherwise floods the band
  and buries a second host that is quietly starting to fail. **This is presentation, not
  suppression: everything is still recorded.**
- **Header counts both**: "6 on 3 hosts" — conditions alone hide concentration, hosts
  alone hide severity.
- **Truncation is always stated** ("+4 more hosts →"). Silent truncation reads as
  completeness.
- Each row links to the host and carries the `✦ Explain` anchor (§8).

### 4.4 The all-hosts overlay

Two charts below the list — CPU and memory, every host on shared axes, de-emphasised, only
the outlier labelled. The column ranks who is highest *now*; the overlay shows who is
behaving *differently*. Grafana's fleet dashboard does exactly this, and it is the one
structural idea from the research that netra was missing.

### 4.5 Views

Two independent axes:

- **Entity** (tabs): `Hosts` / `Containers`. The **container overview** is fleet-wide — the
  same container row plus a Host column, so "every `postgres` in the fleet" and "everything
  on this host" are one component, and both link to container detail (§5.3).
- **Density** (toolbar toggle): `Table` / `Cards`, remembered per browser, defaulted in
  Settings. **Both render from one column definition** (Beszel's approach) — a row and a
  card from the same source, so they cannot drift. Hidden in Containers view; a card grid
  of 247 containers is not useful.

Below the mobile breakpoint, **Cards is automatic**, not a preference — a six-column table
does not survive 390 px.

---

## 5. Host detail

### 5.1 Shell

A header that never changes across tabs — hostname (Display, serif), site · OS · kernel ·
arch, status badge, time range, actions — then the tab bar:

`Overview · Graphs · Containers · Filesystems · Network · Packages · Units · Events`

The **Alerts** tab joins this bar when the Stage 2 alerting engine lands (§6); phase 2 ships
the other eight. The bar is built to take a ninth without relayout.

- **Every tab is a URL** (`/hosts/{id}/graphs`). Reload keeps its place; the diagnosis
  drawer's evidence links deep-link into the tab holding the data.
- **The time range is shared state** across tabs.
- Tabs **wrap to a second line** at narrow widths. No horizontal scroll, no "More ▾" — a
  hidden tab is an unused tab.
- **Overview is a summary, not a duplicate**: two or three facts from each tab plus the
  "Needs attention" card. It must not grow until the tabs are pointless.

### 5.2 Tabs

**Overview** — processor (stacked area, full legend), memory (meter + legend, swap shown
as "none" when absent, never 0), disk (per-filesystem rows with absolute bytes, not
ratios), system facts, temperature, collector capabilities, needs-attention.

**Graphs** — small multiples: one `ChartPanel` component, N instances, uniform size, one
range control driving all of them, grouped `System` / `Network` / `Storage`.
System: device availability, uptime, load averages, context switches, interrupts, running
processes, users logged in, total processes.
Network: TCP statistics, TCP connections, IP fragmentation, UDP statistics.
Adding a family later is data, not design.

**Containers · Filesystems · Network · Packages · Units** — the *inventory family*: one
searchable-list component, differing only in columns, each showing `first_seen`/`last_seen`
where the schema has them. Packages' "changed in the last 30 days" filter is the
"what changed before this broke" timeline.

Containers specifically: identity is **compose project + service**, never the container ID
(which changes on every `compose up -d` and would orphan all history). Memory is the
cgroup figure with `cache` and `inactive_file` subtracted — it reads lower than
`docker stats`, and that is correct. The bar is against the container's limit; a container
with no limit shows "no limit", never the host total.

**Alerts** — rules applying to this host (inherited or overridden, muted state) plus
firing/resolved history. *What is currently firing also appears on Overview*, because
current state must not sit behind a tab.

**Events** — this host's slice of the events log.


### 5.3 Container detail

Containers are the only sub-entity with a time series of their own, so they get a page
rather than a row expansion. Reached from either container list (fleet or host).

Header: `project/service` (Display), host as a link, image, status badge, time range.
Then **four small multiples from the same `ChartPanel` component the Graphs tab uses** —
CPU, memory against `mem_limit`, network (rx/tx), disk I/O (read/write) — because a
container is just another entity with a time series and deserves no bespoke chart.

Then two cards: **Identity** (`container_key`, `name`, `image`, host, `is_agent`, last
sample) and **Not collected** (§11).

A container with no `mem_limit` shows "no limit" rather than a bar against the host total.

---

## 6. Events, and what an alert is

**An event is an instant; an alert is an interval.** This distinction drives the model.

If alerts are just events, "what is wrong right now" means scanning backwards pairing
`fired` with `resolved` — and a single lost resolve event (dropped batch, agent restart)
produces a permanent phantom alarm with no way to clear it.

The model, when the Stage 2 engine lands:

1. `alert_rules` — configuration
2. `alerts` — instances with state: `rule_id`, `host_id`, `state`, `started_at`,
   `resolved_at`, `last_seen`. One indexed query answers "firing now"
3. transitions written into the existing `events` table, so the timeline stays unified

Alerts and Events are then two projections of one history: Events is the log, Alerts is the
current-state index.

**Sequencing: phase 2 ships Events only.** netra has no alerting engine today; threshold
crossings go straight to `events`, so the attention band reads from events for now. The
Alerts pages land with the engine that defines them. The band's contract does not change
when its source does — it only gets more reliable.

**The Events page** carries search, host, type, severity and time-range filters. Type is a
chip, not a colour: only critical and warning carry a status dot. A package upgrade is not
a colour-coded emergency.

---

## 7. Correctness rules the UI must honour

These come from the read API's contract. Each is cheap to implement and expensive to
retrofit.

1. **Gaps render as gaps.** A missing point inside `window` means *the host reported
   nothing*. Charts must break the line — never interpolate across a hole. Interpolation
   converts "agent was down" into "CPU was steady", which is the exact failure netra
   exists to prevent. Applies to sparklines too.
2. **Show the window you got, not the one you asked for.** `window` and `requested_window`
   differ at the leading edge (retention) and the trailing edge (materialisation lag: the
   5m tier is fresh only to `now−10m`, the 1h tier to `now−1h`). Ask 90 days of `process`
   and you get 48 hours. The range control states the actual coverage, or people will read
   retention as data loss.
3. **Read `tier` and `columns`; never index points positionally without them.** Column
   names differ per tier by construction (`busy` → `busy_avg`/`busy_max`), and
   `family=collector` returns a bool and a string beside numbers.
4. **`?columns=` is tier-specific** — pin `step`, or read `columns` off an unfiltered
   response first.
5. **Absent ≠ zero.** `NULL` swap means no swap; render "none". `swap_used = 0` means swap
   exists and is unused.
6. **Unavailable collectors say so.** A capability that is off renders an explicit
   "not collected" panel with the reason — never an empty chart. Currently applies to
   IP statistics, ICMP statistics and ICMP informational (§11).
7. **Polling is 60 s**, aligned to the scrape interval, paused when the tab is hidden.
   Sub-minute realtime is a permanent non-goal; faster polling is pure waste.

---

## 8. LLM anchors (Stage 3, designed for now)

Phase 2 builds **no LLM feature**. It builds the two anchors so Stage 3 attaches without
redesign:

- an event/attention row that can carry an action
- a chart that can report a brushed time range

The Stage 3 shape, specified here so those anchors are the right ones:

- **Anchored, not floating.** `✦ Explain` on an event, and brush-a-range on a chart. A
  chat box makes the user supply the context they needed the answer to find.
- **Tool-calling over the read API**, not prompt-stuffing. Stage 1D already shipped the
  tool surface; the model fetches what it needs and the call log comes free.
- **The call log is the trust mechanism.** Every answer ends in the requests it made,
  each clickable. Prose without provenance is worse than nothing at 3 a.m.
- **"What I could not check" is a required section** — the *never silently degrade*
  promise applied to the reasoning layer.
- **Generated content never sits inline with facts** — own surface, dashed-outline
  affordance, always labelled.
- **It must not become a reason to collect more.** The argv guard stands; the LLM feature
  does not get to relax it.
- Explicit opt-in, visible indicator, configurable base URL so a local model is
  first-class. Reuse music's `internal/llm` (stdlib-only, OpenAI-compatible, tool-calling;
  non-streaming) and `studio/loop.go`'s `runResearch`/`runTurn` pair.

---

## 9. Cross-cutting

| Concern | Decision |
|---|---|
| **Theme** | Light / Dark / System in Settings → Appearance. System follows the OS live |
| **Auth** | This spec designs the login page and the authenticated shell; the OIDC integration itself is separate Stage 2 work. OIDC replaces the admin-token→session-cookie exchange. Rotating `NETRA_ADMIN_TOKEN` invalidating every session must survive the transition |
| **Host management** | Phase 1's create/rotate/delete plus the token-shown-once and the paste-ready `setup-agent.sh` line are absorbed into this UI, not left as a separate page |
| **Empty state** | A fresh hub has zero hosts; that screen *is* the onboarding, and leads directly into host creation |
| **Units** | Network in bits/s (`Mb/s`, `Gb/s`); storage and memory in bytes (`GB`, `TB`, decimal). One formatter module, no ad-hoc call sites |
| **Timestamps** | Browser-local by default, with the site's timezone (`sites.timezone`) shown alongside on host detail. Durations relative ("2 h 14 m"), absolute on hover |
| **URL state** | Time range, filters and selected tab live in the URL — links are shareable and reload-safe |
| **Mobile** | Cards automatic below the breakpoint; tabs wrap; no horizontal page scroll |
| **Agent version** | Column with a drift arrow — grey for update available, red for security. A version-change event already exists |
| **Language** | English only. No i18n layer |
| **Accessibility** | Focus ring on every control, `/` focuses filter, tab order follows reading order, no colour-only meaning anywhere |

---

## 10. Data coverage

**Included in phase 2 beyond what the pages above imply:**

- **Agent self-telemetry** (`agent_samples`) — `buffer_depth`, `buffer_dropped_total`,
  `post_failures_total`, `post_latency_ms`. `buffer_dropped_total > 0` means **data has
  been lost**; that is arguably the most important number netra produces and it currently
  surfaces nowhere. Shown on host detail and raised into the attention band when non-zero.
- **Disk `await`** (`r_await_ms`, `w_await_ms`, `io_util_pct`) — how you tell a busy disk
  from a failing one.
- **Filesystem inodes** (`inodes_total`, `inodes_used`) — inode exhaustion presents as
  "disk full" with free space showing, and nothing else in the UI would explain it.

**Collected, deliberately deferred** (listed so they are deferred rather than forgotten):
per-core CPU as a heatmap, SMART attribute values (only threshold events surface today),
`forks_per_s`, `boot_time_s`, top-N processes, per-interface breakdowns beyond the Network
tab, `host_addresses` subnet queries, sites/providers management, the geo map, and global
search across hosts/containers/packages.

---

## 11. Known gaps this UI exposes

Three requested graph families have **no data behind them**. The UI renders them as
explicit "not collected" panels until they land:

| Family | Status |
|---|---|
| TCP statistics | ✅ present |
| IP fragmentation | ✅ present (`ip_frag_*`, `ip_reasm_*`, + IPv6 mirror) |
| **IP statistics** | ❌ only fragmentation/reassembly exist; `InReceives`/`InDelivers`/`OutRequests` are never parsed |
| **ICMP statistics** | ❌ no ICMP columns anywhere in `0001_init.sql` |
| **ICMP informational** | ❌ same gap |

Closing them is agent work (`/proc/net/snmp` already contains the `Icmp` and `Ip` rows —
more parsing, no new privileges) plus schema columns plus the three tier views. Two
cautions: schema edits go into `0001_init.sql`, and the migration runner matches by
filename with **no checksum**, so an edited `0001` is silently skipped on an existing DB.

**Container health, restart count, state and labels are also absent.** Spec §6.2 says the
agent reads health and labels from the Docker socket, but neither reaches the hub:
`ContainerSample` on the wire carries `container_key`, `name`, `image`, `is_agent` and the
six metrics, and the `containers` table stores only the first four. So the UI must not
render a health badge or a restart count.

Until that lands, container state is **derived from what is collected**, and labelled as
such: a container that stops appearing in samples is silent, memory approaching
`mem_limit` is a warning, and a gap in its series is a restart. The badge says what was
measured, never what was inferred. Adding real health is a proto field, a schema column
and an ingest path — worth doing, but it is agent work, not UI work.

---

## 12. Testing

- **Unit** (Vitest): the formatter module (units, durations, tier-aware column lookup);
  the chart geometry functions; tier/window clamping display logic.
- **Component**: each primitive renders in both themes; the attention band is absent when
  empty; a metrics response with a hole renders a broken line, not an interpolated one; a
  `NULL` swap renders "none", not 0.
- **Contract**: fixtures captured from the real read API, including the tier-specific
  column names, so a schema change that renames a column fails a UI test.
- **Coverage**: `ui/` joins `hack/coverage-floors` at the same discipline as the Go
  packages.

---

## 13. Open items

None blocking. Deliberately deferred to their owning stage: alerting engine vocabulary
(§6), OIDC mechanics (§9), the map, and the LLM feature itself (§8).
