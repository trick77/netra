// The (i) beside a chart title, and the sentence behind it.
//
// The host detail page draws thirty-odd panels whose titles are the kernel's
// words rather than the reader's -- "TCP listen queue", "IP fragmentation",
// "Disk utilisation". What each one means, and what a bad reading looks like,
// was already written down in chartSpecs.ts as source comments; this is how
// it reaches the person looking at the chart.
//
// Given ONLY where the title is not enough. A panel with no text draws no
// glyph at all, and that absence is the point: an explanation on "Uptime"
// teaches the reader that these are not worth opening.
import { useEffect, useId, useRef, useState } from "react";
import { Info } from "lucide-react";

export interface InfoTipProps {
  /** The sentence or two. One to three sentences; this is not a manual. */
  text: string;
  /** What the tip is ABOUT, for the button's accessible name -- the panel
   * title. "About TCP listen queue" says which of thirty buttons this is,
   * where a bare "About" in a grid of panels says nothing. */
  label: string;
}

export function InfoTip({ text, label }: InfoTipProps) {
  const [open, setOpen] = useState(false);
  const id = useId();
  const wrap = useRef<HTMLSpanElement>(null);

  // Escape closes it, and a pointer down anywhere else does too. Both are for
  // the TAP case: a pointer that entered and will never leave -- a finger --
  // has no pointerleave to close on, so a bubble opened by a tap is closed by
  // Escape or by a press elsewhere. (Tapping the glyph again does not close
  // it: onClick opens rather than toggles, deliberately -- see below.)
  //
  // The keydown is registered in the CAPTURE phase and stops the event there,
  // so Escape closes the topmost layer only. Inside the enlarged chart dialog
  // both this and ChartDetail listen on `document`; without this, one press
  // closed the tooltip and the dialog under it at once. document is visited
  // twice per event -- capture on the way down, bubble on the way back up --
  // so stopping it here is what keeps ChartDetail's bubble-phase listener
  // from running. It is registered only while the bubble is open, so a press
  // with no tooltip showing closes the dialog exactly as it always did. Only
  // Escape is stopped; every other key still reaches the page (the fleet
  // page's "/" shortcut among them).
  useEffect(() => {
    if (!open) return;
    const key = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      setOpen(false);
    };
    const down = (e: PointerEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("keydown", key, true);
    document.addEventListener("pointerdown", down);
    return () => {
      document.removeEventListener("keydown", key, true);
      document.removeEventListener("pointerdown", down);
    };
  }, [open]);

  return (
    <span className="itip" ref={wrap}>
      <button
        type="button"
        className="itipb"
        aria-label={`About ${label}`}
        aria-expanded={open}
        aria-describedby={open ? id : undefined}
        // Hover and keyboard focus both open it, so neither a mouse user nor
        // a tab user has to click. onClick opens it too, for the touch device
        // that has no hover to open it with -- and OPENS rather than toggles,
        // because for a mouse the click arrives on a bubble that pointerenter
        // has already opened, and toggling there would close what the reader
        // just pointed at. Escape and a press elsewhere are what close it.
        onPointerEnter={(e) => {
          // Touch fires enter on tap, immediately followed by click. Letting
          // both through would open and then close in one gesture.
          if (e.pointerType !== "touch") setOpen(true);
        }}
        onPointerLeave={(e) => {
          if (e.pointerType !== "touch") setOpen(false);
        }}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onClick={() => setOpen(true)}
      >
        <Info size={13} aria-hidden="true" focusable="false" />
      </button>
      {open && (
        <span className="itipbub" id={id} role="tooltip">
          {text}
        </span>
      )}
    </span>
  );
}
