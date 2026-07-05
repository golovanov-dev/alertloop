import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { api } from "../api";
import { useApp } from "../context";
import { fullTime, timeOfDay } from "../format";
import { c, eventActionRules, mono } from "../theme";
import type { AlertEvent, DeliveryAttempt, EventAction } from "../types";
import { Badge, Card, ErrorState, Loading, td, tdMono, th } from "../ui";
import { PayloadView } from "../components/PayloadView";

export function EventDetail() {
  const { id = "" } = useParams();
  const nav = useNavigate();
  const { showToast } = useApp();

  const [event, setEvent] = useState<AlertEvent | null>(null);
  const [deliveries, setDeliveries] = useState<DeliveryAttempt[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [confirmingEscalate, setConfirmingEscalate] = useState(false);
  const [payloadExpanded, setPayloadExpanded] = useState(true);
  const [copied, setCopied] = useState<string | null>(null);
  const [replayed, setReplayed] = useState<Record<string, boolean>>({});

  const load = () => {
    setLoading(true);
    setError(null);
    Promise.all([api.getEvent(id), api.listDeliveries({ event_id: id, limit: 200 })])
      .then(([ev, dl]) => {
        setEvent(ev);
        setDeliveries(dl.items);
      })
      .catch((e) => setError(e instanceof Error ? e.message : "Failed to load"))
      .finally(() => setLoading(false));
  };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(load, [id]);

  const copy = (text: string, key: string) => {
    navigator.clipboard?.writeText(text).catch(() => {});
    setCopied(key);
    window.setTimeout(() => setCopied((k) => (k === key ? null : k)), 1400);
  };

  const doAction = async (action: EventAction, msg: string) => {
    if (!event) return;
    try {
      const updated = await api.eventAction(event.id, action);
      setEvent(updated);
      setConfirmingEscalate(false);
      showToast(msg);
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Action failed");
    }
  };

  const replay = async (did: string) => {
    try {
      const updated = await api.replay(did);
      setDeliveries((prev) => prev.map((d) => (d.id === did ? updated : d)));
      setReplayed((r) => ({ ...r, [did]: true }));
      showToast("Delivery re-queued");
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Replay failed");
    }
  };

  const back = (
    <div
      onClick={() => nav("/events")}
      style={{ display: "inline-flex", alignItems: "center", gap: 6, fontSize: 13, color: c.muted, cursor: "pointer" }}
    >
      <svg width="12" height="12" viewBox="0 0 16 16" fill="none">
        <path d="M10 3 5 8l5 5" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
      Events
    </div>
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
          <ErrorState message={error ?? "Event not found"} onRetry={load} />
        </div>
      </div>
    );
  }

  const rules = eventActionRules[event.state] ?? eventActionRules.new;

  return (
    <div style={{ maxWidth: 900 }}>
      {back}

      <div style={{ marginTop: 14 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 10, flexWrap: "wrap" }}>
          <Badge kind="severity" value={event.severity} />
          <Badge kind="state" value={event.state} pulse={event.state === "escalated"} />
          <span style={{ fontFamily: mono, fontSize: 12.5, color: c.muted }}>{fullTime(event.created_at)}</span>
        </div>
        <h1 style={{ margin: "12px 0 0", fontSize: 24, fontWeight: 600, letterSpacing: "-0.01em", lineHeight: 1.3 }}>
          {event.message}
        </h1>
      </div>

      {/* Actions */}
      <div style={{ display: "flex", gap: 10, flexWrap: "wrap", marginTop: 20 }}>
        <ActionButton label="Acknowledge" enabled={rules.ack} onClick={() => doAction("ack", "Event acknowledged")} />
        <ActionButton label="Resolve" enabled={rules.resolve} onClick={() => doAction("resolve", "Event resolved")} />
        {event.state === "muted" ? (
          <ActionButton label="Unmute" enabled={rules.unmute} onClick={() => doAction("unmute", "Event unmuted")} />
        ) : (
          <ActionButton label="Mute" enabled={rules.mute} onClick={() => doAction("mute", "Event muted")} />
        )}
        <ActionButton
          label="Escalate"
          enabled={rules.escalate}
          danger
          onClick={() => setConfirmingEscalate(true)}
        />
      </div>

      {confirmingEscalate && (
        <div
          style={{
            marginTop: 12,
            padding: "12px 16px",
            borderRadius: 8,
            background: "#241a2e",
            border: "1px solid #4c3a68",
            display: "flex",
            alignItems: "center",
            gap: 14,
          }}
        >
          <span style={{ fontSize: 13, flex: 1 }}>Escalate this event? This is a semi-destructive action.</span>
          <div onClick={() => setConfirmingEscalate(false)} style={{ fontSize: 13, color: c.muted, cursor: "pointer" }}>
            Cancel
          </div>
          <div
            onClick={() => doAction("escalate", "Event escalated")}
            style={{ padding: "6px 14px", borderRadius: 6, background: c.accent, color: c.accentInk, fontSize: 13, fontWeight: 600, cursor: "pointer" }}
          >
            Confirm escalate
          </div>
        </div>
      )}

      {/* Metadata */}
      <Card style={{ marginTop: 32, padding: "20px 22px" }}>
        <SectionTitle>Metadata</SectionTitle>
        <div className="al-meta" style={{ display: "grid", gridTemplateColumns: "150px 1fr", gap: "10px 16px", fontSize: 13.5 }}>
          <Meta label="ID">
            <CopyValue value={event.id} display={copied === "id" ? "Copied ✓" : event.id} active={copied === "id"} onClick={() => copy(event.id, "id")} />
          </Meta>
          <Meta label="Source">{event.source}</Meta>
          <Meta label="Category">{event.category || "—"}</Meta>
          <Meta label="Entity">{event.entity_type ? `${event.entity_type} / ${event.entity_id}` : "—"}</Meta>
          <Meta label="Trace ID">
            {event.trace_id ? (
              <CopyValue value={event.trace_id} display={copied === "trace" ? "Copied ✓" : event.trace_id} active={copied === "trace"} onClick={() => copy(event.trace_id!, "trace")} />
            ) : (
              <span style={{ color: c.muted2, fontFamily: mono }}>—</span>
            )}
          </Meta>
          <Meta label="Dedupe key">
            {event.dedupe_key ? (
              <CopyValue value={event.dedupe_key} display={copied === "dedupe" ? "Copied ✓" : event.dedupe_key} active={copied === "dedupe"} onClick={() => copy(event.dedupe_key!, "dedupe")} />
            ) : (
              <span style={{ color: c.muted2, fontFamily: mono }}>—</span>
            )}
          </Meta>
          <Meta label="Created">
            <span style={{ fontFamily: mono }}>{fullTime(event.created_at)}</span>
          </Meta>
          <Meta label="Updated">
            <span style={{ fontFamily: mono }}>{fullTime(event.updated_at)}</span>
          </Meta>
        </div>
      </Card>

      {/* Payload */}
      <Card style={{ marginTop: 20, padding: "20px 22px" }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
          <SectionTitle noMargin>Payload</SectionTitle>
          <div style={{ display: "flex", gap: 14 }}>
            <div onClick={() => setPayloadExpanded((v) => !v)} style={{ fontSize: 12.5, color: c.accent, cursor: "pointer" }}>
              {payloadExpanded ? "Collapse" : "Expand"}
            </div>
            <div
              onClick={() => {
                copy(JSON.stringify(event.payload ?? {}, null, 2), "payload");
              }}
              style={{ fontSize: 12.5, color: c.accent, cursor: "pointer" }}
            >
              {copied === "payload" ? "Copied ✓" : "Copy"}
            </div>
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
          <div style={{ overflowX: "auto", marginTop: 14 }}>
            <table style={{ width: "100%", minWidth: 640, fontSize: 13.5 }}>
              <thead>
                <tr>
                  {["Channel", "State", "Attempts", "Next retry", "Last error", "Updated", ""].map((h, i) => (
                    <th key={i} style={th}>
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {deliveries.map((d) => (
                  <tr key={d.id}>
                    <td style={{ ...td, color: c.text2, whiteSpace: "nowrap" }}>
                      {d.channel} <span style={{ color: c.muted }}>/</span>{" "}
                      <code style={{ fontFamily: mono, fontSize: 12.5 }}>{d.channel_name}</code>
                    </td>
                    <td style={td}>
                      <Badge kind="delivery" value={d.state} />
                    </td>
                    <td style={{ ...tdMono, color: c.text2 }}>
                      {d.attempts} / {d.max_attempts}
                    </td>
                    <td style={tdMono}>{d.next_retry_at ? timeOfDay(d.next_retry_at) : "—"}</td>
                    <td style={{ ...tdMono, maxWidth: 200, whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }} title={d.last_error}>
                      {d.last_error || "—"}
                    </td>
                    <td style={tdMono}>{timeOfDay(d.updated_at)}</td>
                    <td style={{ ...td, textAlign: "right" }}>
                      {replayed[d.id] ? (
                        <span style={{ fontSize: 12, color: c.ok }}>Queued ✓</span>
                      ) : d.state === "dead_letter" ? (
                        <div
                          onClick={() => replay(d.id)}
                          style={{ display: "inline-block", padding: "5px 12px", borderRadius: 6, background: c.accent, color: c.accentInk, fontSize: 12.5, fontWeight: 600, cursor: "pointer" }}
                        >
                          Replay
                        </div>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
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
  enabled,
  onClick,
  danger,
}: {
  label: string;
  enabled: boolean;
  onClick: () => void;
  danger?: boolean;
}) {
  return (
    <div
      onClick={enabled ? onClick : undefined}
      style={{
        padding: "8px 16px",
        borderRadius: 7,
        fontSize: 13,
        fontWeight: danger ? 600 : 500,
        background: danger && enabled ? "rgba(252,165,165,0.08)" : c.card,
        border: `1px solid ${danger && enabled ? "#7a3a3a" : c.border}`,
        color: enabled ? (danger ? c.danger : c.text2) : c.muted3,
        cursor: enabled ? "pointer" : "not-allowed",
      }}
    >
      {label}
    </div>
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
      <div>{children}</div>
    </>
  );
}

function CopyValue({ value, display, active, onClick }: { value: string; display: string; active: boolean; onClick: () => void }) {
  return (
    <span onClick={onClick} title={value} style={{ fontFamily: mono, cursor: "pointer", color: active ? c.ok : c.muted }}>
      {display}
    </span>
  );
}
