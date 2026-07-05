import type { CSSProperties, ReactNode } from "react";
import { c, delState, evState, mono, sev } from "./theme";

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

export function Card({
  children,
  style,
  onClick,
}: {
  children: ReactNode;
  style?: CSSProperties;
  onClick?: () => void;
}) {
  return (
    <div
      onClick={onClick}
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

export const tdMono: CSSProperties = { ...td, fontFamily: mono, color: c.muted };

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

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
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
      <div style={{ marginTop: 18, fontSize: 15, fontWeight: 600 }}>Could not load data</div>
      <div style={{ marginTop: 6, fontSize: 13.5, color: c.muted, maxWidth: 380 }}>{message}</div>
      {onRetry && (
        <div
          onClick={onRetry}
          style={{
            marginTop: 20,
            padding: "9px 18px",
            borderRadius: 7,
            border: "1px solid #7a3a3a",
            background: "rgba(252,165,165,0.08)",
            color: c.danger,
            fontSize: 13.5,
            cursor: "pointer",
          }}
        >
          Try again
        </div>
      )}
    </Card>
  );
}

export function PrimaryButton({
  children,
  onClick,
  style,
}: {
  children: ReactNode;
  onClick?: () => void;
  style?: CSSProperties;
}) {
  return (
    <div
      onClick={onClick}
      style={{
        display: "inline-block",
        textAlign: "center",
        padding: "9px 18px",
        borderRadius: 8,
        background: c.accent,
        color: c.accentInk,
        fontSize: 13.5,
        fontWeight: 600,
        cursor: "pointer",
        ...style,
      }}
    >
      {children}
    </div>
  );
}
