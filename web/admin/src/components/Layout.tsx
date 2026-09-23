import { forwardRef, useEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { api } from "../api";
import { useMediaQuery } from "../hooks";
import { c, font } from "../theme";
import { Button } from "../ui";
import { Logo } from "./Logo";
import { Sidebar } from "./Sidebar";

// Layout renders the sidebar + scrollable content area and keeps the
// dead-letter badge count fresh via a light poll. On narrow screens the sidebar
// collapses into a slide-over drawer opened from a top bar.
export function Layout({ children }: { children: ReactNode }) {
  const [deadLetter, setDeadLetter] = useState(0);
  const mobile = useMediaQuery("(max-width: 820px)");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const menuButton = useRef<HTMLButtonElement>(null);
  const topBar = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!mobile) setDrawerOpen(false);
  }, [mobile]);

  // An open menu is modal: the page behind it is inert, the focus starts on
  // its first link and Tab cycles inside it. Escape, the backdrop or a menu
  // link closes it, and the focus returns to the menu button.
  useEffect(() => {
    const menu = document.getElementById("al-menu");
    if (!drawerOpen || !menu) return;
    const behind = [topBar.current, content.current];
    behind.forEach((el) => el && (el.inert = true));
    menu.querySelector("a")?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setDrawerOpen(false);
        return;
      }
      if (e.key !== "Tab") return;
      const items = menu.querySelectorAll<HTMLElement>("a[href], button");
      const first = items[0];
      const last = items[items.length - 1];
      const inside = menu.contains(document.activeElement);
      if (e.shiftKey ? !inside || document.activeElement === first : !inside || document.activeElement === last) {
        e.preventDefault();
        (e.shiftKey ? last : first)?.focus();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      behind.forEach((el) => el && (el.inert = false));
      menuButton.current?.focus();
    };
  }, [drawerOpen]);

  useEffect(() => {
    let alive = true;
    const load = () =>
      api
        .stats()
        .then((s) => alive && setDeadLetter(s.deliveries["dead_letter"] ?? 0))
        .catch(() => {});
    load();
    const t = window.setInterval(load, 20000);
    return () => {
      alive = false;
      window.clearInterval(t);
    };
  }, []);

  return (
    <div
      style={{
        display: "flex",
        height: "100vh",
        width: "100%",
        background: c.bg,
        color: c.text,
        fontFamily: font,
      }}
    >
      {!mobile && <Sidebar deadLetterCount={deadLetter} />}

      {mobile && (
        <>
          <TopBar ref={menuButton} barRef={topBar} open={drawerOpen} onMenu={() => setDrawerOpen(true)} />
          {drawerOpen && (
            <div
              onClick={() => setDrawerOpen(false)}
              style={{
                position: "fixed",
                inset: 0,
                background: "rgba(0,0,0,0.5)",
                zIndex: 55,
              }}
            />
          )}
          <Sidebar
            deadLetterCount={deadLetter}
            mobile
            open={drawerOpen}
            onNavigate={() => setDrawerOpen(false)}
          />
        </>
      )}

      <div
        ref={content}
        style={{
          flex: 1,
          minWidth: 0,
          overflowY: "auto",
          padding: mobile ? "64px 16px 24px" : "40px 48px",
          boxSizing: "border-box",
        }}
      >
        {children}
      </div>
    </div>
  );
}

// TopBar is the fixed mobile header with a hamburger button.
const TopBar = forwardRef<
  HTMLButtonElement,
  { open: boolean; onMenu: () => void; barRef: RefObject<HTMLDivElement> }
>(function TopBar({ open, onMenu, barRef }, ref) {
  return (
    <div
      ref={barRef}
      style={{
        position: "fixed",
        top: 0,
        left: 0,
        right: 0,
        height: 52,
        zIndex: 50,
        background: c.sidebar,
        borderBottom: `1px solid ${c.border}`,
        display: "flex",
        alignItems: "center",
        gap: 12,
        padding: "0 14px",
        boxSizing: "border-box",
      }}
    >
      <Button
        ref={ref}
        onClick={onMenu}
        aria-label="Open menu"
        aria-expanded={open}
        aria-controls="al-menu"
        style={{
          width: 34,
          height: 34,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          borderRadius: 7,
          color: c.text2,
        }}
      >
        <svg width="20" height="20" viewBox="0 0 20 20" fill="none">
          <line x1="3" y1="6" x2="17" y2="6" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
          <line x1="3" y1="10" x2="17" y2="10" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
          <line x1="3" y1="14" x2="17" y2="14" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
        </svg>
      </Button>
      <Logo size={20} />
      <span style={{ fontSize: 15, fontWeight: 600, letterSpacing: "-0.01em" }}>AlertLoop</span>
    </div>
  );
});
