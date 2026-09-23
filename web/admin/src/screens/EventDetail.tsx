import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, ApiError } from "../api";
import { useApp } from "../context";
import { c, mono } from "../theme";
import type { AlertEvent, DeliveryAttempt, EventAction } from "../types";
import { alertStates, Badge, Button, Card, DeliveriesTable, ErrorState, Loading, Time } from "../ui";
import { PayloadView } from "../components/PayloadView";

const actionTarget: Record<Exclude<EventAction, "unmute">, string> = {
  ack: "acknowledged",
  resolve: "resolved",
  mute: "muted",
  escalate: "escalated",
};

// blockedReason says why an action is unavailable, or null when it is allowed.
// It follows ApplyAction in internal/domain/event.go, the only place the rules
// live: nothing leaves `resolved`, unmute needs `muted`, everything else is
// allowed. An action whose target is the current state changes nothing, so it
// is offered as disabled too.
function blockedReason(state: string, action: EventAction): string | null {
  if (state === "resolved") return "The incident is resolved; nothing changes it any more.";
  if (action === "unmute") return state === "muted" ? null : "The incident is not muted.";
  if (state === actionTarget[action]) return `Already ${state}.`;
  return null;
}

export function EventDetail() {
  const { id = "" } = useParams();
  const { showToast } = useApp();

  const [event, setEvent] = useState<AlertEvent | null>(null);
  const [deliveries, setDeliveries] = useState<DeliveryAttempt[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notFound, setNotFound] = useState(false);

  // Resolve and Escalate ask for confirmation first.
  const [confirming, setConfirming] = useState<"resolve" | "escalate" | null>(null);
  // The button that opened the confirmation gets the focus back when it closes.
  const opener = useRef<HTMLElement | null>(null);
  const [busy, setBusy] = useState(false);
  const [replaying, setReplaying] = useState<string | null>(null);
  const [payloadExpanded, setPayloadExpanded] = useState(true);
  const [copied, setCopied] = useState<string | null>(null);
  const [replayed, setReplayed] = useState<Record<string, boolean>>({});

  const load = () => {
    setLoading(true);
    setError(null);
    setNotFound(false);
    Promise.all([api.getEvent(id), api.listDeliveries({ event_id: id, limit: 200 })])
      .then(([ev, dl]) => {
        setEvent(ev);
        setDeliveries(dl.items);
      })
      .catch((e) => {
        setNotFound(e instanceof ApiError && e.status === 404);
        setError(e instanceof Error ? e.message : "Failed to load");
      })
      .finally(() => setLoading(false));
  };

  // reloadQuietly re-reads the event and its deliveries without the loading
  // screen: after an action the server may have queued a recovery. It returns
  // the event it read, or null when the reload failed (the toast says so).
  const reloadQuietly = (): Promise<AlertEvent | null> =>
    Promise.all([api.getEvent(id), api.listDeliveries({ event_id: id, limit: 200 })])
      .then(([ev, dl]) => {
        setEvent(ev);
        setDeliveries(dl.items);
        return ev;
      })
      .catch((e) => {
        showToast(e instanceof Error ? e.message : "Failed to reload");
        return null;
      });
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(load, [id]);

  // The clipboard needs HTTPS or localhost. Without it the copy fails and the
  // toast says so; the values are plain text and can be selected instead.
  const copy = (text: string, key: string) => {
    const failed = () => showToast("Could not copy. Select the text instead.");
    if (!navigator.clipboard) return failed();
    navigator.clipboard.writeText(text).then(() => {
      setCopied(key);
      window.setTimeout(() => setCopied((k) => (k === key ? null : k)), 1400);
    }, failed);
  };

  const ask = (what: "resolve" | "escalate") => {
    opener.current = document.activeElement as HTMLElement | null;
    setConfirming(what);
  };
  const closeConfirm = () => {
    setConfirming(null);
    opener.current?.focus();
    opener.current = null;
  };

  // Escape closes the confirmation, unless the phone menu is open over it (the
  // page behind the menu is inert): that Escape belongs to the menu.
  const confirmBox = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!confirming) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && !confirmBox.current?.closest("[inert]") && closeConfirm();
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [confirming]);

  // The answer to an action is the event after it, so the buttons follow it at
  // once; the reload that follows adds the deliveries (a queued recovery).
  const doAction = async (action: EventAction, msg: string) => {
    if (!event || busy) return;
    setBusy(true);
    let error: unknown = null;
    try {
      setEvent(await api.eventAction(event.id, action));
      showToast(msg);
    } catch (e) {
      error = e;
    }
    // Only the confirmed action returns the focus to the button that asked;
    // another action pressed meanwhile keeps the focus where it is.
    if (confirming === action) closeConfirm();
    else setConfirming(null);
    const fresh = await reloadQuietly();
    setBusy(false);
    if (!error) return;
    // A 409 on resolve usually means someone else (a second click, the
    // monitoring source) closed the incident first. Say so only when the reload
    // shows it resolved; otherwise the server's reason stands ("retry").
    if (error instanceof ApiError && error.status === 409 && action === "resolve" && fresh?.state === "resolved") {
      showToast("The incident is already resolved.");
    } else {
      showToast(error instanceof Error ? error.message : "Action failed");
    }
  };

  const replay = async (did: string) => {
    setReplaying(did);
    try {
      const updated = await api.replay(did);
      setDeliveries((prev) => prev.map((d) => (d.id === did ? updated : d)));
      setReplayed((r) => ({ ...r, [did]: true }));
      showToast("Delivery re-queued");
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Replay failed");
    } finally {
      setReplaying(null);
    }
  };

  const alerts = useMemo(() => alertStates(deliveries), [deliveries]);

  const back = (
    <Link
      to="/events"
      style={{ display: "inline-flex", alignItems: "center", gap: 6, fontSize: 13, color: c.muted, textDecoration: "none" }}
    >
      <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
        <path d="M10 3 5 8l5 5" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
      Events
    </Link>
  );

  if (loading) {
    return (
      <div style={{ maxWidth: 900 }}>
        {back}
        <div style={{ marginTop: 20 }}>
          <Loading />
        </div>
      </div>
    );
  }
  if (error || !event) {
    return (
      <div style={{ maxWidth: 900 }}>
        {back}
        <div style={{ marginTop: 20 }}>
          {notFound ? (
            <ErrorState
              title="Event not found"
              message="There is no event with this ID. It may have been deleted by retention."
            />
          ) : (
            <ErrorState message={error ?? "Event not found"} onRetry={load} />
          )}
        </div>
      </div>
    );
  }

  const isIncident = event.type === "incident";
  const reason = (a: EventAction) => (busy ? "Wait for the previous action to finish." : blockedReason(event.state, a));

  return (
    <div style={{ maxWidth: 900 }}>
      {back}

      <div style={{ marginTop: 14 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 10, flexWrap: "wrap" }}>
          <Badge kind="severity" value={event.severity} />
          <Badge kind="state" value={event.state} pulse={event.state === "escalated"} />
          <span style={{ fontFamily: mono, fontSize: 12.5, color: c.muted }}>
            <Time ts={event.created_at} />
          </span>
        </div>
        <h1 style={{ margin: "12px 0 0", fontSize: 24, fontWeight: 600, letterSpacing: "-0.01em", lineHeight: 1.3, overflowWrap: "anywhere" }}>
          {event.message}
        </h1>
      </div>

      {/* Actions: incidents only (the server answers 409 for other types). */}
      {isIncident ? (
        <div style={{ display: "flex", gap: 10, flexWrap: "wrap", marginTop: 20 }}>
          <ActionButton label="Acknowledge" blocked={reason("ack")} onClick={() => doAction("ack", "Event acknowledged")} />
          <ActionButton label="Resolve" blocked={reason("resolve")} onClick={() => ask("resolve")} />
          {event.state === "muted" ? (
            <ActionButton
              label="Unmute"
              blocked={reason("unmute")}
              hint="Returns the incident to new."
              onClick={() => doAction("unmute", "Event unmuted, back to new")}
            />
          ) : (
            <ActionButton label="Mute" blocked={reason("mute")} onClick={() => doAction("mute", "Event muted")} />
          )}
          <ActionButton
            label="Mark escalated"
            blocked={reason("escalate")}
            hint="Marks the incident as escalated. No notifications are sent."
            danger
            onClick={() => ask("escalate")}
          />
        </div>
      ) : (
        <div style={{ marginTop: 16, fontSize: 13, color: c.muted }}>
          Actions apply to incidents only.
        </div>
      )}

      {confirming && (
        <div
          ref={confirmBox}
          role="group"
          aria-labelledby="confirm-text"
          style={{
            marginTop: 12,
            padding: "12px 16px",
            borderRadius: 8,
            background: "#241a2e",
            border: "1px solid #4c3a68",
            display: "flex",
            alignItems: "center",
            gap: 14,
            flexWrap: "wrap",
          }}
        >
          <span id="confirm-text" style={{ fontSize: 13, flex: 1, minWidth: 200 }}>
            {confirming === "escalate"
              ? "Mark this incident as escalated? No notifications are sent."
              : event.state === "muted"
                ? "Resolve this incident? It is muted, so no recovery notice is sent."
                : "Resolve this incident? Channels that got its alert receive a recovery notice, unless notify_on_resolve is off."}
          </span>
          {/* Focus lands on Cancel: two presses of Enter do not change the incident. */}
          <Button autoFocus onClick={closeConfirm} style={{ fontSize: 13, color: c.muted }}>
            Cancel
          </Button>
          <Button
            disabled={busy}
            onClick={() =>
              confirming === "escalate"
                ? doAction("escalate", "Event marked as escalated")
                : doAction("resolve", "Event resolved")
            }
            style={{
              padding: "6px 14px",
              borderRadius: 6,
              background: c.accent,
              color: c.accentInk,
              fontSize: 13,
              fontWeight: 600,
              cursor: busy ? "default" : "pointer",
              opacity: busy ? 0.6 : 1,
            }}
          >
            {confirming === "escalate" ? "Confirm mark escalated" : "Confirm resolve"}
          </Button>
        </div>
      )}

      {/* Metadata */}
      <Card style={{ marginTop: 32, padding: "20px 22px" }}>
        <SectionTitle>Metadata</SectionTitle>
        <div className="al-meta" style={{ display: "grid", gridTemplateColumns: "150px minmax(0, 1fr)", gap: "10px 16px", fontSize: 13.5 }}>
          <Meta label="ID">
            <CopyValue what="ID" value={event.id} copied={copied === "id"} onCopy={() => copy(event.id, "id")} />
          </Meta>
          <Meta label="Source">{event.source}</Meta>
          <Meta label="Category">{event.category || "—"}</Meta>
          <Meta label="Entity">{event.entity_type ? `${event.entity_type} / ${event.entity_id}` : "—"}</Meta>
          <Meta label="Trace ID">
            {event.trace_id ? (
              <CopyValue what="trace ID" value={event.trace_id} copied={copied === "trace"} onCopy={() => copy(event.trace_id!, "trace")} />
            ) : (
              <span style={{ color: c.muted2, fontFamily: mono }}>—</span>
            )}
          </Meta>
          <Meta label="Dedupe key">
            {event.dedupe_key ? (
              <CopyValue what="dedupe key" value={event.dedupe_key} copied={copied === "dedupe"} onCopy={() => copy(event.dedupe_key!, "dedupe")} />
            ) : (
              <span style={{ color: c.muted2, fontFamily: mono }}>—</span>
            )}
          </Meta>
          <Meta label="Created">
            <span style={{ fontFamily: mono }}>
              <Time ts={event.created_at} />
            </span>
          </Meta>
          <Meta label="Last seen">
            <span style={{ fontFamily: mono }}>
              <Time ts={event.last_seen_at || event.created_at} />
            </span>
          </Meta>
          {event.resolved_at ? (
            <Meta label="Resolved">
              <span style={{ fontFamily: mono }}>
              <Time ts={event.resolved_at} />
            </span>
            </Meta>
          ) : null}
          <Meta label="Updated">
            <span style={{ fontFamily: mono }}>
              <Time ts={event.updated_at} />
            </span>
          </Meta>
        </div>
      </Card>

      {/* Payload */}
      <Card style={{ marginTop: 20, padding: "20px 22px" }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
          <SectionTitle noMargin>Payload</SectionTitle>
          <div style={{ display: "flex", gap: 14 }}>
            <Button aria-expanded={payloadExpanded} onClick={() => setPayloadExpanded((v) => !v)} style={{ fontSize: 12.5, color: c.accent }}>
              {payloadExpanded ? "Collapse" : "Expand"}
            </Button>
            <Button
              onClick={() => {
                copy(JSON.stringify(event.payload ?? {}, null, 2), "payload");
              }}
              style={{ fontSize: 12.5, color: c.accent }}
            >
              {copied === "payload" ? "Copied ✓" : "Copy"}
            </Button>
          </div>
        </div>
        {payloadExpanded && <PayloadView value={event.payload ?? {}} />}
      </Card>

      {/* Deliveries */}
      <Card style={{ marginTop: 20, overflow: "hidden" }}>
        <div style={{ padding: "20px 22px 0" }}>
          <SectionTitle noMargin>Deliveries</SectionTitle>
        </div>
        {deliveries.length > 0 ? (
          <div style={{ marginTop: 14 }}>
            <DeliveriesTable items={deliveries} alerts={alerts} replayed={replayed} replaying={replaying} onReplay={replay} />
          </div>
        ) : (
          <div style={{ padding: "40px 20px", textAlign: "center", color: c.muted, fontSize: 13.5 }}>
            No delivery attempts.
          </div>
        )}
      </Card>
    </div>
  );
}

