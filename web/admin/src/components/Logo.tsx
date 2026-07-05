import { c } from "../theme";

export function Logo({ size = 22 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 22 22" fill="none">
      <circle cx="11" cy="11" r="8.5" stroke={c.accent} strokeWidth="2" strokeDasharray="40 13.4" />
      <polygon points="15.5,4.5 19,6.7 15,8" fill={c.accent} />
    </svg>
  );
}
