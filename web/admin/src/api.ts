import type {
  AlertEvent,
  DeliveryAttempt,
  EventAction,
  Info,
  Page,
  Stats,
} from "./types";

// The console is served by the AlertLoop binary, so the API is on the same
// origin and every path below is absolute. The server keeps the sign-in in an
// HttpOnly cookie the browser sends by itself; this code never sees it.

/** A console user as /admin/auth/* returns it. */
export interface User {
  id: string;
  login: string;
  created_at: string;
  disabled_at: string | null;
  last_login_at: string | null;
}

// onUnauthorized runs when the server answers 401 in the middle of a session:
// it expired, was ended on another device, or the user was disabled.
let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(fn: (() => void) | null): void {
  onUnauthorized = fn;
}

export class ApiError extends Error {
  status: number;
  code: string;
  constructor(status: number, message: string, code = "") {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function request<T>(
  method: string,
  path: string,
  opts: { body?: unknown; signedOut?: boolean } = {},
): Promise<T> {
  // The server refuses a state change made with the session cookie unless
  // this header is present: another site cannot set it.
  const headers: Record<string, string> = { "X-AlertLoop-Console": "1" };
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";

  let resp: Response;
  try {
    resp = await fetch(path, {
      method,
      headers,
      credentials: "same-origin",
      body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    });
  } catch {
    throw new ApiError(0, "Network error — is the API reachable?");
  }

  if (resp.status === 204) return undefined as T;
  const text = await resp.text();
  const data = text ? safeJSON(text) : undefined;
  if (!resp.ok) {
    if (resp.status === 401 && !opts.signedOut) onUnauthorized?.();
    const err = (data as any)?.error;
    throw new ApiError(resp.status, err?.message || `Request failed (${resp.status})`, err?.code ?? "");
  }
  return data as T;
}

function safeJSON(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

// normalizePage guards against an API returning `items: null` for an empty
// list, so callers can always treat items as an array.
function normalizePage<T>(page: Page<T>): Page<T> {
  return { items: page?.items ?? [], next_cursor: page?.next_cursor };
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "") q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

export const api = {
  /** The signed-in user; ApiError 401 with code "setup_required" before the first one exists. */
  me(): Promise<User> {
    return request<User>("GET", "/admin/auth/me", { signedOut: true });
  },
  setup(body: { admin_token: string; login: string; password: string }): Promise<User> {
    return request<User>("POST", "/admin/auth/setup", { body, signedOut: true });
  },
  login(body: { login: string; password: string }): Promise<User> {
    return request<User>("POST", "/admin/auth/login", { body, signedOut: true });
  },
  logout(everywhere = false): Promise<void> {
    return request<void>("POST", "/admin/auth/logout", { body: { everywhere }, signedOut: true });
  },
  changePassword(current_password: string, new_password: string): Promise<void> {
    return request<void>("POST", "/admin/auth/password", { body: { current_password, new_password } });
  },
  async listUsers(): Promise<User[]> {
    return (await request<Page<User>>("GET", "/admin/auth/users")).items ?? [];
  },
  createUser(login: string, password: string): Promise<User> {
    return request<User>("POST", "/admin/auth/users", { body: { login, password } });
  },
  disableUser(id: string): Promise<User> {
    return request<User>("POST", `/admin/auth/users/${encodeURIComponent(id)}/disable`);
  },
  enableUser(id: string): Promise<User> {
    return request<User>("POST", `/admin/auth/users/${encodeURIComponent(id)}/enable`);
  },
  resetPassword(id: string, password: string): Promise<void> {
    return request<void>("POST", `/admin/auth/users/${encodeURIComponent(id)}/password`, { body: { password } });
  },
  info(): Promise<Info> {
    return request<Info>("GET", "/v1/info");
  },
  stats(): Promise<Stats> {
    return request<Stats>("GET", "/v1/stats");
  },
  async listEvents(params: {
    type?: string;
    severity?: string;
    state?: string;
    source?: string;
    /** Substring of the message, matched on the server ignoring case. */
    q?: string;
    limit?: number;
    cursor?: string;
  }): Promise<Page<AlertEvent>> {
    return normalizePage(await request<Page<AlertEvent>>("GET", "/v1/events" + query(params)));
  },
  getEvent(id: string): Promise<AlertEvent> {
    return request<AlertEvent>("GET", `/v1/events/${encodeURIComponent(id)}`);
  },
  eventAction(id: string, action: EventAction): Promise<AlertEvent> {
    return request<AlertEvent>(
      "POST",
      `/v1/events/${encodeURIComponent(id)}/${action}`,
    );
  },
  async listDeliveries(params: {
    state?: string;
    channel?: string;
    channel_name?: string;
    event_id?: string;
    limit?: number;
    cursor?: string;
  }): Promise<Page<DeliveryAttempt>> {
    return normalizePage(
      await request<Page<DeliveryAttempt>>("GET", "/v1/delivery-attempts" + query(params)),
    );
  },
  replay(id: string): Promise<DeliveryAttempt> {
    return request<DeliveryAttempt>(
      "POST",
      `/v1/delivery-attempts/${encodeURIComponent(id)}/replay`,
    );
  },
  async health(): Promise<boolean> {
    try {
      const r = await fetch("/health");
      return r.ok;
    } catch {
      return false;
    }
  },
};
