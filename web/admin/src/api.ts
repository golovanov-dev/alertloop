import type {
  AlertEvent,
  DeliveryAttempt,
  EventAction,
  Info,
  Page,
  Stats,
} from "./types";

// The console is served by the AlertLoop binary, so the API is on the same
// origin and every path below is absolute.

const TOKEN_KEY = "alertloop.adminToken";

export function getToken(): string {
  return sessionStorage.getItem(TOKEN_KEY) ?? "";
}
export function setToken(token: string): void {
  sessionStorage.setItem(TOKEN_KEY, token);
}
export function clearToken(): void {
  sessionStorage.removeItem(TOKEN_KEY);
}

// onUnauthorized runs when the server rejects the session token (401) in the
// middle of a session: the token was changed or the key removed.
let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(fn: (() => void) | null): void {
  onUnauthorized = fn;
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(
  method: string,
  path: string,
  opts: { token?: string; body?: unknown } = {},
): Promise<T> {
  const token = opts.token ?? getToken();
  const headers: Record<string, string> = {};
  if (token) headers["X-API-Key"] = token;
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";

  let resp: Response;
  try {
    resp = await fetch(path, {
      method,
      headers,
      body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    });
  } catch {
    throw new ApiError(0, "Network error — is the API reachable?");
  }

  if (resp.status === 204) return undefined as T;
  const text = await resp.text();
  const data = text ? safeJSON(text) : undefined;
  if (!resp.ok) {
    if (resp.status === 401 && opts.token === undefined) onUnauthorized?.();
    const msg =
      (data && (data as any).error?.message) ||
      `Request failed (${resp.status})`;
    throw new ApiError(resp.status, msg);
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
  // verify checks that a token opens the whole console. /v1/info needs scope
  // read; /v1/routing needs full, which the console's actions need, and has no
  // side effects. A read or ingest key gets 403 from one of the two.
  async verify(token: string): Promise<void> {
    await request<Info>("GET", "/v1/info", { token });
    await request<unknown>("GET", "/v1/routing", { token });
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
