import { c } from "../theme";

export function Toast({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <div
      style={{
        position: "fixed",
        bottom: 24,
        right: 28,
        background: c.cardAlt,
        border: `1px solid ${c.accent}`,
        color: c.text,
        padding: "11px 18px",
        borderRadius: 8,
        fontSize: 13.5,
        boxShadow: "0 8px 24px rgba(0,0,0,0.4)",
        zIndex: 50,
      }}
    >
      {message}
    </div>
  );
}
