import { Link } from "react-router-dom";
import { api } from "../api";
import { Badge, Card, ErrorState, Loading, Time } from "../ui";
import { useAsync } from "../hooks";
import { c, mono } from "../theme";
import type { AlertEvent, Stats } from "../types";

export function Overview() {
  const { data, loading, error, reload } = useAsync<{ stats: Stats; critical: AlertEvent[] }>(
    async () => {
      const [stats, critical] = await Promise.all([
        api.stats(),
        api.listEvents({ severity: "critical", limit: 4 }),
      ]);
      return { stats, critical: critical.items };
    },
    [],
  );

  // Business events and audit entries stay `new` for good, so open incidents
  // come from the server's own count, not from events["new"]. While the stats
  // load, or when they fail, the cards show "—", not 0. A state absent from a
  // count map has no rows, so it is 0.
  const stats = data?.stats;
  const openIncidents = stats ? stats.open_incidents : null;
  const escalatedCount = stats ? (stats.events["escalated"] ?? 0) : null;
  const pending = stats ? (stats.deliveries["pending"] ?? 0) : null;
  const failed = stats ? (stats.deliveries["failed"] ?? 0) : null;
  const deadLetterCount = stats ? (stats.deliveries["dead_letter"] ?? 0) : null;

  return (
    <div>
      <h1 style={{ margin: 0, fontSize: 26, fontWeight: 600, letterSpacing: "-0.01em" }}>
        Overview
      </h1>

      <div className="al-cards" style={{ marginTop: 28 }}>
        <StatCard
          value={openIncidents}
          label="Open incidents"
          cta="All incidents, incl. resolved →"
          to="/events?type=incident"
        />
        <StatCard
          value={escalatedCount}
          label="Escalated"
          color="#dc6c6c"
          cta="View events →"
          to="/events?state=escalated"
        />
        <StatCard
          value={pending === null || failed === null ? null : pending + failed}
          label="Delivery problems"
          detail={pending === null ? undefined : `pending ${pending} · failed ${failed}`}
          cta="View deliveries →"
          to={`/deliveries?state=${failed ? "failed" : "pending"}`}
        />
        <StatCard
          value={deadLetterCount}
          label="Dead-letter deliveries"
          color="#c99a2e"
          cta="View deliveries →"
          to="/deliveries?state=dead_letter"
        />
      </div>

      <div style={{ marginTop: 40 }}>
        <h2
          style={{
            margin: "0 0 12px",
            fontSize: 14,
            fontWeight: 600,
            color: c.muted,
            textTransform: "uppercase",
            letterSpacing: ".04em",
          }}
        >
          Recent critical events{" "}
          <span style={{ textTransform: "none", letterSpacing: 0, fontWeight: 400 }}>(local time)</span>
        </h2>
        {loading ? (
          <Loading />
        ) : error ? (
          <ErrorState message={error} onRetry={reload} />
        ) : data && data.critical.length > 0 ? (
          <Card style={{ overflow: "hidden" }}>
            {data.critical.map((ev) => (
              <Link
                key={ev.id}
                to={`/events/${ev.id}`}
                className="al-crit-row"
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 16,
                  padding: "13px 18px",
                  borderBottom: `1px solid ${c.rowBorder}`,
                  color: c.text,
                  textDecoration: "none",
                }}
              >
                <span
                  className="al-crit-time"
                  style={{ fontFamily: mono, fontSize: 12.5, color: c.muted, width: 150, flexShrink: 0, whiteSpace: "nowrap" }}
                >
                  <Time ts={ev.created_at} />
                </span>
                <Badge kind="severity" value={ev.severity} />
                <span
                  className="al-crit-msg"
                  style={{
                    flex: 1,
                    fontSize: 14,
                    whiteSpace: "nowrap",
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                  }}
                  title={ev.message}
                >
                  {ev.message}
                </span>
                <span style={{ fontSize: 12.5, color: c.muted, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                  {ev.source}
                </span>
              </Link>
            ))}
          </Card>
        ) : (
          <Card style={{ padding: "40px 20px", textAlign: "center", color: c.muted, fontSize: 13.5 }}>
            No critical events.
          </Card>
        )}
      </div>
    </div>
  );
}

function StatCard({
  value,
  label,
  detail,
  cta,
  to,
  color,
}: {
  value: number | null;
  label: string;
  detail?: string;
  cta: string;
  to: string;
  color?: string;
}) {
  return (
    <Link
      to={to}
      style={{
        flex: 1,
        display: "block",
        padding: "20px 22px",
        background: c.card,
        border: `1px solid ${c.border}`,
        borderRadius: 10,
        color: c.text,
        textDecoration: "none",
      }}
    >
      <div style={{ fontSize: 32, fontWeight: 600, color: value === null ? c.muted : (color ?? c.text) }}>{value ?? "—"}</div>
      <div style={{ marginTop: 6, fontSize: 13, color: c.muted }}>{label}</div>
      {detail && <div style={{ marginTop: 2, fontSize: 12, color: c.muted2 }}>{detail}</div>}
      <div style={{ marginTop: 10, fontSize: 12.5, color: c.accent }}>{cta}</div>
    </Link>
  );
}
