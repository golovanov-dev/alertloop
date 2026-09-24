export type EventType = "incident" | "business_event" | "audit";
export type Severity = "info" | "success" | "warning" | "error" | "critical";
export type EventState =
  | "new"
  | "acknowledged"
  | "resolved"
  | "muted"
  | "escalated";
export type DeliveryState =
  | "pending"
  | "sending"
  | "sent"
  | "failed"
  | "dead_letter"
  /** Stopped by mute before it was sent; never replayed. */
  | "cancelled";

export interface AlertEvent {
  id: string;
  type: EventType;
  severity: Severity;
  state: EventState;
  source: string;
  category?: string;
  message: string;
  entity_type?: string;
  entity_id?: string;
  trace_id?: string;
  dedupe_key?: string;
  payload?: unknown;
  created_at: string;
  updated_at: string;
  /** When the incident was last reported as still firing. */
  last_seen_at: string;
  /** When the incident was closed. Absent while it is open. */
  resolved_at?: string | null;
}

/** Delivery channel types; `slack` also covers Mattermost and Rocket.Chat. */
export const CHANNEL_TYPES = ["email", "telegram", "webhook", "slack", "teams", "discord", "ntfy", "pushover"] as const;
export type ChannelType = (typeof CHANNEL_TYPES)[number];

export interface DeliveryAttempt {
  id: string;
  event_id: string;
  channel: ChannelType;
  channel_name: string;
  /** "alert" announces the incident; "recovery" announces that it is over. */
  kind: "alert" | "recovery";
  state: DeliveryState;
  attempts: number;
  max_attempts: number;
  next_retry_at?: string | null;
  last_error?: string;
  created_at: string;
  updated_at: string;
  /** Set on an alert redirected to a channel's fallback: the dead-lettered attempt. */
  fallback_of?: AttemptLink;
  /** Set on a dead-lettered alert that was redirected: the attempt on the fallback. */
  fallback_to?: AttemptLink;
  /** Set on a recovery: the alert attempt it follows; the recovery waits until that alert is sent. */
  recovery_for?: AttemptLink;
}

export interface AttemptLink {
  id: string;
  channel_name?: string;
  /** State of the linked attempt: on fallback_to, whether the redirect got through. */
  state?: DeliveryState;
}

export interface Page<T> {
  items: T[];
  next_cursor?: string;
}

export interface Stats {
  /** Counts by state; a state with no rows is absent, not 0. */
  events: Record<string, number>;
  deliveries: Record<string, number>;
  /** Incidents that are not resolved. */
  open_incidents: number;
  oldest_due_delivery_age_seconds: number | null;
  dead_letter_last_24h: number;
  worker_last_tick_at: string | null;
}

export interface Info {
  version: string;
  edition: string;
  license: string;
}

export type EventAction = "ack" | "resolve" | "mute" | "unmute" | "escalate";
