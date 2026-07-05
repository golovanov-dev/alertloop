import { useCallback, useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { api } from "../api";
import { useApp } from "../context";
import { relToLabel, timeOfDay } from "../format";
import { c, mono } from "../theme";
import type { DeliveryAttempt } from "../types";
import { Badge, Card, ErrorState, Loading, td, tdMono, th } from "../ui";

export function Deliveries() {
  const { showToast } = useApp();
  const [params] = useSearchParams();

  const [state, setState] = useState(params.get("state") ?? "");
  const [channel, setChannel] = useState("");
  const [channelName, setChannelName] = useState("");
  const [eventId, setEventId] = useState("");

  const [items, setItems] = useState<DeliveryAttempt[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [replayed, setReplayed] = useState<Record<string, boolean>>({});
  const seq = useRef(0);

  const load = useCallback(async () => {
    const mySeq = ++seq.current;
    setLoading(true);
    setError(null);
    try {
      const page = await api.listDeliveries({
        state,
        channel,
        channel_name: channelName,
        event_id: eventId,
        limit: 50,
      });
      if (seq.current !== mySeq) return;
      setItems(page.items);
      setCursor(page.next_cursor);
    } catch (e) {
      if (seq.current !== mySeq) return;
      setError(e instanceof Error ? e.message : "Failed to load");
    } finally {
      if (seq.current === mySeq) setLoading(false);
    }
  }, [state, channel, channelName, eventId]);

  useEffect(() => {
    load();
  }, [load]);

  const loadMore = async () => {
    if (!cursor) return;
    setLoadingMore(true);
    try {
      const page = await api.listDeliveries({
        state,
        channel,
        channel_name: channelName,
        event_id: eventId,
        limit: 50,
        cursor,
      });
      setItems((prev) => [...prev, ...page.items]);
      setCursor(page.next_cursor);
    } catch {
      /* keep existing */
    } finally {
      setLoadingMore(false);
    }
  };

  const replay = async (id: string) => {
    try {
      const updated = await api.replay(id);
      setItems((prev) => prev.map((d) => (d.id === id ? updated : d)));
      setReplayed((r) => ({ ...r, [id]: true }));
      showToast("Delivery re-queued");
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Replay failed");
    }
  };

  const filtersActive = !!state || !!channel || !!channelName || !!eventId;
  const clearFilters = () => {
    setState("");
    setChannel("");
    setChannelName("");
    setEventId("");
  };
  const isDeadLetterPreset = state === "dead_letter";

  return (
    <div>
      <h1 style={{ margin: 0, fontSize: 26, fontWeight: 600, letterSpacing: "-0.01em" }}>Deliveries</h1>
      <div style={{ marginTop: 6, fontSize: 14, color: c.muted }}>
        All delivery attempts across events, by channel.
      </div>

      <div style={{ display: "flex", gap: 10, flexWrap: "wrap", alignItems: "center", marginTop: 24 }}>
        <div
          onClick={() => setState((s) => (s === "dead_letter" ? "" : "dead_letter"))}
          style={{
            display: "flex",
            alignItems: "center",
            gap: 7,
            padding: "7px 14px",
            borderRadius: 999,
            fontSize: 13,
            fontWeight: 500,
            cursor: "pointer",
            background: isDeadLetterPreset ? c.accent : "transparent",
            color: isDeadLetterPreset ? c.accentInk : c.text2,
            border: `1px solid ${isDeadLetterPreset ? c.accent : c.border}`,
          }}
        >
          Dead letter
        </div>
        <select value={state} onChange={(e) => setState(e.target.value)} style={{ minWidth: 130 }}>
          <option value="">All states</option>
          {["pending", "sending", "sent", "failed", "dead_letter"].map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <select value={channel} onChange={(e) => setChannel(e.target.value)} style={{ minWidth: 130 }}>
          <option value="">All channel types</option>
          {["email", "telegram", "webhook"].map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <input value={channelName} onChange={(e) => setChannelName(e.target.value)} placeholder="Channel name…" style={{ width: 150 }} />
        <input value={eventId} onChange={(e) => setEventId(e.target.value)} placeholder="Event ID…" style={{ width: 150 }} />
        {filtersActive && (
          <div onClick={clearFilters} style={{ fontSize: 13, color: c.accent, cursor: "pointer", padding: "8px 4px" }}>
            Clear filters
          </div>
        )}
      </div>

      <Card style={{ marginTop: 20, overflow: "hidden" }}>
        {loading ? (
          <div style={{ padding: 4 }}>
            <Loading />
          </div>
        ) : error ? (
          <div style={{ padding: 20 }}>
            <ErrorState message={error} onRetry={load} />
          </div>
        ) : items.length > 0 ? (
          <div style={{ overflowX: "auto" }}>
            <table style={{ width: "100%", minWidth: 920, fontSize: 13.5 }}>
              <thead>
                <tr>
                  {["Channel", "Event", "State", "Attempts", "Next retry", "Last error", "Updated", ""].map((h, i) => (
                    <th key={i} style={th}>
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {items.map((d) => (
                  <tr key={d.id}>
                    <td style={{ ...td, color: c.text2, whiteSpace: "nowrap" }}>
                      {d.channel} <span style={{ color: c.muted }}>/</span>{" "}
                      <code style={{ fontFamily: mono, fontSize: 12.5 }}>{d.channel_name}</code>
                    </td>
                    <td style={tdMono}>{d.event_id.slice(0, 8)}</td>
                    <td style={td}>
                      <Badge kind="delivery" value={d.state} />
                    </td>
                    <td style={{ ...tdMono, color: c.text2 }}>
                      {d.attempts} / {d.max_attempts}
                    </td>
                    <td style={tdMono}>{relToLabel(d.next_retry_at)}</td>
                    <td
                      style={{ ...tdMono, maxWidth: 220, whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}
                      title={d.last_error}
                    >
                      {d.last_error || "—"}
                    </td>
                    <td style={tdMono}>{timeOfDay(d.updated_at)}</td>
                    <td style={{ ...td, textAlign: "right" }}>
                      {replayed[d.id] ? (
                        <span style={{ fontSize: 12, color: c.ok }}>Queued ✓</span>
                      ) : d.state === "dead_letter" ? (
                        <div
                          onClick={() => replay(d.id)}
                          style={{
                            display: "inline-block",
                            padding: "5px 12px",
                            borderRadius: 6,
                            background: c.accent,
                            color: c.accentInk,
                            fontSize: 12.5,
                            fontWeight: 600,
                            cursor: "pointer",
                          }}
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
          <div style={{ padding: "60px 20px", textAlign: "center" }}>
            <div style={{ fontSize: 14, color: c.muted }}>Nothing found for this filter.</div>
            {filtersActive && (
              <div onClick={clearFilters} style={{ marginTop: 14, fontSize: 13, color: c.accent, cursor: "pointer" }}>
                Clear filters
              </div>
            )}
          </div>
        )}
      </Card>

      {cursor && (
        <div style={{ marginTop: 18, textAlign: "center" }}>
          <div
            onClick={loadMore}
            style={{
              display: "inline-block",
              padding: "9px 20px",
              borderRadius: 7,
              border: `1px solid ${c.border}`,
              background: c.card,
              color: c.text2,
              fontSize: 13.5,
              cursor: "pointer",
            }}
          >
            {loadingMore ? "Loading…" : "Load more"}
          </div>
        </div>
      )}
    </div>
  );
}
