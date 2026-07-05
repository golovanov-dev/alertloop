import { useEffect, useState } from "react";
import { api } from "../api";
import { c } from "../theme";
import { Card } from "../ui";
import type { Info } from "../types";

export function About() {
  const [info, setInfo] = useState<Info | null>(null);
  const [apiStatus, setApiStatus] = useState<"checking" | "ready" | "down">("checking");

  const check = () => {
    setApiStatus("checking");
    api.health().then((ok) => setApiStatus(ok ? "ready" : "down"));
  };

  useEffect(() => {
    api.info().then(setInfo).catch(() => {});
    check();
  }, []);

  const statusColor = apiStatus === "checking" ? c.warn : apiStatus === "ready" ? c.ok : c.danger;
  const statusLabel = apiStatus === "checking" ? "Checking…" : apiStatus === "ready" ? "Ready" : "Unreachable";

  return (
    <div style={{ maxWidth: 560 }}>
      <h1 style={{ margin: 0, fontSize: 26, fontWeight: 600, letterSpacing: "-0.01em" }}>About</h1>
      <div style={{ marginTop: 6, fontSize: 14, color: c.muted }}>
        Community Edition — self-hosted event and notification center.
      </div>

      <Card style={{ marginTop: 28, padding: "20px 22px", display: "flex", flexDirection: "column", gap: 16 }}>
        <Row>
          <div>
            <div style={{ fontSize: 13, color: c.muted }}>Version</div>
            <div style={{ marginTop: 3, fontFamily: "ui-monospace,monospace", fontSize: 14 }}>
              {info?.version ?? "—"}
            </div>
          </div>
          <span
            style={{
              fontSize: 11.5,
              fontWeight: 600,
              padding: "3px 10px",
              borderRadius: 999,
              background: c.cardAlt,
              color: c.muted,
              border: `1px solid ${c.border}`,
            }}
          >
            Community
          </span>
        </Row>

        <Divider />

        <Row>
          <div>
            <div style={{ fontSize: 13, color: c.muted }}>API status</div>
            <div style={{ marginTop: 5, display: "flex", alignItems: "center", gap: 7 }}>
              <span style={{ width: 7, height: 7, borderRadius: "50%", background: statusColor }} />
              <span style={{ fontSize: 14 }}>{statusLabel}</span>
            </div>
          </div>
          <div
            onClick={check}
            style={{ fontSize: 12.5, color: c.accent, cursor: "pointer", padding: "6px 12px", border: `1px solid ${c.border}`, borderRadius: 7 }}
          >
            Check again
          </div>
        </Row>

        <Divider />

        <Row>
          <div style={{ fontSize: 13, color: c.muted }}>API documentation</div>
          <a href="/swagger" style={{ fontSize: 13.5 }}>
            /swagger →
          </a>
        </Row>

        <Divider />

        <Row>
          <div style={{ fontSize: 13, color: c.muted }}>License</div>
          <div style={{ fontSize: 13.5, color: c.text2 }}>{info?.license ?? "AGPL-3.0-only"}</div>
        </Row>
      </Card>
    </div>
  );
}

function Row({ children }: { children: React.ReactNode }) {
  return <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>{children}</div>;
}
function Divider() {
  return <div style={{ height: 1, background: c.border }} />;
}