function ActionButton({
  label,
  blocked,
  hint,
  onClick,
  danger,
}: {
  label: string;
  /** Why the action is unavailable; null when it is allowed. */
  blocked: string | null;
  hint?: string;
  onClick: () => void;
  danger?: boolean;
}) {
  const enabled = blocked === null;
  return (
    <Button
      disabled={!enabled}
      onClick={onClick}
      title={blocked ?? hint}
      style={{
        padding: "8px 16px",
        borderRadius: 7,
        fontSize: 13,
        fontWeight: danger ? 600 : 500,
        background: danger && enabled ? "rgba(252,165,165,0.08)" : c.card,
        border: `1px solid ${danger && enabled ? "#7a3a3a" : c.border}`,
        color: enabled ? (danger ? c.danger : c.text2) : c.muted3,
      }}
    >
      {label}
    </Button>
  );
}

function SectionTitle({ children, noMargin }: { children: React.ReactNode; noMargin?: boolean }) {
  return (
    <h2
      style={{
        margin: noMargin ? 0 : "0 0 14px",
        fontSize: 13,
        fontWeight: 600,
        color: c.muted,
        textTransform: "uppercase",
        letterSpacing: ".04em",
      }}
    >
      {children}
    </h2>
  );
}

function Meta({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <>
      <div style={{ color: c.muted }}>{label}</div>
      <div style={{ minWidth: 0, overflowWrap: "anywhere" }}>{children}</div>
    </>
  );
}

// CopyValue is the value as plain text, which any browser can select, and a
// Copy button next to it.
function CopyValue({ what, value, copied, onCopy }: { what: string; value: string; copied: boolean; onCopy: () => void }) {
  return (
    <>
      <span style={{ fontFamily: mono, color: c.muted }}>{value}</span>{" "}
      <Button onClick={onCopy} aria-label={`Copy ${what}`} style={{ marginLeft: 8, fontSize: 12.5, color: copied ? c.ok : c.accent }}>
        {copied ? "Copied ✓" : "Copy"}
      </Button>
    </>
  );
}
