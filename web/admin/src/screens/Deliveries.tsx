import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { api } from "../api";
import { useApp } from "../context";
import { useDebounced } from "../hooks";
import { c } from "../theme";
import type { DeliveryAttempt } from "../types";
import { alertStates, Button, Card, DeliveriesTable, ErrorState, focusIfLost, Loading } from "../ui";

export function Deliveries() {
  const { showToast } = useApp();
  const [params] = useSearchParams();

  const [state, setState] = useState(params.get("state") ?? "");
  // A link to this screen (the menu, an Overview card) sets the state again:
  // the screen stays mounted when only the address changes.
  useEffect(() => {
    setState(params.get("state") ?? "");
  }, [params]);
  const [channel, setChannel] = useState("");
  const [channelName, setChannelName] = useState("");
  const [eventId, setEventId] = useState("");

  const [items, setItems] = useState<DeliveryAttempt[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [moreError, setMoreError] = useState<string | null>(null);
  const [replayed, setReplayed] = useState<Record<string, boolean>>({});
  const [replaying, setReplaying] = useState<string | null>(null);
  // Dead-lettered attempts beyond the loaded page: a recovery on this page may
  // be held by an alert that the page does not show.
  const [deadLetters, setDeadLetters] = useState<DeliveryAttempt[]>([]);
  const seq = useRef(0);
  const firstFilter = useRef<HTMLButtonElement>(null);
  const noMore = useRef<HTMLDivElement>(null);

  const channelNameQ = useDebounced(channelName.trim());
  const eventIdQ = useDebounced(eventId.trim());

  const load = useCallback(async () => {
    const mySeq = ++seq.current;
    setLoading(true);
    setError(null);
    setMoreError(null);
    try {
      const page = await api.listDeliveries({
        state,
        channel,
        channel_name: channelNameQ,
        event_id: eventIdQ,
        limit: 50,
      });
      if (seq.current !== mySeq) return;
      setItems(page.items);
      setCursor(page.next_cursor);
    } catch (e) {
      if (seq.current !== mySeq) return;
      setItems([]);
      setCursor(undefined);
      setError(e instanceof Error ? e.message : "Failed to load");
    } finally {
      if (seq.current === mySeq) setLoading(false);
    }
  }, [state, channel, channelNameQ, eventIdQ]);

  useEffect(() => {
    load();
  }, [load]);

  // The dead-letter lookup runs once per visit (Retry repeats it), not on every
  // filter change. It only adds "waits for its alert" to recoveries whose alert
  // is not on the page, so when it fails the screen works without that hint.
  const loadDeadLetters = useCallback(() => {
    api
      .listDeliveries({ state: "dead_letter", limit: 200 })
      .then((p) => setDeadLetters(p.items))
      .catch(() => setDeadLetters([]));
  }, []);
  useEffect(loadDeadLetters, [loadDeadLetters]);

  const loadMore = async () => {
    // While the first page reloads, the cursor belongs to the old list.
    if (!cursor || loadingMore || loading) return;
    setLoadingMore(true);
    setMoreError(null);
    const mySeq = seq.current;
    try {
      const page = await api.listDeliveries({
        state,
        channel,
        channel_name: channelNameQ,
        event_id: eventIdQ,
        limit: 50,
        cursor,
      });
      if (seq.current !== mySeq) return;
      setItems((prev) => [...prev, ...page.items]);
      setCursor(page.next_cursor);
    } catch (e) {
      if (seq.current === mySeq) setMoreError(e instanceof Error ? e.message : "Failed to load more");
    } finally {
      setLoadingMore(false);
    }
  };
  // On the last page Load more gives way to "No more deliveries", which takes
  // the focus if the button had it.
  useEffect(() => {
    if (!cursor) focusIfLost(noMore.current);
  }, [cursor]);

  // Loaded rows are fresher than the dead-letter lookup, so they win.
  const alerts = useMemo(() => alertStates([...deadLetters, ...items]), [deadLetters, items]);

  const replay = async (id: string) => {
    setReplaying(id);
    try {
      const updated = await api.replay(id);
      setItems((prev) => prev.map((d) => (d.id === id ? updated : d)));
      setReplayed((r) => ({ ...r, [id]: true }));
      showToast("Delivery re-queued");
    } catch (e) {
      showToast(e instanceof Error ? e.message : "Replay failed");
    } finally {
      setReplaying(null);
    }
  };

  const filtersActive = !!state || !!channel || !!channelName || !!eventId;
  const clearFilters = () => {
    setState("");
    setChannel("");
    setChannelName("");
    setEventId("");
    // The button goes away with the filters; the focus goes to the first one.
    firstFilter.current?.focus();
  };
  const isDeadLetterPreset = state === "dead_letter";

  return (
    <div>
      <h1 style={{ margin: 0, fontSize: 26, fontWeight: 600, letterSpacing: "-0.01em" }}>Deliveries</h1>
      <div style={{ marginTop: 6, fontSize: 14, color: c.muted }}>
        All delivery attempts across events, by channel.
      </div>

      <div style={{ display: "flex", gap: 10, flexWrap: "wrap", alignItems: "center", marginTop: 24 }}>
        <Button
          ref={firstFilter}
          aria-pressed={isDeadLetterPreset}
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
        </Button>
        <select aria-label="State" value={state} onChange={(e) => setState(e.target.value)} style={{ minWidth: 130 }}>
          <option value="">All states</option>
          {["pending", "sending", "sent", "failed", "dead_letter", "cancelled"].map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <select aria-label="Channel type" value={channel} onChange={(e) => setChannel(e.target.value)} style={{ minWidth: 130 }}>
          <option value="">All channel types</option>
          {["email", "telegram", "webhook"].map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <input
          value={channelName}
          onChange={(e) => setChannelName(e.target.value)}
          aria-label="Channel name"
          placeholder="Channel name (exact)…"
          title="Matches the whole channel name exactly"
          style={{ width: 170 }}
        />
        <input
          value={eventId}
          onChange={(e) => setEventId(e.target.value)}
          aria-label="Event ID"
          placeholder="Full event ID…"
          title="The full event ID, as on the event page"
          style={{ width: 170 }}
        />
        {filtersActive && (
          <Button onClick={clearFilters} style={{ fontSize: 13, color: c.accent, padding: "8px 4px" }}>
            Clear filters
          </Button>
        )}
      </div>

      <Card style={{ marginTop: 20, overflow: "hidden" }}>
        {loading ? (
          <div style={{ padding: 4 }}>
            <Loading />
          </div>
        ) : error ? (
          <div style={{ padding: 20 }}>
            <ErrorState
              message={error}
              onRetry={() => {
                loadDeadLetters();
                load();
              }}
            />
          </div>
        ) : items.length > 0 ? (
          <DeliveriesTable
            items={items}
            alerts={alerts}
            replayed={replayed}
            replaying={replaying}
            onReplay={replay}
            showEvent
          />
        ) : (
          <div style={{ padding: "60px 20px", textAlign: "center" }}>
            <div style={{ fontSize: 14, color: c.muted }}>
              {filtersActive
                ? "Nothing found for this filter."
                : "No deliveries yet. One appears for each channel an event is routed to."}
            </div>
            {filtersActive && (
              <Button onClick={clearFilters} style={{ marginTop: 14, fontSize: 13, color: c.accent }}>
                Clear filters
              </Button>
            )}
          </div>
        )}
      </Card>

      {!cursor && !loading && items.length > 0 && (
        <div ref={noMore} tabIndex={-1} style={{ marginTop: 18, textAlign: "center", fontSize: 13, color: c.muted2, outline: "none" }}>
          No more deliveries
        </div>
      )}
      {cursor && !loading && (
        <div style={{ marginTop: 18, textAlign: "center" }}>
          <Button
            onClick={loadMore}
            style={{
              display: "inline-block",
              padding: "9px 20px",
              borderRadius: 7,
              border: `1px solid ${c.border}`,
              background: c.card,
              color: c.text2,
              fontSize: 13.5,
            }}
          >
            {loadingMore ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}
      {moreError && (
        <div style={{ marginTop: 10, textAlign: "center", fontSize: 13, color: c.danger }}>{moreError}</div>
      )}
    </div>
  );
}
