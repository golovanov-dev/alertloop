import { useCallback, useEffect, useState } from "react";
import { api, type User } from "../api";
import { useApp } from "../context";
import { useMediaQuery } from "../hooks";
import { c } from "../theme";
import { Button, Card, ErrorState, Loading, NARROW, PageTitle, td, th, Time } from "../ui";
import { Field, MIN_PASSWORD } from "./Login";

const small = {
  fontSize: 12.5,
  color: c.accent,
  padding: "5px 10px",
  border: `1px solid ${c.border}`,
  borderRadius: 7,
} as const;

const primary = {
  marginTop: 16,
  padding: "8px 14px",
  borderRadius: 8,
  background: c.accent,
  color: c.accentInk,
  fontSize: 13,
  fontWeight: 600,
} as const;

const message = (e: unknown) => (e instanceof Error ? e.message : "Request failed");

/** Console users (all administrators) and the signed-in user's own account. */
export function Users() {
  const { user, showToast } = useApp();
  const narrow = useMediaQuery(NARROW);
  const [users, setUsers] = useState<User[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setError(null);
    api.listUsers().then(setUsers).catch((e) => setError(message(e)));
  }, []);
  useEffect(load, [load]);

  return (
    <div style={{ maxWidth: 760 }}>
      <PageTitle title="Users" subtitle="Everyone here is an administrator. Roles and teams are part of Pro." />
      <OwnAccount />
      <Card style={{ marginTop: 20, padding: "18px 20px" }}>
        <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>All users</h2>
        {error ? (
          <ErrorState message={error} onRetry={load} />
        ) : users === null ? (
          <Loading />
        ) : narrow ? (
          // On a phone each user is a card: the actions and the forms they
          // open sit under the login they apply to, with no sideways scroll.
          <div style={{ marginTop: 6 }}>
            {users.map((u) => (
              <UserCard key={u.id} u={u} self={u.id === user?.id} onChange={load} onDone={showToast} />
            ))}
          </div>
        ) : (
          <div style={{ overflowX: "auto", marginTop: 10 }}>
            <table style={{ width: "100%", borderCollapse: "collapse" }}>
              <thead>
                <tr>
                  <th style={th}>Login</th>
                  <th style={th}>Last sign-in</th>
                  <th style={th}>Created</th>
                  <th style={th} />
                </tr>
              </thead>
              <tbody>
                {users.map((u) => (
                  <UserRow key={u.id} u={u} self={u.id === user?.id} onChange={load} onDone={showToast} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <AddUser onAdded={load} />
    </div>
  );
}

type RowProps = { u: User; self: boolean; onChange: () => void; onDone: (m: string) => void };

function Login({ u, self }: { u: User; self: boolean }) {
  return (
    <>
      <span style={{ fontWeight: 600 }}>{u.login}</span>
      {self && <span style={{ color: c.muted }}> (you)</span>}
      {u.disabled_at && <span style={{ color: c.danger }}> · disabled</span>}
    </>
  );
}

function UserRow(props: RowProps) {
  const { u, self } = props;
  return (
    <tr aria-label={u.login}>
      <td style={td}>
        <Login u={u} self={self} />
      </td>
      <td style={td}>
        <Time ts={u.last_login_at} />
      </td>
      <td style={td}>
        <Time ts={u.created_at} />
      </td>
      <td style={{ ...td, textAlign: "right" }}>
        <UserActions {...props} align="flex-end" />
      </td>
    </tr>
  );
}

function UserCard(props: RowProps) {
  const { u, self } = props;
  return (
    <div role="group" aria-label={u.login} style={{ padding: "12px 0", borderBottom: `1px solid ${c.rowBorder}` }}>
      <div>
        <Login u={u} self={self} />
      </div>
      <div style={{ marginTop: 4, fontSize: 12.5, color: c.muted }}>
        Last sign-in <Time ts={u.last_login_at} /> · created <Time ts={u.created_at} />
      </div>
      <div style={{ marginTop: 8 }}>
        <UserActions {...props} align="flex-start" />
      </div>
    </div>
  );
}

/** Reset password, Disable and Enable for another user. One's own password
 *  changes in "Your account", which asks for the current one. */
function UserActions({ u, self, onChange, onDone, align }: RowProps & { align: "flex-start" | "flex-end" }) {
  const [mode, setMode] = useState<"idle" | "reset" | "disable">("idle");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);

  const act = async (fn: () => Promise<unknown>, done: string) => {
    setError(null);
    try {
      await fn();
      setMode("idle");
      setPassword("");
      onDone(done);
      onChange();
    } catch (e) {
      setError(message(e));
    }
  };

  const row = { display: "flex", flexWrap: "wrap", gap: 8, alignItems: "center", justifyContent: align } as const;
  const note = { fontSize: 12.5, color: c.muted } as const;

  if (self) {
    return <span style={note}>Change your password under “Your account”</span>;
  }
  return (
    <div>
      {mode === "idle" && (
        <div style={row}>
          {u.disabled_at ? (
            <Button style={small} onClick={() => act(() => api.enableUser(u.id), `${u.login} enabled`)}>
              Enable
            </Button>
          ) : (
            <>
              <Button style={small} onClick={() => setMode("reset")}>
                Reset password
              </Button>
              <Button style={small} onClick={() => setMode("disable")}>
                Disable
              </Button>
            </>
          )}
        </div>
      )}
      {mode === "reset" && (
        <div style={row}>
          <span style={note}>New password for {u.login}; signs them out everywhere.</span>
          <input
            type="password"
            aria-label={`New password for ${u.login}`}
            placeholder={`New password (${MIN_PASSWORD}+)`}
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            style={{ padding: "5px 8px", fontSize: 12.5, minWidth: 0, maxWidth: "100%" }}
          />
          <Button style={small} onClick={() => act(() => api.resetPassword(u.id, password), `Password of ${u.login} reset`)}>
            Save password
          </Button>
          <Button style={{ ...small, color: c.muted }} onClick={() => setMode("idle")}>
            Cancel
          </Button>
        </div>
      )}
      {mode === "disable" && (
        <div style={row}>
          <span style={note}>Signs {u.login} out everywhere.</span>
          <Button style={{ ...small, color: c.danger }} onClick={() => act(() => api.disableUser(u.id), `${u.login} disabled`)}>
            Confirm disable
          </Button>
          <Button style={{ ...small, color: c.muted }} onClick={() => setMode("idle")}>
            Cancel
          </Button>
        </div>
      )}
      {error && <div style={{ marginTop: 6, fontSize: 12, color: c.danger }}>{error}</div>}
    </div>
  );
}

function AddUser({ onAdded }: { onAdded: () => void }) {
  const { showToast } = useApp();
  const [login, setLogin] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);

  const add = async () => {
    setError(null);
    try {
      const u = await api.createUser(login.trim(), password);
      setLogin("");
      setPassword("");
      showToast(`User ${u.login} added`);
      onAdded();
    } catch (e) {
      setError(message(e));
    }
  };

  return (
    <Card style={{ marginTop: 20, padding: "18px 20px" }}>
      <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>Add user</h2>
      <Field id="new-login" label="Login" value={login} onChange={setLogin} autoComplete="off" />
      <Field id="new-password" label={`Password (at least ${MIN_PASSWORD} characters)`} type="password" value={password}
        onChange={setPassword} autoComplete="new-password" />
      {error && <div role="alert" style={{ marginTop: 12, fontSize: 12.5, color: c.danger }}>{error}</div>}
      <Button style={primary} onClick={add}>
        Add user
      </Button>
    </Card>
  );
}

function OwnAccount() {
  const { user, logout, showToast } = useApp();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [error, setError] = useState<string | null>(null);

  const change = async () => {
    setError(null);
    try {
      await api.changePassword(current, next);
      setCurrent("");
      setNext("");
      showToast("Password changed; your other sessions have ended");
    } catch (e) {
      setError(message(e));
    }
  };

  return (
    <Card style={{ marginTop: 24, padding: "18px 20px" }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", gap: 12, flexWrap: "wrap" }}>
        <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>Your account: {user?.login}</h2>
        <Button style={small} onClick={() => logout(true)}>
          Sign out everywhere
        </Button>
      </div>
      <Field id="current-password" label="Current password" type="password" value={current} onChange={setCurrent}
        autoComplete="current-password" />
      <Field id="next-password" label={`New password (at least ${MIN_PASSWORD} characters)`} type="password" value={next}
        onChange={setNext} autoComplete="new-password" />
      {error && <div role="alert" style={{ marginTop: 12, fontSize: 12.5, color: c.danger }}>{error}</div>}
      <Button style={primary} onClick={change}>
        Change password
      </Button>
    </Card>
  );
}
