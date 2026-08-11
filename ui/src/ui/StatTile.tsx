// The one large number on a page uses proportional figures (design system
// default, spec §3.6) -- tabular-nums is reserved for columns of numbers
// that need to line up vertically, which a single headline value is not.
// index.css's `.tile .v` rule deliberately carries no font-variant-numeric,
// so this component must not add one inline.
export interface StatTileProps {
  label: string;
  value: string | number;
  unit?: string;
  detail?: string;
}

export function StatTile({ label, value, unit, detail }: StatTileProps) {
  return (
    <div className="tile">
      <div className="k">{label}</div>
      <div className="v">
        {value}
        {unit !== undefined && <span> {unit}</span>}
      </div>
      {detail !== undefined && <div className="d">{detail}</div>}
    </div>
  );
}
