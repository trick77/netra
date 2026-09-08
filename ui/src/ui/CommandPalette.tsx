import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Box, Search, Server } from "lucide-react";
import {
  getFleetContainers,
  getHosts,
  type Container,
  type Host,
} from "../lib/api";
import { routePath } from "../lib/router";

/**
 * The bar's search: one control that opens a palette over hosts and
 * containers.
 *
 * Two things a fleet page cannot do. It lists hosts OR containers, never
 * both, so finding a container means knowing which host runs it first; and
 * its filter only reaches the page you are already on, which on a host's
 * Sensors tab is nothing at all. This reaches the whole inventory from
 * anywhere, which is why it lives in the bar rather than in a toolbar.
 *
 * The control is a BUTTON drawn as a field, not a field. A real input would
 * have to either search in place -- a second results surface in a 52px bar --
 * or open this on focus, which traps a Tab: focus enters, the dialog steals
 * it, and the next Tab is inside a thing the reader never asked to open.
 * Clicking it opens the palette and the palette's own input takes the caret,
 * so typing straight after the click still works, which is the only thing
 * the field shape was promising.
 */
export function BarSearch({ go }: { go: (to: string) => void }) {
  const [open, setOpen] = useState(false);
  const buttonRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    // Cmd+K on a Mac, Ctrl+K elsewhere -- and both are accepted on both, so
    // a reader who learned the other one still gets the palette. "/" was not
    // available: the fleet page already binds it to its own filter box
    // (useSlashToFocus), and one key that means two different things
    // depending on the page is worse than no shortcut.
    //
    // No "is a field focused" guard, unlike "/": a modifier chord cannot be
    // typed into a text field by accident, and someone half way through
    // typing a filter is exactly who wants to jump to a host by name.
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "k" && e.key !== "K") return;
      if (!e.metaKey && !e.ctrlKey) return;
      e.preventDefault();
      setOpen(true);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  const close = useCallback(() => {
    setOpen(false);
    // Back to the control that opened it. Without this focus lands on
    // document.body and the next Tab starts from the top of the page --
    // the same return ChartDetail makes for the same reason.
    buttonRef.current?.focus();
  }, []);

  return (
    <>
      <button
        type="button"
        ref={buttonRef}
        className="searchbox"
        onClick={() => setOpen(true)}
      >
        <Search aria-hidden="true" />
        <span className="searchbox-label">Search hosts and containers</span>
        {/* Decoration: the shortcut is announced by the button's own name
            instead, because a screen reader spelling out "command K" as a
            second label after the words reads as two controls. */}
        <span className="kbd" aria-hidden="true">
          {shortcutHint()}
        </span>
      </button>
      {open && <Palette go={go} onClose={close} />}
    </>
  );
}

/** ⌘K where that is the chord people know, Ctrl K everywhere else. */
function shortcutHint(): string {
  // userAgent rather than platform: platform is deprecated and already
  // frozen in some browsers, and this only decides which of two hints to
  // draw -- both chords work either way, so a wrong guess costs a hint.
  const ua = typeof navigator === "undefined" ? "" : navigator.userAgent;
  return /Mac|iPhone|iPad/.test(ua) ? "⌘K" : "Ctrl K";
}

/** One thing you can jump to: a host, or a container on one. */
type Entry = {
  id: string;
  kind: "host" | "container";
  /** What the row is called, and what the query matches on. */
  name: string;
  /** The host a container runs on. Never set for a host. */
  under?: string;
  href: string;
};

/** How many of each kind a query is allowed to put on screen. A palette that
 *  answers with sixty rows has not answered; the count line below says how
 *  many were left out so nobody reads a short list as the whole truth. */
const PER_KIND = 8;

