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
  | "dead_letter";

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
}

export interface DeliveryAttempt {
  id: string;
  event_id: string;
  channel: "email" | "telegram" | "webhook";
  channel_name: string;
  state: DeliveryState;
  attempts: number;
  max_attempts: number;
  next_retry_at?: string | null;
  last_error?: string;
  created_at: string;
  updated_at: string;
}

export interface Page<T> {
  items: T[];
  next_cursor?: string;
}

export interface Stats {
  events: Record<string, number>;
  deliveries: Record<string, number>;
}

export interface Info {
  version: string;
  edition: string;
  license: string;
}

export type EventAction = "ack" | "resolve" | "mute" | "unmute" | "escalate";
