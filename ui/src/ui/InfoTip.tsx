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
  // has no pointerleave to close on, so without these a bubble opened by a
  // tap would stay until the next tap on the same button.
  useEffect(() => {
    if (!open) return;
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    const down = (e: PointerEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("keydown", key);
    document.addEventListener("pointerdown", down);
    return () => {
      document.removeEventListener("keydown", key);
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
