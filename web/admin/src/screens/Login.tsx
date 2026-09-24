import { useState, type ReactNode } from "react";
import { api, ApiError } from "../api";
import { Logo } from "../components/Logo";
import { useApp } from "../context";
import { c, font } from "../theme";
import { Button } from "../ui";

export const MIN_PASSWORD = 12;

// signInError turns a refused sign-in or setup into a sentence for the form.
export function signInError(e: unknown): string {
  if (e instanceof ApiError && e.status === 0) return "Cannot reach the AlertLoop server.";
  if (e instanceof ApiError && e.status === 429) return "Too many attempts. Wait a little and try again.";
  return e instanceof Error ? e.message : "Sign in failed";
}

/** Sign in with a login and password. */
export function Login() {
  const { signedIn, notice } = useApp();
  const [login, setLogin] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!login.trim() || !password) {
      setError("Enter your login and password");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      signedIn(await api.login({ login: login.trim(), password }));
    } catch (e) {
      setError(e instanceof ApiError && e.status === 401 ? "Wrong login or password" : signInError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Frame title="Sign in" subtitle="Community edition admin console" onSubmit={submit} busy={busy} action="Sign in"
      busyAction="Signing in…" error={error} notice={notice}
      footer="Forgot the password? An administrator can reset it, or run alertloop user passwd on the server.">
      <Field id="login" label="Login" value={login} onChange={setLogin} autoComplete="username" autoFocus />
      <Field id="password" label="Password" type="password" value={password} onChange={setPassword} autoComplete="current-password" />
    </Frame>
  );
}

/** Create the first administrator; the admin token proves you own this installation. */
export function Setup() {
  const { signedIn } = useApp();
  const [token, setToken] = useState("");
  const [login, setLogin] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!token.trim() || !login.trim()) {
      setError("Enter the admin token and a login");
      return;
    }
    if (password.length < MIN_PASSWORD) {
      setError(`The password needs at least ${MIN_PASSWORD} characters`);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      signedIn(await api.setup({ admin_token: token.trim(), login: login.trim(), password }));
    } catch (e) {
      setError(e instanceof ApiError && e.status === 401 ? "Wrong admin token" : signInError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Frame title="Create administrator" subtitle="No users yet. The first one needs the admin token."
      onSubmit={submit} busy={busy} action="Create and sign in" busyAction="Creating…" error={error}
      footer="The admin token is admin_token in your AlertLoop config (ALERTLOOP_ADMIN_TOKEN in .env under Compose).">
      <Field id="token" label="Admin token" type="password" value={token} onChange={setToken} autoComplete="off" autoFocus />
      <Field id="login" label="Login" value={login} onChange={setLogin} autoComplete="username" />
      <Field id="password" label={`Password (at least ${MIN_PASSWORD} characters)`} type="password" value={password}
        onChange={setPassword} autoComplete="new-password" />
    </Frame>
  );
}

/** The server did not answer the sign-in check. */
export function Unreachable() {
  const { retry } = useApp();
  return (
    <Frame title="Cannot reach AlertLoop" subtitle="The server did not answer." onSubmit={retry} busy={false}
      action="Try again" busyAction="" error={null} />
  );
}

export function Field({
  id,
  label,
  value,
  onChange,
  type = "text",
  autoComplete,
  autoFocus,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
  type?: string;
  autoComplete?: string;
  autoFocus?: boolean;
}) {
  return (
    <div style={{ marginTop: 16 }}>
      <label htmlFor={id} style={{ display: "block", fontSize: 12.5, color: c.muted, marginBottom: 6 }}>
        {label}
      </label>
      <input
        id={id}
        type={type}
        value={value}
        autoFocus={autoFocus}
        autoComplete={autoComplete}
        onChange={(e) => onChange(e.target.value)}
        // Enter submits the sign-in forms; outside a form it does nothing.
        onKeyDown={(e) => e.key === "Enter" && e.currentTarget.form?.requestSubmit()}
        style={{ width: "100%", padding: "10px 12px", fontSize: 13.5, boxSizing: "border-box" }}
      />
    </div>
  );
}

function Frame({
  title,
  subtitle,
  children,
  onSubmit,
  busy,
  action,
  busyAction,
  error,
  notice,
  footer,
}: {
  title: string;
  subtitle: string;
  children?: ReactNode;
  onSubmit: () => void;
  busy: boolean;
  action: string;
  busyAction: string;
  error: string | null;
  notice?: string | null;
  footer?: string;
}) {
  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        minHeight: "100vh",
        width: "100%",
        background: c.bg,
        color: c.text,
        fontFamily: font,
      }}
    >
      <div style={{ width: 360, maxWidth: "90vw" }}>
        <div style={{ display: "flex", alignItems: "center", gap: 10, justifyContent: "center", marginBottom: 28 }}>
          <Logo size={26} />
          <span style={{ fontSize: 18, fontWeight: 600, letterSpacing: "-0.01em" }}>AlertLoop</span>
        </div>

        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (!busy) onSubmit();
          }}
          style={{ background: c.card, border: `1px solid ${c.border}`, borderRadius: 12, padding: "28px 26px" }}
        >
          <h1 style={{ margin: 0, fontSize: 19, fontWeight: 600 }}>{title}</h1>
          <div style={{ marginTop: 5, fontSize: 13, color: c.muted }}>{subtitle}</div>

          <div style={{ marginTop: 8 }}>{children}</div>

          {notice && !error && <div style={{ marginTop: 14, fontSize: 12.5, color: c.warn }}>{notice}</div>}
          {error && (
            <div role="alert" style={{ marginTop: 14, fontSize: 12.5, color: c.danger }}>
              {error}
            </div>
          )}

          <Button
            disabled={busy}
            onClick={onSubmit}
            style={{
              display: "block",
              width: "100%",
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
            {busy ? busyAction : action}
          </Button>
          {footer && <div style={{ marginTop: 16, textAlign: "center", fontSize: 12, color: c.muted2 }}>{footer}</div>}
        </form>
      </div>
    </div>
  );
}

