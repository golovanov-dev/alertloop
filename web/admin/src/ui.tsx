import { forwardRef, Fragment, useEffect, useRef, useState, type ButtonHTMLAttributes, type CSSProperties, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { dateTime, exactTime } from "./format";
import { useMediaQuery } from "./hooks";
import { c, delState, evState, mono, sev } from "./theme";
import type { DeliveryAttempt, DeliveryState } from "./types";

// Below this width tables put the columns that matter first.
export const NARROW = "(max-width: 600px)";

// Button is a real <button>, so Tab reaches it and Enter or Space presses it;
// the look comes from style. A disabled one stays focusable (aria-disabled),
// so its title can still say why.
export const Button = forwardRef<HTMLButtonElement, Omit<ButtonHTMLAttributes<HTMLButtonElement>, "type">>(
  function Button({ disabled, onClick, className, ...rest }, ref) {
    return (
      <button
        ref={ref}
        type="button"
        className={className ? `al-btn ${className}` : "al-btn"}
        aria-disabled={disabled || undefined}
        onClick={disabled ? undefined : onClick}
        {...rest}
      />
    );
  },
);

// focusIfLost focuses el when the focused element has gone from the page (a
// button replaced by its result), so the keyboard does not restart at the top.
export function focusIfLost(el: HTMLElement | null) {
  if (el && (!document.activeElement || document.activeElement === document.body)) el.focus();
}

// Time shows a timestamp as dateTime does, with the exact instant in its title.
export function Time({ ts }: { ts?: string | null }) {
  if (!ts) return <>—</>;
  return <time title={exactTime(ts)}>{dateTime(ts)}</time>;
}

// Pill / badge for severity, event state, delivery state.
export function Badge({
  kind,
  value,
  pulse,
}: {
  kind: "severity" | "state" | "delivery";
  value: string;
  pulse?: boolean;
}) {
  const meta =
    kind === "severity" ? sev(value) : kind === "state" ? evState(value) : delState(value);
  return (
    <span
      style={{
        display: "inline-block",
        padding: "2px 9px",
        borderRadius: 999,
        fontSize: 11.5,
        fontWeight: 600,
        background: meta.bg,
        color: meta.color,
        animation: pulse ? "escalate-pulse 2.2s infinite" : undefined,
        whiteSpace: "nowrap",
      }}
    >
      {value}
    </span>
  );
}

export function Card({ children, style }: { children: ReactNode; style?: CSSProperties }) {
  return (
    <div
      style={{
        background: c.card,
        border: `1px solid ${c.border}`,
        borderRadius: 10,
        ...style,
      }}
    >
      {children}
    </div>
  );
}

export function PageTitle({ title, subtitle }: { title: string; subtitle?: string }) {
  return (
    <div>
      <h1 style={{ margin: 0, fontSize: 26, fontWeight: 600, letterSpacing: "-0.01em" }}>
        {title}
      </h1>
      {subtitle && (
        <div style={{ marginTop: 6, fontSize: 14, color: c.muted }}>{subtitle}</div>
      )}
    </div>
  );
}

export const th: CSSProperties = {
  textAlign: "left",
  padding: "10px 14px",
  color: c.muted,
  fontWeight: 600,
  fontSize: 11,
  textTransform: "uppercase",
  letterSpacing: ".05em",
  borderBottom: `1px solid ${c.border}`,
};

export const td: CSSProperties = {
  padding: "10px 14px",
  borderBottom: `1px solid ${c.rowBorder}`,
};

// nowrap: a "2026-09-10 14:32:07" cell must not break at its space and turn
// every row into two lines. Tables sit in horizontally scrolling containers.
export const tdMono: CSSProperties = { ...td, fontFamily: mono, color: c.muted, whiteSpace: "nowrap" };

export function Loading() {
  return (
    <Card style={{ overflow: "hidden" }}>
      {[0, 1, 2, 3, 4, 5].map((i) => (
        <div
          key={i}
          style={{
            display: "flex",
            alignItems: "center",
            gap: 16,
            padding: "16px 18px",
            borderBottom: `1px solid ${c.rowBorder}`,
          }}
        >
          {[70, 60, undefined, 110].map((w, j) => (
            <div
              key={j}
              style={{
                width: w ?? undefined,
                flex: w ? undefined : 1,
                height: j === 1 ? 20 : 12,
                borderRadius: j === 1 ? 999 : 4,
                background: "#23262c",
                animation: `skeleton-pulse 1.4s ease-in-out infinite`,
                animationDelay: `${j * 0.05}s`,
              }}
            />
          ))}
        </div>
      ))}
    </Card>
  );
}

export function EmptyState({
  title,
  hint,
  action,
}: {
  title: string;
  hint?: string;
  action?: ReactNode;
}) {
  return (
    <Card
      style={{
        padding: "70px 20px",
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        textAlign: "center",
      }}
    >
      <div
        style={{
          width: 56,
          height: 56,
          borderRadius: 14,
          background: c.cardAlt,
          border: `1px solid ${c.border}`,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
        }}
      >
        <svg width="24" height="24" viewBox="0 0 16 16" fill="none">
          <rect x="2" y="2" width="12" height="12" rx="2" stroke={c.muted2} strokeWidth="1.4" strokeDasharray="3 2.4" />
        </svg>
      </div>
      <div style={{ marginTop: 18, fontSize: 15, fontWeight: 600 }}>{title}</div>
      {hint && (
        <div style={{ marginTop: 6, fontSize: 13.5, color: c.muted, maxWidth: 340 }}>{hint}</div>
      )}
      {action && <div style={{ marginTop: 20 }}>{action}</div>}
    </Card>
  );
}

export function ErrorState({
  message,
  onRetry,
  title = "Could not load data",
}: {
  message: string;
  onRetry?: () => void;
  title?: string;
}) {
  return (
    <Card
      style={{
        border: "1px solid #3a1b1b",
        padding: "70px 20px",
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        textAlign: "center",
      }}
    >
      <div
        style={{
          width: 56,
          height: 56,
          borderRadius: 14,
          background: "#241717",
          border: "1px solid #3a1b1b",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
        }}
      >
        <svg width="24" height="24" viewBox="0 0 16 16" fill="none">
          <path d="M8 2 14.5 13.5H1.5Z" stroke={c.danger} strokeWidth="1.4" strokeLinejoin="round" />
          <line x1="8" y1="6.3" x2="8" y2="9.6" stroke={c.danger} strokeWidth="1.4" strokeLinecap="round" />
          <circle cx="8" cy="11.6" r="0.9" fill={c.danger} />
        </svg>
      </div>
      <div style={{ marginTop: 18, fontSize: 15, fontWeight: 600 }}>{title}</div>
      <div style={{ marginTop: 6, fontSize: 13.5, color: c.muted, maxWidth: 380 }}>{message}</div>
      {onRetry && (
        <Button
          onClick={onRetry}
          style={{
            marginTop: 20,
            padding: "9px 18px",
            borderRadius: 7,
            border: "1px solid #7a3a3a",
            background: "rgba(252,165,165,0.08)",
            color: c.danger,
            fontSize: 13.5,
          }}
        >
          Try again
        </Button>
      )}
    </Card>
  );
}

// alertKey pairs a recovery with the alert of its event to the same channel.
export function alertKey(d: Pick<DeliveryAttempt, "event_id" | "channel_name">): string {
  return d.event_id + "\u0000" + d.channel_name;
}

// alertStates maps alertKey to the state of each alert among rows.
export function alertStates(rows: DeliveryAttempt[]): Map<string, DeliveryState> {
  const m = new Map<string, DeliveryState>();
  for (const d of rows) if (d.kind === "alert") m.set(alertKey(d), d.state);
  return m;
}

// A recovery is not sent while its alert is anything but sent (storage.ClaimDue).
// An alert in dead_letter holds it until someone replays the alert.
function waitsForDeadAlert(d: DeliveryAttempt, alerts: Map<string, DeliveryState>): boolean {
  return d.kind === "recovery" && alerts.get(alertKey(d)) === "dead_letter";
}

export function DeliveryStateCell({ d, alerts }: { d: DeliveryAttempt; alerts: Map<string, DeliveryState> }) {
  const waiting = (d.state === "pending" || d.state === "failed") && waitsForDeadAlert(d, alerts);
  return (
    <>
      <Badge kind="delivery" value={d.state} />
      {waiting && (
        <div style={{ marginTop: 4, fontSize: 12, color: c.warn, maxWidth: 130 }}>
          waiting for alert — replay the alert
        </div>
      )}
    </>
  );
}

// ReplayCell offers Replay on a dead-lettered attempt and says what a replay did.
export function ReplayCell({
  d,
  alerts,
  replayed,
  busy,
  onReplay,
}: {
  d: DeliveryAttempt;
  alerts: Map<string, DeliveryState>;
  replayed: boolean;
  busy: boolean;
  onReplay: () => void;
}) {
  // The result takes the focus from the Replay button it replaces.
  const result = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (replayed) focusIfLost(result.current);
  }, [replayed]);
  if (replayed) {
    const waits = waitsForDeadAlert(d, alerts);
    return (
      <span ref={result} tabIndex={-1} style={{ fontSize: 12, color: waits ? c.warn : c.ok, outline: "none" }}>
        {waits ? "Queued, waits for its alert" : "Queued ✓"}
      </span>
    );
  }
  if (d.state !== "dead_letter") return null;
  return (
    <Button
      disabled={busy}
      onClick={onReplay}
      style={{
        display: "inline-block",
        padding: "5px 12px",
        borderRadius: 6,
        background: c.accent,
        color: c.accentInk,
        fontSize: 12.5,
        fontWeight: 600,
        cursor: busy ? "default" : "pointer",
        opacity: busy ? 0.6 : 1,
      }}
    >
      Replay
    </Button>
  );
}

