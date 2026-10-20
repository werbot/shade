import type { Span } from "./segments";

export type Scope = "global" | "project";
export interface Rule { name: string; type: string; kind: string; pattern: string; secret_group: number; enabled: boolean; builtin: boolean; scope: Scope }
export interface RuleList { project: string; types: string[]; rules: Rule[] }
export interface NewRule { name: string; type: string; kind: "regex" | "literal"; pattern: string; secret_group: number; scope: Scope }
export interface Candidate { type: string; kind: string; pattern: string; secret_group: number }
export interface TestResult { text: string; spans: Span[] }

/** A refusal of the API: its HTTP status and the text it gave for the user. */
export class ApiError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

// A name may hold "/", a space or "%", so it is encoded to stay one path segment.
export const ruleURL = (name: string, scope: Scope): string => `/api/rules/${encodeURIComponent(name)}?scope=${scope}`;

// A global rule and a project rule may share a name, so the scope is part of the key.
export const ruleKey = (r: Pick<Rule, "name" | "scope">): string => `${r.scope}/${r.name}`;

async function request<T>(url: string, method = "GET", body?: unknown, signal?: AbortSignal): Promise<T> {
  // The server answers a POST or PATCH without the JSON type with 415. No mode is set: the default
  // one sends the page's own Origin, and the server refuses the null that "same-origin" would send.
  const res = await fetch(url, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
  if (!res.ok) {
    // The API refuses with {"error": text}; anything else, such as a proxy's page, has the status text.
    const refusal = await res.json().catch(() => null);
    throw new ApiError(res.status, refusal?.error ?? res.statusText);
  }
  return res.status === 204 ? (undefined as T) : res.json();
}

export function listRules(): Promise<RuleList> {
  return request("/api/rules");
}

export function createRule(r: NewRule): Promise<Rule> {
  return request("/api/rules", "POST", r);
}

export function setEnabled(r: Rule, enabled: boolean): Promise<void> {
  return request(ruleURL(r.name, r.scope), "PATCH", { enabled });
}

export function deleteRule(r: Rule): Promise<void> {
  return request(ruleURL(r.name, r.scope), "DELETE");
}

/** Tries the candidate on the text, or the active rules when there is none. */
export function testText(text: string, rule: Candidate | null, signal: AbortSignal): Promise<TestResult> {
  return request("/api/test", "POST", rule ? { text, rule } : { text }, signal);
}
