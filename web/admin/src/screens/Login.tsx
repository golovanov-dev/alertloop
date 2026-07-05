import { useState } from "react";
import { api, ApiError } from "../api";
import { Logo } from "../components/Logo";
import { useApp } from "../context";
import { c, font } from "../theme";

export function Login() {
  const { login } = useApp();
  const [token, setTokenInput] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!token.trim()) {
      setError("Enter the admin token");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await api.verify(token.trim());
      login(token.trim());
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        setError("Invalid admin token");
      } else if (e instanceof ApiError && e.status === 0) {
        setError("Cannot reach the API. Check the server and API base URL.");
      } else {
        setError(e instanceof Error ? e.message : "Sign in failed");
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        height: "100vh",
        width: "100%",
        background: c.bg,
        color: c.text,
        fontFamily: font,
      }}
    >
      <div style={{ width: 360, maxWidth: "90vw" }}>
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 10,
            justifyContent: "center",
            marginBottom: 28,
          }}
        >
          <Logo size={26} />
          <span style={{ fontSize: 18, fontWeight: 600, letterSpacing: "-0.01em" }}>AlertLoop</span>
        </div>

        <div
          style={{
            background: c.card,
            border: `1px solid ${c.border}`,
            borderRadius: 12,
            padding: "28px 26px",
          }}
        >
          <h1 style={{ margin: 0, fontSize: 19, fontWeight: 600 }}>Sign in</h1>
          <div style={{ marginTop: 5, fontSize: 13, color: c.muted }}>
            Community edition admin console
          </div>

          <div style={{ marginTop: 24 }}>
            <div style={{ fontSize: 12.5, color: c.muted, marginBottom: 6 }}>Admin token</div>
            <input
              type="password"
              value={token}
              autoFocus
              onChange={(e) => {
                setTokenInput(e.target.value);
                setError(null);
              }}
              onKeyDown={(e) => e.key === "Enter" && submit()}
              placeholder="••••••••"
              style={{ width: "100%", padding: "10px 12px", fontSize: 13.5 }}
            />
          </div>

          {error && (
            <div style={{ marginTop: 14, fontSize: 12.5, color: c.danger }}>{error}</div>
          )}

          <div
            onClick={busy ? undefined : submit}
            style={{
              marginTop: 20,
              textAlign: "center",
              padding: 11,
              borderRadius: 8,
              background: c.accent,
              color: c.accentInk,
              fontSize: 14,
              fontWeight: 600,
              cursor: busy ? "default" : "pointer",
              opacity: busy ? 0.7 : 1,
            }}
          >
            {busy ? "Signing in…" : "Sign in"}
          </div>
          <div style={{ marginTop: 16, textAlign: "center", fontSize: 12, color: c.muted2 }}>
            Use the admin token from your AlertLoop config.
          </div>
        </div>
      </div>
    </div>
  );
}
