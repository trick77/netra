import { forwardRef, type ButtonHTMLAttributes } from "react";

// iconoir has no spinner glyph; this is lucide's old Loader2 arc, drawn in
// iconoir's own 1.5 stroke so it sits with the rest of the set.
function Spinner() {
  return (
    <svg
      className="animate-spin"
      width={14}
      height={14}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.5}
      strokeLinecap="round"
      aria-hidden="true"
    >
      <path d="M21 12a9 9 0 1 1-6.219-8.56" />
    </svg>
  );
}

export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  /** Visual variant. "secondary" is the base `.btn` look with no modifier class. */
  variant?: ButtonVariant;
  /** True while an async action triggered by this button is in flight. Disables
   * the button for real (not just visually) and shows a spinner. */
  busy?: boolean;
  /** Compact sizing for dense toolbars. */
  small?: boolean;
}

/**
 * The one button in the system. Apply `.btn` plus a variant modifier class —
 * never inline colour. `secondary` carries no modifier class since `.btn`
 * alone is the secondary look (see index.css).
 */
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  function Button(
    {
      variant = "secondary",
      busy = false,
      small = false,
      disabled,
      className,
      children,
      type,
      ...rest
    },
    ref,
  ) {
    const classes = ["btn"];
    if (variant !== "secondary") classes.push(variant);
    if (small) classes.push("small");
    if (className) classes.push(className);

    return (
      <button
        ref={ref}
        type={type ?? "button"}
        className={classes.join(" ")}
        disabled={disabled || busy}
        aria-busy={busy || undefined}
        {...rest}
      >
        {busy && <Spinner />}
        {children}
      </button>
    );
  },
);
