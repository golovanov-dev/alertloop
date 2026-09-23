// Formatting helpers for timestamps and identifiers.

// Every time in the console reads "YYYY-MM-DD HH:MM:SS" in the browser's time
// zone. The date is always there: lists hold up to retention_days of events,
// and a retry can be due tomorrow. The zone is named once, on About; the exact
// instant, with its offset and in UTC, is the title of each time (exactTime).
// A fixed format rather than the browser's locale, so rows line up.
export function dateTime(ts: string): string {
  const d = parseDate(ts);
  return d ? localParts(d).join(" ") : ts;
}

// exactTime is "2026-09-22T16:41:07-03:00 (2026-09-22 19:41:07 UTC)".
export function exactTime(ts: string): string {
  const d = parseDate(ts);
  if (!d) return ts;
  const [date, time] = localParts(d);
  const utc = d.toISOString().slice(0, 19).replace("T", " ");
  return `${date}T${time}${offset(d)} (${utc} UTC)`;
}

// timeZone names the browser's zone and today's offset, shown on About as
// "America/Sao_Paulo (now UTC-03:00)". In a zone with daylight saving an older
// time may have another offset; each time's own offset is in its title.
export function timeZone(): { name: string; offset: string } {
  return { name: Intl.DateTimeFormat().resolvedOptions().timeZone ?? "", offset: "UTC" + offset(new Date()) };
}

function localParts(d: Date): [string, string] {
  return [
    `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())}`,
    `${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`,
  ];
}

function offset(d: Date): string {
  const m = -d.getTimezoneOffset();
  const a = Math.abs(m);
  return `${m < 0 ? "-" : "+"}${p2(Math.floor(a / 60))}:${p2(a % 60)}`;
}

function p2(n: number): string {
  return String(n).padStart(2, "0");
}

function parseDate(ts: string): Date | null {
  if (!ts) return null;
  // Accept "2026-07-03 14:32:07 UTC" as well as RFC3339.
  let s = ts.trim();
  if (s.endsWith(" UTC")) s = s.slice(0, -4).replace(" ", "T") + "Z";
  const d = new Date(s);
  return isNaN(d.getTime()) ? null : d;
}

export function shortId(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id;
}
