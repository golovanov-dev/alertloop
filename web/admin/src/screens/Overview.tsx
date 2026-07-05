import { useNavigate } from "react-router-dom";
import { api } from "../api";
import { Badge, Card, ErrorState, Loading } from "../ui";
import { useAsync } from "../hooks";
import { timeOfDay } from "../format";
import { c, mono } from "../theme";
import type { AlertEvent, Stats } from "../types";

export function Overview() {
  const nav = useNavigate();
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

  const newCount = data?.stats.events["new"] ?? 0;
  const escalatedCount = data?.stats.events["escalated"] ?? 0;
  const deadLetterCount = data?.stats.deliveries["dead_letter"] ?? 0;

  return (
    <div>
      <h1 style={{ margin: 0, fontSize: 26, fontWeight: 600, letterSpacing: "-0.01em" }}>
        Overview
      </h1>
      <div style={{ marginTop: 6, fontSize: 14, color: c.muted }}>
        Community edition — single project, single global channel set.
      </div>

      <div className="al-cards" style={{ marginTop: 28 }}>
        <StatCard
          value={newCount}
          label="New events"
          cta="View events →"
          onClick={() => nav("/events?state=new")}
        />
        <StatCard
          value={escalatedCount}
          label="Escalated"
          color="#dc6c6c"
          cta="View events →"
          onClick={() => nav("/events?state=escalated")}
        />
        <StatCard
          value={deadLetterCount}
          label="Dead-letter deliveries"
          color="#c99a2e"
          cta="View deliveries →"
          onClick={() => nav("/deliveries?state=dead_letter")}
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
          Recent critical events
        </h2>
        {loading ? (
          <Loading />
        ) : error ? (
          <ErrorState message={error} onRetry={reload} />
        ) : data && data.critical.length > 0 ? (
          <Card style={{ overflow: "hidden" }}>
            {data.critical.map((ev) => (
              <div
                key={ev.id}
                onClick={() => nav(`/events/${ev.id}`)}
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 16,
                  padding: "13px 18px",
                  borderBottom: `1px solid ${c.rowBorder}`,
                  cursor: "pointer",
                }}
              >
                <span style={{ fontFamily: mono, fontSize: 12.5, color: c.muted, width: 70, flexShrink: 0 }}>
                  {timeOfDay(ev.created_at)}
                </span>
                <Badge kind="severity" value={ev.severity} />
                <span
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
                <span style={{ fontSize: 12.5, color: c.muted, flexShrink: 0 }}>{ev.source}</span>
              </div>
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
  cta,
  onClick,
  color,
}: {
  value: number;
  label: string;
  cta: string;
  onClick: () => void;
  color?: string;
}) {
  return (
    <Card
      onClick={onClick}
      style={{ flex: 1, padding: "20px 22px", cursor: "pointer" }}
    >
      <div style={{ fontSize: 32, fontWeight: 600, color: color ?? c.text }}>{value}</div>
      <div style={{ marginTop: 6, fontSize: 13, color: c.muted }}>{label}</div>
      <div style={{ marginTop: 10, fontSize: 12.5, color: c.accent }}>{cta}</div>
    </Card>
  );
}
