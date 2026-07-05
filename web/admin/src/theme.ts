// Design tokens taken directly from the approved prototype (dark theme).

export const c = {
  bg: "#0f1115",
  sidebar: "#12141a",
  card: "#161a22",
  cardAlt: "#1c212b",
  border: "#242832",
  rowBorder: "#1c212b",
  text: "#e6e6e6",
  text2: "#c3c8d1",
  muted: "#8b93a1",
  muted2: "#565c68",
  muted3: "#4d5361",
  accent: "#a78bfa",
  accentInk: "#0f1115",
  danger: "#fca5a5",
  ok: "#86efac",
  warn: "#fcd34d",
} as const;

export const font =
  "system-ui,-apple-system,'Segoe UI',Roboto,sans-serif";
export const mono = "ui-monospace,SFMono-Regular,Menlo,monospace";

type Pill = { bg: string; color: string };

export const sevMeta: Record<string, Pill> = {
  info: { bg: "#1f2937", color: "#93c5fd" },
  success: { bg: "#14311f", color: "#86efac" },
  warning: { bg: "#3a2f14", color: "#fcd34d" },
  error: { bg: "#3a1b1b", color: "#fca5a5" },
  critical: { bg: "#4c1d1d", color: "#fecaca" },
};

export const stateMeta: Record<string, Pill> = {
  new: { bg: "#1f2937", color: "#cbd5e1" },
  acknowledged: { bg: "#1e3a34", color: "#6ee7b7" },
  resolved: { bg: "#1a1d24", color: "#6b7280" },
  muted: { bg: "#23262c", color: "#8b93a1" },
  escalated: { bg: "#4c1d1d", color: "#fecaca" },
};

export const deliveryStateMeta: Record<string, Pill> = {
  pending: { bg: "#1c212b", color: "#cbd5e1" },
  sending: { bg: "#1f2937", color: "#93c5fd" },
  sent: { bg: "#14311f", color: "#86efac" },
  failed: { bg: "#3a2f14", color: "#fcd34d" },
  dead_letter: { bg: "#3a1b1b", color: "#fca5a5" },
};

export const typeLabels: Record<string, string> = {
  incident: "Incident",
  business_event: "Business event",
  audit: "Audit",
};

// Which manual actions are allowed from each event state (mirrors the backend
// state machine in internal/domain/event.go).
export const eventActionRules: Record<
  string,
  { ack: boolean; resolve: boolean; mute: boolean; unmute: boolean; escalate: boolean }
> = {
  new: { ack: true, resolve: false, mute: true, unmute: false, escalate: true },
  acknowledged: { ack: false, resolve: true, mute: true, unmute: false, escalate: true },
  escalated: { ack: true, resolve: true, mute: true, unmute: false, escalate: false },
  muted: { ack: false, resolve: false, mute: false, unmute: true, escalate: false },
  resolved: { ack: false, resolve: false, mute: false, unmute: false, escalate: false },
};

export function sev(name: string): Pill {
  return sevMeta[name] ?? sevMeta.info;
}
export function evState(name: string): Pill {
  return stateMeta[name] ?? stateMeta.new;
}
export function delState(name: string): Pill {
  return deliveryStateMeta[name] ?? deliveryStateMeta.pending;
}
