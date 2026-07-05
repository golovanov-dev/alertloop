import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { api } from "../api";
import { timeOfDay } from "../format";
import { c, mono, typeLabels } from "../theme";
import type { AlertEvent } from "../types";
import { Badge, Card, ErrorState, Loading, td, tdMono, th } from "../ui";

const typeTabs = [
  { key: "", label: "All" },
  { key: "incident", label: "Incidents" },
  { key: "business_event", label: "Business events" },
  { key: "audit", label: "Audit" },
];

export function Events() {
  const nav = useNavigate();
  const [params] = useSearchParams();

  const [type, setType] = useState("");
  const [severity, setSeverity] = useState("");
  const [state, setState] = useState(params.get("state") ?? "");
  const [source, setSource] = useState("");
  const [search, setSearch] = useState("");
  const [live, setLive] = useState(false);

  const [items, setItems] = useState<AlertEvent[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const seq = useRef(0);

  const loadFirst = useCallback(async () => {
    const mySeq = ++seq.current;
    setLoading(true);
    setError(null);
    try {
      const page = await api.listEvents({ type, severity, state, source, limit: 50 });
      if (seq.current !== mySeq) return;
      setItems(page.items);
      setCursor(page.next_cursor);
    } catch (e) {
      if (seq.current !== mySeq) return;
      setError(e instanceof Error ? e.message : "Failed to load");
    } finally {
      if (seq.current === mySeq) setLoading(false);
    }
  }, [type, severity, state, source]);

  useEffect(() => {
    loadFirst();
  }, [loadFirst]);

  // Live auto-refresh of the first page.
  useEffect(() => {
    if (!live) return;
    const t = window.setInterval(loadFirst, 5000);
    return () => window.clearInterval(t);
  }, [live, loadFirst]);

  const loadMore = async () => {
    if (!cursor) return;
    setLoadingMore(true);
    try {
      const page = await api.listEvents({ type, severity, state, source, limit: 50, cursor });
      setItems((prev) => [...prev, ...page.items]);
      setCursor(page.next_cursor);
    } catch {
      /* keep existing items */
    } finally {
      setLoadingMore(false);
    }
  };

  // Message search is applied client-side over the loaded rows.
  const visible = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return items;
    return items.filter((e) => e.message.toLowerCase().includes(q));
  }, [items, search]);

  const filtersActive = !!type || !!severity || !!state || !!source || !!search;
  const clearFilters = () => {
    setType("");
    setSeverity("");
    setState("");
    setSource("");
    setSearch("");
  };
  const pauseAndSet = (fn: () => void) => {
    setLive(false);
    fn();
  };

  return (
    <div>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
        <h1 style={{ margin: 0, fontSize: 26, fontWeight: 600, letterSpacing: "-0.01em" }}>Events</h1>
        <div
          onClick={() => setLive((v) => !v)}
          title={live ? "Live — refreshes automatically. Click to pause." : "Paused — click to resume."}
          style={{
            display: "flex",
            alignItems: "center",
            gap: 7,
            padding: "6px 12px",
            borderRadius: 999,
            border: `1px solid ${c.border}`,
            background: c.card,
            cursor: "pointer",
            fontSize: 12.5,
            color: live ? c.ok : c.muted,
          }}
        >
          <span
            style={{
              width: 7,
              height: 7,
              borderRadius: "50%",
              background: live ? c.ok : c.muted2,
              animation: live ? "live-pulse 1.6s infinite" : undefined,
            }}
          />
          {live ? "Live" : "Paused"}
        </div>
      </div>

      {/* Type tabs */}
      <div
        style={{
          display: "flex",
          gap: 6,
          marginTop: 22,
          background: c.sidebar,
          border: `1px solid ${c.border}`,
          borderRadius: 9,
          padding: 4,
          width: "fit-content",
          maxWidth: "100%",
          overflowX: "auto",
        }}
      >
        {typeTabs.map((t) => (
          <div
            key={t.key}
            onClick={() => pauseAndSet(() => setType(t.key))}
            style={{
              padding: "7px 14px",
              borderRadius: 6,
              fontSize: 13,
              fontWeight: 500,
              cursor: "pointer",
              background: type === t.key ? c.accent : "transparent",
              color: type === t.key ? c.accentInk : c.muted,
            }}
          >
            {t.label}
          </div>
        ))}
      </div>

      {/* Filters */}
      <div style={{ display: "flex", gap: 10, flexWrap: "wrap", alignItems: "center", marginTop: 16 }}>
        <select value={severity} onChange={(e) => pauseAndSet(() => setSeverity(e.target.value))} style={{ minWidth: 130 }}>
          <option value="">All severities</option>
          {["info", "success", "warning", "error", "critical"].map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <select value={state} onChange={(e) => pauseAndSet(() => setState(e.target.value))} style={{ minWidth: 130 }}>
          <option value="">All states</option>
          {["new", "acknowledged", "resolved", "muted", "escalated"].map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <input
          value={source}
          onChange={(e) => pauseAndSet(() => setSource(e.target.value))}
          placeholder="Filter by source…"
          style={{ width: 160 }}
        />
        <input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search message…"
          style={{ flex: 1, minWidth: 200 }}
        />
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
            <ErrorState message={error} onRetry={loadFirst} />
          </div>
        ) : visible.length > 0 ? (
          <div className="al-table-scroll">
          <table style={{ width: "100%", minWidth: 720, fontSize: 13.5 }}>
            <thead>
              <tr>
                {["Time", "Type", "Severity", "State", "Source", "Message", "ID"].map((h) => (
                  <th key={h} style={th}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {visible.map((ev) => (
                <tr key={ev.id} onClick={() => nav(`/events/${ev.id}`)} style={{ cursor: "pointer" }}>
                  <td style={tdMono}>{timeOfDay(ev.created_at)}</td>
                  <td style={{ ...td, color: c.text2 }}>{typeLabels[ev.type] ?? ev.type}</td>
                  <td style={td}>
                    <Badge kind="severity" value={ev.severity} />
                  </td>
                  <td style={td}>
                    <Badge kind="state" value={ev.state} pulse={ev.state === "escalated"} />
                  </td>
                  <td style={{ ...td, color: c.text2 }}>{ev.source}</td>
                  <td
                    style={{ ...td, color: c.text2, maxWidth: 320, whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}
                    title={ev.message}
                  >
                    {ev.message}
                  </td>
                  <td style={{ ...tdMono, fontFamily: mono }}>{ev.id.slice(0, 8)}</td>
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

      <div style={{ marginTop: 18, textAlign: "center" }}>
        {cursor ? (
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
        ) : (
          !loading && visible.length > 0 && <div style={{ fontSize: 13, color: c.muted2 }}>No more events</div>
        )}
      </div>
    </div>
  );
}
