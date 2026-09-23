import { Fragment, useCallback, useEffect, useRef, useState, type MouseEvent, type ReactNode } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { api } from "../api";
import { exactTime } from "../format";
import { useDebounced, useMediaQuery } from "../hooks";
import { c, mono, typeLabels } from "../theme";
import type { AlertEvent } from "../types";
import { Badge, Button, Card, ErrorState, focusIfLost, Loading, NARROW, td, tdMono, th, Time } from "../ui";

const typeTabs = [
  { key: "", label: "All" },
  { key: "incident", label: "Incidents" },
  { key: "business_event", label: "Business events" },
  { key: "audit", label: "Audit" },
];

export function Events() {
  const [params] = useSearchParams();
  const narrow = useMediaQuery(NARROW);
  const navigate = useNavigate();

  const [type, setType] = useState(params.get("type") ?? "");
  const [severity, setSeverity] = useState("");
  const [state, setState] = useState(params.get("state") ?? "");
  // A link to this screen (the menu, an Overview card) sets the filters again:
  // the screen stays mounted when only the address changes.
  useEffect(() => {
    setType(params.get("type") ?? "");
    setState(params.get("state") ?? "");
  }, [params]);
  const [source, setSource] = useState("");
  const [search, setSearch] = useState("");
  const [live, setLive] = useState(false);

  const [items, setItems] = useState<AlertEvent[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [moreError, setMoreError] = useState<string | null>(null);
  const seq = useRef(0);
  // The load whose next_cursor `cursor` holds. Load more waits while a newer
  // first page is on its way, so it never joins that page to an old cursor.
  const cursorSeq = useRef(0);
  const firstFilter = useRef<HTMLSelectElement>(null);
  const noMore = useRef<HTMLDivElement>(null);

  // Text filters go to the server once typing pauses.
  const sourceQ = useDebounced(source.trim());
  const q = useDebounced(search.trim());

  // quiet (the Live refresh) keeps the list on screen while the page loads.
  const loadFirst = useCallback(async (quiet = false) => {
    const mySeq = ++seq.current;
    if (!quiet) setLoading(true);
    setError(null);
    setMoreError(null);
    try {
      const page = await api.listEvents({ type, severity, state, source: sourceQ, q, limit: 50 });
      if (seq.current !== mySeq) return;
      setItems(page.items);
      setCursor(page.next_cursor);
      cursorSeq.current = mySeq;
    } catch (e) {
      if (seq.current !== mySeq) return;
      setItems([]);
      setCursor(undefined);
      setError(e instanceof Error ? e.message : "Failed to load");
    } finally {
      if (seq.current === mySeq) setLoading(false);
    }
  }, [type, severity, state, sourceQ, q]);

  useEffect(() => {
    loadFirst();
  }, [loadFirst]);

  // Live refreshes the first page. Load more turns it off, so the rows it
  // added are not replaced by the first page five seconds later.
  useEffect(() => {
    if (!live) return;
    const t = window.setInterval(() => loadFirst(true), 5000);
    return () => window.clearInterval(t);
  }, [live, loadFirst]);

  const loadMore = async () => {
    // While the first page reloads, the cursor belongs to the old list.
    if (!cursor || loadingMore || loading || cursorSeq.current !== seq.current) return;
    setLive(false);
    setLoadingMore(true);
    setMoreError(null);
    const mySeq = seq.current;
    try {
      const page = await api.listEvents({ type, severity, state, source: sourceQ, q, limit: 50, cursor });
      if (seq.current !== mySeq) return;
      setItems((prev) => [...prev, ...page.items]);
      setCursor(page.next_cursor);
    } catch (e) {
      if (seq.current === mySeq) setMoreError(e instanceof Error ? e.message : "Failed to load more");
    } finally {
      setLoadingMore(false);
    }
  };
  // On the last page Load more gives way to "No more events", which takes the
  // focus if the button had it.
  useEffect(() => {
    if (!cursor) focusIfLost(noMore.current);
  }, [cursor]);

  const filtersActive = !!type || !!severity || !!state || !!source || !!search;
  const clearFilters = () => {
    setType("");
    setSeverity("");
    setState("");
    setSource("");
    setSearch("");
    // The button goes away with the filters; the focus goes to the first one.
    firstFilter.current?.focus();
  };
  // A mouse click on the rest of the row opens the event as its link does. The
  // keyboard uses the link itself; selecting text in a row does not navigate.
  // The middle button and Ctrl+click open it in a new tab.
  const openRow = (e: MouseEvent, id: string) => {
    if ((e.target as Element).closest("a, button") || window.getSelection()?.toString()) return;
    const to = `/events/${id}`;
    if (e.ctrlKey || e.metaKey || e.button === 1) window.open(`#${to}`, "_blank", "noopener");
    else navigate(to);
  };
  const pauseAndSet = (fn: () => void) => {
    setLive(false);
    fn();
  };

  // On a narrow screen the message and the state come first and the message
  // wraps; the rest is behind the horizontal scroll.
  const cols: Record<string, { h: string; cell: (ev: AlertEvent) => ReactNode }> = {
    time: {
      h: "Time (local)",
      cell: (ev) => (
        <td style={tdMono}>
          <Time ts={ev.created_at} />
        </td>
      ),
    },
    type: { h: "Type", cell: (ev) => <td style={{ ...td, color: c.text2 }}>{typeLabels[ev.type] ?? ev.type}</td> },
    severity: {
      h: "Severity",
      cell: (ev) => (
        <td style={td}>
          <Badge kind="severity" value={ev.severity} />
        </td>
      ),
    },
    state: {
      h: "State",
      cell: (ev) => (
        <td style={td}>
          <Badge kind="state" value={ev.state} pulse={ev.state === "escalated"} />
        </td>
      ),
    },
    source: { h: "Source", cell: (ev) => <td style={{ ...td, color: c.text2 }}>{ev.source}</td> },
    message: {
      h: "Message",
      cell: (ev) => (
        <td
          style={
            narrow
              ? { ...td, color: c.text2, minWidth: 160, maxWidth: 200, overflowWrap: "anywhere" }
              : { ...td, color: c.text2, maxWidth: 320 }
          }
          title={ev.message}
        >
          {/* The exact time is in the Time title for the mouse and in the
              link's description for the keyboard and screen readers. The link
              clips the message itself, so the cell does not clip its focus ring. */}
          <Link
            to={`/events/${ev.id}`}
            aria-describedby={`al-t-${ev.id}`}
            style={
              narrow
                ? { color: "inherit", textDecoration: "none" }
                : { color: "inherit", textDecoration: "none", display: "block", whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }
            }
          >
            {ev.message}
          </Link>
          <span id={`al-t-${ev.id}`} hidden>
            {`Created ${exactTime(ev.created_at)}`}
          </span>
        </td>
      ),
    },
    id: { h: "ID", cell: (ev) => <td style={{ ...tdMono, fontFamily: mono }}>{ev.id.slice(0, 8)}</td> },
  };
  const order = narrow
    ? ["message", "state", "severity", "time", "type", "source", "id"]
    : ["time", "type", "severity", "state", "source", "message", "id"];

  return (
    <div>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
        <h1 style={{ margin: 0, fontSize: 26, fontWeight: 600, letterSpacing: "-0.01em" }}>Events</h1>
        <Button
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
        </Button>
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
          <Button
            key={t.key}
            aria-pressed={type === t.key}
            onClick={() => pauseAndSet(() => setType(t.key))}
            style={{
              padding: "7px 14px",
              borderRadius: 6,
              fontSize: 13,
              fontWeight: 500,
              whiteSpace: "nowrap",
              background: type === t.key ? c.accent : "transparent",
              color: type === t.key ? c.accentInk : c.muted,
            }}
          >
            {t.label}
          </Button>
        ))}
      </div>

      {/* Filters */}
      <div style={{ display: "flex", gap: 10, flexWrap: "wrap", alignItems: "center", marginTop: 16 }}>
        <select ref={firstFilter} aria-label="Severity" value={severity} onChange={(e) => pauseAndSet(() => setSeverity(e.target.value))} style={{ minWidth: 130 }}>
          <option value="">All severities</option>
          {["info", "success", "warning", "error", "critical"].map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <select aria-label="State" value={state} onChange={(e) => pauseAndSet(() => setState(e.target.value))} style={{ minWidth: 130 }}>
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
          placeholder="Source (exact match)…"
          aria-label="Source"
          title="Matches the whole source name exactly"
          style={{ width: 160 }}
        />
        <input
          value={search}
          onChange={(e) => pauseAndSet(() => setSearch(e.target.value))}
          maxLength={200}
          placeholder="Search message…"
          aria-label="Search message"
          style={{ flex: 1, minWidth: 200 }}
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
            <ErrorState message={error} onRetry={() => loadFirst()} />
          </div>
        ) : items.length > 0 ? (
          <div className="al-table-scroll">
            <table style={{ width: "100%", minWidth: 720, fontSize: 13.5 }}>
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
                {items.map((ev) => (
                  <tr key={ev.id} onClick={(e) => openRow(e, ev.id)} onAuxClick={(e) => e.button === 1 && openRow(e, ev.id)} style={{ cursor: "pointer" }}>
                    {order.map((k) => (
                      <Fragment key={k}>{cols[k].cell(ev)}</Fragment>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <div style={{ padding: "60px 20px", textAlign: "center" }}>
            {filtersActive ? (
              <div style={{ fontSize: 14, color: c.muted }}>Nothing found for this filter.</div>
            ) : (
              <div style={{ fontSize: 14, color: c.muted }}>
                No events yet. Send the first one with <code style={{ fontFamily: mono }}>POST /v1/events</code>:{" "}
                <a href="https://github.com/golovanov-dev/alertloop#sending-events" target="_blank" rel="noreferrer" style={{ color: c.accent }}>
                  Sending events
                </a>
                .
              </div>
            )}
            {filtersActive && (
              <Button onClick={clearFilters} style={{ marginTop: 14, fontSize: 13, color: c.accent }}>
                Clear filters
              </Button>
            )}
          </div>
        )}
      </Card>

      <div style={{ marginTop: 18, textAlign: "center" }}>
        {loading ? null : cursor ? (
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
        ) : (
          !loading &&
          items.length > 0 && (
            <div ref={noMore} tabIndex={-1} style={{ fontSize: 13, color: c.muted2, outline: "none" }}>
              No more events
            </div>
          )
        )}
        {moreError && <div style={{ marginTop: 10, fontSize: 13, color: c.danger }}>{moreError}</div>}
      </div>
    </div>
  );
}