function Palette({
  go,
  onClose,
}: {
  go: (to: string) => void;
  onClose: () => void;
}) {
  const [query, setQuery] = useState("");
  const [entries, setEntries] = useState<Entry[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [active, setActive] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    // Fetched when the palette opens, not when the app mounts: this is the
    // whole inventory, and most sessions never open it. Nothing polls it --
    // a palette is read in the seconds it is open, and it is refetched the
    // next time it opens.
    let live = true;
    void (async () => {
      try {
        const hosts = await getHosts();
        if (!live) return;
        setEntries(hostEntries(hosts));
        // Containers are a second request, and a failing one must not cost
        // the hosts that already arrived: a reader who opened this to reach
        // db-011 gets db-011 even when /api/v1/containers is down.
        try {
          const byHost = await getFleetContainers(hosts.map((h) => h.id));
          if (!live) return;
          setEntries([
            ...hostEntries(hosts),
            ...containerEntries(hosts, byHost),
          ]);
        } catch {
          setFailed(true);
        }
      } catch {
        if (live) setFailed(true);
      }
    })();
    return () => {
      live = false;
    };
  }, []);

  const shown = useMemo(() => matches(entries ?? [], query), [entries, query]);
  const hosts = shown.rows.filter((e) => e.kind === "host");
  const containers = shown.rows.filter((e) => e.kind === "container");

  // A new query re-ranks the list under the cursor, so the cursor goes back
  // to the top rather than staying on whatever row happens to be at index 3.
  useEffect(() => setActive(0), [query]);

  const move = (delta: number) => {
    if (shown.rows.length === 0) return;
    const next = (active + delta + shown.rows.length) % shown.rows.length;
    setActive(next);
    // Arrowing past the fold has to bring the row with it -- sixteen rows in
    // a 70vh panel scroll. Optional call because jsdom has an Element with
    // no scrollIntoView on it, and a keyboard test must not die on layout.
    const row = listRef.current?.querySelector(`#${OPTION_ID}${next}`);
    row?.scrollIntoView?.({ block: "nearest" });
  };

  // Escape on the document, not on the panel: the rows are out of the tab
  // order (see Group), so a Tab from the field leaves .pal at once and a
  // handler bound to the panel would be dead the moment it did. The chart
  // dialog binds it in the same place for the same reason. The arrows and
  // Enter stay on the panel -- they only mean anything with the field
  // focused.
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCloseRef.current();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      move(1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      move(-1);
    } else if (e.key === "Enter") {
      const row = shown.rows[active];
      if (!row) return;
      e.preventDefault();
      go(row.href);
      onClose();
    }
  };

  return (
    // The backdrop closes on a click and the panel stops the click inside
    // itself, exactly as the chart dialog does -- and it dims the bar too,
    // which sits three stacking levels below this one.
    <div className="pal-back" onClick={onClose}>
      <div
        className="pal"
        role="dialog"
        aria-modal="true"
        aria-label="Search hosts and containers"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={onKeyDown}
      >
        <div className="pal-field">
          <Search aria-hidden="true" />
          <input
            className="ctl"
            type="text"
            autoFocus
            autoComplete="off"
            spellCheck={false}
            placeholder="Search hosts and containers"
            aria-label="Search hosts and containers"
            role="combobox"
            aria-expanded="true"
            aria-controls="pal-list"
            aria-activedescendant={
              shown.rows.length === 0 ? undefined : `${OPTION_ID}${active}`
            }
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>
        <div className="pal-list" ref={listRef}>
          <div id="pal-list" role="listbox" aria-label="Results">
            {hosts.length > 0 && (
              <Group
                label="Hosts"
                rows={hosts}
                all={shown.rows}
                active={active}
                onPick={(row) => {
                  go(row.href);
                  onClose();
                }}
                onHover={setActive}
              />
            )}
            {containers.length > 0 && (
              <Group
                label="Containers"
                rows={containers}
                all={shown.rows}
                active={active}
                onPick={(row) => {
                  go(row.href);
                  onClose();
                }}
                onHover={setActive}
              />
            )}
          </div>
          {/* Three states that are not a list, in the order they can happen:
              nothing fetched yet, the fetch failed, the fetch worked and the
              query matched nothing. Only the last of the three is about what
              was typed. */}
          {entries === null && !failed && (
            <p className="pal-note" role="status">
              Loading…
            </p>
          )}
          {failed && entries === null && (
            <p className="pal-note" role="status">
              Could not load the inventory.
            </p>
          )}
          {entries !== null && shown.rows.length === 0 && (
            <p className="pal-note" role="status">
              {query === "" ? "Nothing to show." : `No match for “${query}”.`}
            </p>
          )}
        </div>
        {/* Outside the scroller, pinned to the bottom edge: a list that was
            cut short says so where the reader is looking, not on a line they
            have to scroll past eight rows to find. */}
        {shown.hidden > 0 && (
          <p className="pal-foot">
            {shown.hidden} more {shown.hidden === 1 ? "match" : "matches"}. Keep
            typing.
          </p>
        )}
      </div>
    </div>
  );
}

