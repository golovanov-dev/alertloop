import type {
  AlertEvent,
  DeliveryAttempt,
  EventAction,
  Info,
  Page,
  Stats,
} from "./types";

declare global {
  interface Window {
    __ALERTLOOP__?: { apiBase?: string };
  }
}

// apiBase is resolved at runtime from config.js (empty = same origin).
function apiBase(): string {
  return (window.__ALERTLOOP__?.apiBase ?? "").replace(/\/$/, "");
}

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
    resp = await fetch(apiBase() + path, {
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
  // Verifies a token by calling an authenticated endpoint.
  async verify(token: string): Promise<void> {
    await request<Info>("GET", "/v1/info", { token });
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
      const r = await fetch(apiBase() + "/health");
      return r.ok;
    } catch {
      return false;
    }
  },
};