// LastErrorCell shows the start of an error; pressing it shows the whole text
// in a row of its own under the attempt, which a title alone does not do on a
// phone or from the keyboard. That row spans the table, so a long error does
// not widen the columns and push Replay out of the card.
function LastErrorCell({ id, text, width, open, onToggle }: { id: string; text?: string; width: number; open: boolean; onToggle: () => void }) {
  if (!text) return <td style={tdMono}>—</td>;
  return (
    <td style={tdMono}>
      <Button
        aria-expanded={open}
        aria-controls={open ? `al-err-${id}` : undefined}
        title={open ? undefined : text}
        onClick={onToggle}
        style={{ display: "block", maxWidth: width, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", textAlign: "left" }}
      >
        {text}
      </Button>
    </td>
  );
}

// DeliveriesTable lists delivery attempts: on the Deliveries screen with the
// Event column, and on an event's page. On a narrow screen State and Replay
// come first; the rest is behind the horizontal scroll.
export function DeliveriesTable({
  items,
  alerts,
  replayed,
  replaying,
  onReplay,
  showEvent,
}: {
  items: DeliveryAttempt[];
  alerts: Map<string, DeliveryState>;
  replayed: Record<string, boolean>;
  replaying: string | null;
  onReplay: (id: string) => void;
  showEvent?: boolean;
}) {
  const narrow = useMediaQuery(NARROW);
  const [openErrors, setOpenErrors] = useState<Record<string, boolean>>({});
  const cols: Record<string, { h: string; cell: (d: DeliveryAttempt) => ReactNode }> = {
    channel: {
      h: "Channel",
      cell: (d) => (
        <td style={{ ...td, color: c.text2, whiteSpace: "nowrap" }}>
          {d.channel} <span style={{ color: c.muted }}>/</span>{" "}
          <code style={{ fontFamily: mono, fontSize: 12.5 }}>{d.channel_name}</code>
        </td>
      ),
    },
    // An alert and its recovery are two rows for the same event. Without this
    // column they are indistinguishable, and a recovery reads as a duplicate.
    kind: {
      h: "Kind",
      cell: (d) => (
        <td style={{ ...td, whiteSpace: "nowrap", color: d.kind === "recovery" ? c.text2 : c.muted }}>
          {d.kind === "recovery" ? "recovery" : "alert"}
        </td>
      ),
    },
    event: {
      h: "Event",
      cell: (d) => (
        <td style={tdMono}>
          <Link to={`/events/${d.event_id}`} title={d.event_id} style={{ color: c.accent, textDecoration: "none" }}>
            {d.event_id.slice(0, 8)}
          </Link>
        </td>
      ),
    },
    state: {
      h: "State",
      cell: (d) => (
        <td style={td}>
          <DeliveryStateCell d={d} alerts={alerts} />
        </td>
      ),
    },
    attempts: {
      h: "Attempts",
      cell: (d) => (
        <td style={{ ...tdMono, color: c.text2 }}>
          {d.attempts} / {d.max_attempts}
        </td>
      ),
    },
    retry: {
      h: "Next retry (local)",
      cell: (d) => (
        <td style={tdMono}>
          <Time ts={d.next_retry_at} />
        </td>
      ),
    },
    error: {
      h: "Last error",
      cell: (d) => (
        <LastErrorCell
          id={d.id}
          text={d.last_error}
          width={showEvent ? 220 : 120}
          open={!!openErrors[d.id]}
          onToggle={() => setOpenErrors((o) => ({ ...o, [d.id]: !o[d.id] }))}
        />
      ),
    },
    updated: {
      h: "Updated (local)",
      cell: (d) => (
        <td style={tdMono}>
          <Time ts={d.updated_at} />
        </td>
      ),
    },
    replay: {
      h: "",
      cell: (d) => (
        <td style={{ ...td, textAlign: narrow ? "left" : "right" }}>
          <ReplayCell
            d={d}
            alerts={alerts}
            replayed={!!replayed[d.id]}
            busy={replaying === d.id}
            onReplay={() => onReplay(d.id)}
          />
        </td>
      ),
    },
  };
  const order = (
    narrow
      ? ["state", "replay", "channel", "kind", "event", "error", "attempts", "retry", "updated"]
      : ["channel", "kind", "event", "state", "attempts", "retry", "error", "updated", "replay"]
  ).filter((k) => showEvent || k !== "event");
  return (
    <div className="al-table-scroll">
      <table style={{ width: "100%", minWidth: showEvent ? 920 : 640, fontSize: 13.5 }}>
        <thead>
          <tr>
            {order.map((k) => (
              <th key={k} style={th}>
                {cols[k].h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {items.map((d) => (
            <Fragment key={d.id}>
              <tr>
                {order.map((k) => (
                  <Fragment key={k}>{cols[k].cell(d)}</Fragment>
                ))}
              </tr>
              {openErrors[d.id] && d.last_error && (
                <tr id={`al-err-${d.id}`}>
                  <td colSpan={order.length} style={{ ...tdMono, whiteSpace: "pre-wrap", overflowWrap: "anywhere", color: c.text2 }}>
                    {d.last_error}
                  </td>
                </tr>
              )}
            </Fragment>
          ))}
        </tbody>
      </table>
    </div>
  );
}