const OPTION_ID = "pal-opt-";

function Group({
  label,
  rows,
  all,
  active,
  onPick,
  onHover,
}: {
  label: string;
  rows: readonly Entry[];
  all: readonly Entry[];
  active: number;
  onPick: (row: Entry) => void;
  onHover: (index: number) => void;
}) {
  return (
    <>
      <div className="pal-group" role="presentation">
        {label}
      </div>
      {rows.map((row) => {
        const index = all.indexOf(row);
        return (
          // A real anchor, so a row can be opened in a new tab the way every
          // other link in the app can. The plain click routes here rather
          // than through the app's delegation, so the palette closes and
          // navigates as one act; a modified click is left to the browser.
          <a
            key={row.id}
            id={`${OPTION_ID}${index}`}
            role="option"
            // Out of the tab order: selection is the cursor the field
            // reports through aria-activedescendant, and sixteen focusable
            // rows would mean sixteen Tabs to get back out of the dialog.
            tabIndex={-1}
            aria-selected={index === active}
            className={index === active ? "pal-row on" : "pal-row"}
            href={row.href}
            onMouseMove={() => onHover(index)}
            onClick={(e) => {
              if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
              e.preventDefault();
              onPick(row);
            }}
          >
            {row.kind === "host" ? (
              <Server aria-hidden="true" />
            ) : (
              <Box aria-hidden="true" />
            )}
            <span className="pal-name">{row.name}</span>
            {row.under !== undefined && (
              <span className="pal-under">{row.under}</span>
            )}
          </a>
        );
      })}
    </>
  );
}

function hostEntries(hosts: readonly Host[]): Entry[] {
  return hosts.map((h) => ({
    id: `h${h.id}`,
    kind: "host" as const,
    name: h.hostname,
    href: `/hosts/${h.id}/overview`,
  }));
}

function containerEntries(
  hosts: readonly Host[],
  byHost: ReadonlyMap<number, Container[]>,
): Entry[] {
  const out: Entry[] = [];
  for (const host of hosts) {
    for (const c of byHost.get(host.id) ?? []) {
      out.push({
        id: `c${host.id}:${c.container_key}`,
        kind: "container",
        // The name Docker gave it, falling back to the key the API is keyed
        // on -- the same pair, in the same order, the container list draws.
        name: c.name ?? c.container_key,
        under: host.hostname,
        href: routePath({
          name: "container",
          hostId: String(host.id),
          key: c.container_key,
        }),
      });
    }
  }
  return out;
}

/**
 * Case-insensitive substring, on the name a row shows and -- for a container
 * -- on the host under it, so "db-011" reaches the machine and everything it
 * runs in one query. Not fuzzy: the ask was search, and a fuzzy finder that
 * answers "web-3" with "webhook-relay" spends the reader's attention on
 * rows they did not mean.
 *
 * An empty query lists hosts alone. It is the shorter half of the inventory
 * and the half a bar-level search is usually reaching for; containers appear
 * as soon as there is something to match them against.
 */
function matches(
  entries: readonly Entry[],
  query: string,
): { rows: Entry[]; hidden: number } {
  const q = query.trim().toLowerCase();
  const hit = (e: Entry) =>
    q === ""
      ? e.kind === "host"
      : e.name.toLowerCase().includes(q) ||
        (e.under ?? "").toLowerCase().includes(q);

  const rows: Entry[] = [];
  let hidden = 0;
  for (const kind of ["host", "container"] as const) {
    const found = entries.filter((e) => e.kind === kind && hit(e));
    found.sort((a, b) => a.name.localeCompare(b.name));
    rows.push(...found.slice(0, PER_KIND));
    hidden += Math.max(0, found.length - PER_KIND);
  }
  return { rows, hidden };
}
