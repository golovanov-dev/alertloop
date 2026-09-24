import type { ReactNode } from "react";
import { NavLink } from "react-router-dom";
import { useApp } from "../context";
import { c } from "../theme";
import { Button } from "../ui";
import { Logo } from "./Logo";

function navStyle({ isActive }: { isActive: boolean }) {
  return {
    display: "flex",
    alignItems: "center",
    gap: 10,
    padding: "9px 10px",
    borderRadius: 7,
    fontSize: 13.5,
    cursor: "pointer",
    textDecoration: "none",
    background: isActive ? "rgba(167,139,250,0.12)" : "transparent",
    color: isActive ? c.accent : c.text2,
  } as const;
}

function Item({
  to,
  icon,
  children,
  end,
  onNavigate,
}: {
  to: string;
  icon: ReactNode;
  children: ReactNode;
  end?: boolean;
  onNavigate?: () => void;
}) {
  return (
    <NavLink to={to} end={end} style={navStyle} onClick={onNavigate}>
      {icon}
      {children}
    </NavLink>
  );
}

const proRow = {
  display: "flex",
  alignItems: "center",
  justifyContent: "space-between",
  padding: "9px 10px",
  borderRadius: 7,
  fontSize: 13.5,
  color: c.muted3,
  cursor: "not-allowed",
} as const;

const sectionLabel = {
  margin: "22px 8px 8px",
  fontSize: 10.5,
  fontWeight: 600,
  letterSpacing: ".08em",
  color: c.muted3,
  textTransform: "uppercase",
} as const;

export function Sidebar({
  deadLetterCount,
  mobile = false,
  open = false,
  onNavigate,
}: {
  deadLetterCount: number;
  mobile?: boolean;
  open?: boolean;
  onNavigate?: () => void;
}) {
  const { logout, user } = useApp();
  const rootStyle = mobile
    ? ({
        position: "fixed",
        top: 0,
        left: 0,
        bottom: 0,
        width: 240,
        zIndex: 60,
        background: c.sidebar,
        borderRight: `1px solid ${c.border}`,
        display: "flex",
        flexDirection: "column",
        padding: "20px 14px",
        boxSizing: "border-box",
        overflowY: "auto",
        transform: open ? "translateX(0)" : "translateX(-100%)",
        // Hidden, not only moved away: Tab must not reach a closed menu. It
        // shows at once on open (to take the focus) and hides after sliding out.
        visibility: open ? "visible" : "hidden",
        transition: open ? "transform .2s ease" : "transform .2s ease, visibility .2s",
        boxShadow: open ? "2px 0 24px rgba(0,0,0,0.5)" : "none",
      } as const)
    : ({
        width: 240,
        flexShrink: 0,
        background: c.sidebar,
        borderRight: `1px solid ${c.border}`,
        display: "flex",
        flexDirection: "column",
        padding: "20px 14px",
        boxSizing: "border-box",
        overflowY: "auto",
      } as const);

  return (
    <div id={mobile ? "al-menu" : undefined} style={rootStyle}>
      <div style={{ display: "flex", alignItems: "center", gap: 10, padding: "6px 8px 22px" }}>
        <Logo size={22} />
        <span style={{ fontSize: 15, fontWeight: 600, letterSpacing: "-0.01em" }}>AlertLoop</span>
      </div>

      <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
        <Item to="/overview" icon={<IconGrid />} onNavigate={onNavigate}>
          Overview
        </Item>
        <Item to="/events" icon={<IconList />} onNavigate={onNavigate}>
          Events
        </Item>
        <NavLink to="/deliveries" style={navStyle} onClick={onNavigate}>
          <IconArrow />
          <span style={{ flex: 1 }}>Deliveries</span>
          {deadLetterCount > 0 && (
            <span
              style={{
                background: "#4c1d1d",
                color: "#fecaca",
                fontSize: 11,
                fontWeight: 600,
                padding: "1px 7px",
                borderRadius: 999,
              }}
            >
              {deadLetterCount}
            </span>
          )}
        </NavLink>
        <Item to="/users" icon={<IconUsers />} onNavigate={onNavigate}>
          Users
        </Item>
        <Item to="/about" icon={<IconInfo />} onNavigate={onNavigate}>
          About
        </Item>
      </div>

      <div style={sectionLabel}>Pro</div>
      <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
        <div style={proRow} title="Planned for the Pro edition">
          <span>Projects</span>
        </div>
        <div style={proRow} title="Planned for the Pro edition">
          <span>Escalation</span>
        </div>
        <div style={proRow} title="Planned for the Pro edition">
          <span>Team</span>
        </div>
      </div>

      <div style={{ flex: 1 }} />

      <Button
        onClick={() => logout()}
        aria-label="Log out"
        style={{
          borderTop: `1px solid ${c.border}`,
          marginTop: 6,
          padding: "14px 10px 6px",
          display: "flex",
          alignItems: "center",
          gap: 8,
          width: "100%",
        }}
      >
        <span
          style={{
            flexShrink: 0,
            width: 22,
            height: 22,
            borderRadius: 6,
            background: c.cardAlt,
            border: `1px solid ${c.border}`,
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            fontSize: 10,
            color: c.muted,
          }}
        >
          {(user?.login ?? "?").slice(0, 2).toUpperCase()}
        </span>
        <span style={{ fontSize: 12.5, color: c.muted, flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", textAlign: "left" }}>
          {user?.login}
        </span>
        <span style={{ fontSize: 12, color: c.accent }}>Log out</span>
      </Button>
    </div>
  );
}

function IconGrid() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <rect x="1.5" y="1.5" width="5.5" height="5.5" rx="1" stroke="currentColor" strokeWidth="1.4" />
      <rect x="9" y="1.5" width="5.5" height="5.5" rx="1" stroke="currentColor" strokeWidth="1.4" />
      <rect x="1.5" y="9" width="5.5" height="5.5" rx="1" stroke="currentColor" strokeWidth="1.4" />
      <rect x="9" y="9" width="5.5" height="5.5" rx="1" stroke="currentColor" strokeWidth="1.4" />
    </svg>
  );
}
function IconList() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <circle cx="2.3" cy="3" r="1.1" fill="currentColor" />
      <circle cx="2.3" cy="8" r="1.1" fill="currentColor" />
      <circle cx="2.3" cy="13" r="1.1" fill="currentColor" />
      <line x1="5.5" y1="3" x2="14.2" y2="3" stroke="currentColor" strokeWidth="1.4" />
      <line x1="5.5" y1="8" x2="14.2" y2="8" stroke="currentColor" strokeWidth="1.4" />
      <line x1="5.5" y1="13" x2="14.2" y2="13" stroke="currentColor" strokeWidth="1.4" />
    </svg>
  );
}
function IconArrow() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <path d="M3 8h7.5M8 4.5 11.5 8 8 11.5" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}
function IconUsers() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <circle cx="8" cy="5.2" r="2.7" stroke="currentColor" strokeWidth="1.4" />
      <path d="M2.8 14c.6-2.6 2.7-4.1 5.2-4.1s4.6 1.5 5.2 4.1" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
    </svg>
  );
}
function IconInfo() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <circle cx="8" cy="8" r="6.2" stroke="currentColor" strokeWidth="1.4" />
      <line x1="8" y1="7.2" x2="8" y2="11.3" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
      <circle cx="8" cy="4.9" r="0.9" fill="currentColor" />
    </svg>
  );
}
