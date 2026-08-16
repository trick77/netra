import { OS_ICON_PATHS, OS_ICON_VIEWBOX, osIcon } from "../lib/osIcon";

/**
 * The distribution mark for a host's os_name, or nothing at all when the
 * table does not know the name.
 *
 * aria-hidden and focusable="false": the mark is decoration in the strictest
 * sense -- the OS name it sits in front of is right there, spelled out, so
 * announcing "Ubuntu" twice is the only thing this could add. focusable is
 * the IE/Edge-era attribute that keeps an SVG out of the tab order; harmless
 * elsewhere and cheap insurance.
 *
 * currentColor, so the mark takes the colour of the text it labels and
 * follows the theme with it. See lib/osIcon.ts on why it is never painted in
 * the brand's own colour.
 */
export function OsIcon({ name }: { name: string | null }) {
  const key = osIcon(name);
  if (key === null) return null;
  return (
    <svg
      className="osicon"
      viewBox={OS_ICON_VIEWBOX}
      width="14"
      height="14"
      fill="currentColor"
      aria-hidden="true"
      focusable="false"
    >
      <path d={OS_ICON_PATHS[key]} />
    </svg>
  );
}
