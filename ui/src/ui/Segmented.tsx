export interface SegmentedOption<T extends string = string> {
  value: T;
  label: string;
}

export interface SegmentedProps<T extends string = string> {
  options: SegmentedOption<T>[];
  value: T;
  onChange: (value: T) => void;
  className?: string;
}

/** A row of mutually-exclusive buttons (e.g. a time-range picker). Exactly one
 * option carries `aria-pressed="true"` at all times. Applies `.seg` (see
 * index.css) — each option is a plain, keyboard-reachable `<button>`. */
export function Segmented<T extends string = string>({
  options,
  value,
  onChange,
  className,
}: SegmentedProps<T>) {
  const classes = className ? `seg ${className}` : "seg";
  return (
    <div className={classes} role="group">
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          aria-pressed={option.value === value}
          onClick={() => onChange(option.value)}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}
