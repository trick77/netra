import { useState } from "react";
import { Card } from "../../ui/Card";
import { Segmented } from "../../ui/Segmented";
import { isRange, PAGE_RANGES, type Range } from "../../lib/range";
import { RANGE_KEY, readPref, writePref } from "../../lib/prefs";
import { Settings2 } from "lucide-react";

/** The stored default range. It is lib/range's type and deliberately the
 * whole union: this value is handed to whichever page the user opens next,
 * so a default the receiving page has never heard of is exactly the failure
 * one shared type prevents. A page still chooses which options to offer. */
export type RangeKey = Range;

// lib/prefs owns the key now -- one "netra." namespace, greppable in devtools
// as a group -- and the name is re-exported because this page's tests and
// App.tsx import it from here.
export { RANGE_KEY };

// Only the page-scale ranges: PAGE_RANGES is what this control offers, and
// a label for a window no page can be set to would be dead weight.
const RANGE_LABELS: Partial<Record<RangeKey, string>> = {
  "1h": "1 h",
  "6h": "6 h",
  "12h": "12 h",
  "24h": "24 h",
  "7d": "7 d",
  "30d": "30 d",
};

/**
 * Reads the stored preference, falling back to the default for anything this
 * build does not recognise. A value written by an older or newer build is not
 * an error worth surfacing -- it is a preference, and the default is a
 * perfectly good answer.
 *
 * readPref is the guarded read (lib/prefs): localStorage throws outright in
 * Safari with cookies blocked, and these run inside useState initialisers.
 */
export function loadRange(): RangeKey {
  // isRange, not a membership test on a local list: the stored value comes
  // from a place the user can edit, and an unrecognised one must fall back
  // rather than reach a page as a range nothing can resolve.
  const value = readPref(RANGE_KEY);
  return isRange(value) ? value : "24h";
}

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
  const [range, setRange] = useState<RangeKey>(loadRange);

  function chooseRange(next: RangeKey) {
    // writePref is guarded for the same reason readPref is: a store that
    // refuses to save costs the preference, never the click.
    writePref(RANGE_KEY, next);
    setRange(next);
  }

  return (
    <>
      {/* A heading ROW, with the cards as its siblings -- see the same note
          in HostAdminPage: `.section` is a baseline flex line, so a card
          nested inside it is laid out beside the title, not under it. */}
      <div className="section">
        <span className="pageicon">
          <Settings2 aria-hidden="true" />
        </span>
        <h2>Settings</h2>
      </div>

      {/* Appearance held one control, Theme, and netra has one theme now --
          so the card went with it rather than becoming an empty box titled
          after a choice nobody has. */}
      <Card title="Defaults">
        <Setting
          legend="Default time range"
          hint="A link carrying its own range wins over this; the range lives in the URL."
        >
          <Segmented
            options={PAGE_RANGES.map((r) => ({
              value: r,
              label: RANGE_LABELS[r] ?? r,
            }))}
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
    </>
  );
}
