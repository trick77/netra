import { useState } from "react";
import { Card } from "../../ui/Card";
import { Segmented } from "../../ui/Segmented";
import { applyTheme, loadTheme, type ThemePref } from "../../lib/theme";
import { isRange, RANGES, type Range } from "../../lib/range";
import { DENSITY_KEY } from "../fleet/FleetPage";

/** Density of the fleet overview (spec §4.5). Below the mobile breakpoint
 * cards are automatic and this preference does not apply. */
export type OverviewView = "table" | "cards";

/** The stored default range. It is lib/range's type and deliberately the
 * whole union: this value is handed to whichever page the user opens next,
 * so a default the receiving page has never heard of is exactly the failure
 * one shared type prevents. A page still chooses which options to offer. */
export type RangeKey = Range;

// Same "netra." namespace as lib/theme.ts's key, so one browser's netra
// preferences are greppable in devtools as a group.
// FleetPage owns this key and exports it precisely so this page writes the
// same one. Settings used to write "netra.view" while the fleet page read
// "netra.fleet.density", so choosing Cards here changed nothing at all: the
// overview came back as a table on every reload.
export const VIEW_KEY = DENSITY_KEY;
export const RANGE_KEY = "netra.range";

const VIEWS: OverviewView[] = ["table", "cards"];
const RANGE_LABELS: Record<RangeKey, string> = {
  "1h": "1 h",
  "6h": "6 h",
  "24h": "24 h",
  "7d": "7 d",
  "30d": "30 d",
};

/**
 * Reads the stored preference, falling back to the default for anything this
 * build does not recognise. A value written by an older or newer build is not
 * an error worth surfacing -- it is a preference, and the default is a
 * perfectly good answer.
 */
// Every read is guarded: localStorage throws outright in Safari with
// cookies blocked and in some private modes, and these run inside useState
// initialisers -- an unguarded throw there renders nothing at all. Losing a
// preference is acceptable; losing the page is not.
function stored(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function loadView(): OverviewView {
  const v = stored(VIEW_KEY);
  return VIEWS.includes(v as OverviewView) ? (v as OverviewView) : "table";
}

export function loadRange(): RangeKey {
  // isRange, not a membership test on a local list: the stored value comes
  // from a place the user can edit, and an unrecognised one must fall back
  // rather than reach a page as a range nothing can resolve.
  const value = stored(RANGE_KEY);
  return isRange(value) ? value : "24h";
}

const THEME_OPTIONS: { value: ThemePref; label: string }[] = [
  { value: "light", label: "Light" },
  { value: "dark", label: "Dark" },
  { value: "system", label: "System" },
];

/** A labelled block. A real <fieldset>/<legend> rather than a div plus an
 * aria-label: it is the one grouping construct assistive technology already
 * announces, and it names the Segmented inside it for free. */
function Setting({
  legend,
  hint,
  children,
}: {
  legend: string;
  hint: string;
  children: React.ReactNode;
}) {
  return (
    <fieldset className="setting">
      <legend>{legend}</legend>
      {children}
      <p className="hint">{hint}</p>
    </fieldset>
  );
}

/**
 * Appearance and defaults (spec §9). Everything here is per-browser: none of
 * it reaches the hub, so nothing on this page needs saving or can fail.
 */
export function SettingsPage() {
  const [theme, setTheme] = useState<ThemePref>(loadTheme);
  const [view, setView] = useState<OverviewView>(loadView);
  const [range, setRange] = useState<RangeKey>(loadRange);

  // applyTheme both stamps the root and stores the choice -- including the
  // "system" case, where the stamp is REMOVED so prefers-color-scheme decides
  // live. Never reimplement that here; see lib/theme.ts.
  function chooseTheme(next: ThemePref) {
    applyTheme(next);
    setTheme(next);
  }

  // The write is guarded for the same reason the read is: a store that
  // refuses to save costs the preference, never the click.
  function remember(key: string, value: string) {
    try {
      localStorage.setItem(key, value);
    } catch {
      // See stored(): an unavailable store is a lost preference, not a
      // broken page.
    }
  }

  function chooseView(next: OverviewView) {
    remember(VIEW_KEY, next);
    setView(next);
  }

  function chooseRange(next: RangeKey) {
    remember(RANGE_KEY, next);
    setRange(next);
  }

  return (
    <div className="section">
      <h2>Settings</h2>

      <Card title="Appearance">
        <Setting
          legend="Theme"
          hint="System follows the operating system as it changes, without a reload."
        >
          <Segmented
            options={THEME_OPTIONS}
            value={theme}
            onChange={chooseTheme}
          />
        </Setting>
      </Card>

      <Card title="Defaults">
        <Setting
          legend="Default overview view"
          hint="What the fleet overview opens as. Narrow screens always use cards."
        >
          <Segmented
            options={[
              { value: "table", label: "Table" },
              { value: "cards", label: "Cards" },
            ]}
            value={view}
            onChange={chooseView}
          />
        </Setting>

        <Setting
          legend="Default time range"
          hint="A link carrying its own range wins over this; the range lives in the URL."
        >
          <Segmented
            options={RANGES.map((r) => ({ value: r, label: RANGE_LABELS[r] }))}
            value={range}
            onChange={chooseRange}
          />
        </Setting>
      </Card>

      <p className="note">
        These settings are stored in this browser only. They are not sent to the
        hub, so another browser — or the same one after clearing site data —
        starts from the defaults.
      </p>
    </div>
  );
}
