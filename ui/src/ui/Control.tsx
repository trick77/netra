import {
  forwardRef,
  type InputHTMLAttributes,
  type SelectHTMLAttributes,
} from "react";

/** The one control class. Applied to native `<input>`/`<select>` rather than
 * wrapping them in a controlled abstraction (see index.css `.ctl`). */
export const controlClass = "ctl";

export type InputProps = InputHTMLAttributes<HTMLInputElement>;

/** Thin wrapper: forwards props and ref onto a native `<input>` carrying `.ctl`. */
export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { className, ...rest },
  ref,
) {
  const classes = className ? `${controlClass} ${className}` : controlClass;
  return <input ref={ref} className={classes} {...rest} />;
});

export type SelectProps = SelectHTMLAttributes<HTMLSelectElement>;

/** Thin wrapper: forwards props and ref onto a native `<select>` carrying `.ctl`. */
export const Select = forwardRef<HTMLSelectElement, SelectProps>(
  function Select({ className, children, ...rest }, ref) {
    const classes = className ? `${controlClass} ${className}` : controlClass;
    return (
      <select ref={ref} className={classes} {...rest}>
        {children}
      </select>
    );
  },
);
