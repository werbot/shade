import { afterEach, describe, expect, mock, spyOn, test } from "bun:test";
import {
  ApiError,
  createRule,
  deleteRule,
  listRules,
  ruleKey,
  ruleURL,
  setEnabled,
  testText,
  type Candidate,
  type NewRule,
  type Rule,
} from "./api";

afterEach(() => mock.restore());

// answer makes fetch answer with res; the spy it returns records the request the page made.
const answer = (res: Response) => spyOn(globalThis, "fetch").mockResolvedValue(res);

const signal = new AbortController().signal;
const candidate: Candidate = { type: "TICKET", kind: "regex", pattern: "T-\\d+", secret_group: 0 };
const newRule: NewRule = { name: "acme", type: "TICKET", kind: "regex", pattern: "T-\\d+", secret_group: 1, scope: "project" };
const rule: Rule = { ...candidate, name: "a/b c", enabled: true, builtin: false, scope: "global" };

describe("ruleURL", () => {
  test("puts the name into one path segment and names the scope", () => {
    expect(ruleURL("a/b c", "project")).toBe("/api/rules/a%2Fb%20c?scope=project");
    expect(ruleURL("50%", "global")).toBe("/api/rules/50%25?scope=global");
  });
});

describe("ruleKey", () => {
  test("tells a global rule from the project rule of the same name", () => {
    expect(ruleKey({ name: "dup", scope: "global" })).toBe("global/dup");
    expect(ruleKey({ name: "dup", scope: "project" })).toBe("project/dup");
  });
});

describe("errors", () => {
  test("a refusal of the API carries its status and its error text", async () => {
    // The status text is what the Go server sends; the error text of the body must win over it.
    answer(Response.json({ error: "bad pattern" }, { status: 400, statusText: "Bad Request" }));
    const err = await testText("x", candidate, signal).catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 400, message: "bad pattern" });
  });

  test("an answer that is not JSON falls back to the status text", async () => {
    answer(new Response("<html>upstream is down</html>", { status: 502, statusText: "Bad Gateway" }));
    const err = await listRules().catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 502, message: "Bad Gateway" });
  });
});

describe("testText", () => {
  test("without a candidate sends the text alone, so the server tries the active rules", async () => {
    const spy = answer(Response.json({ text: "hi", spans: [] }));
    await testText("hi", null, signal);
    const [url, init] = spy.mock.calls[0];
    expect(url).toBe("/api/test");
    expect(JSON.parse(String(init?.body))).toStrictEqual({ text: "hi" });
  });

  test("with a candidate sends it as the rule to try, under the signal that cancels the request", async () => {
    const spy = answer(Response.json({ text: "hi", spans: [] }));
    await testText("hi", candidate, signal);
    const [, init] = spy.mock.calls[0];
    expect(JSON.parse(String(init?.body))).toStrictEqual({ text: "hi", rule: candidate });
    expect(init?.signal).toBe(signal);
  });
});

describe("writes", () => {
  test("a new rule goes out as JSON under the default fetch mode", async () => {
    const created: Rule = { ...newRule, enabled: true, builtin: false };
    const spy = answer(Response.json(created, { status: 201 }));
    expect(await createRule(newRule)).toEqual(created);
    const [url, init] = spy.mock.calls[0];
    expect(url).toBe("/api/rules");
    expect(init?.method).toBe("POST");
    expect(new Headers(init?.headers).get("Content-Type")).toBe("application/json");
    // The default mode sends the page's own Origin; "same-origin" would send null, which the server refuses.
    expect(init?.mode).toBeUndefined();
    expect(JSON.parse(String(init?.body))).toStrictEqual(newRule);
  });

  test("switching a rule patches the one of its name and scope, and takes the empty 204", async () => {
    const spy = answer(new Response(null, { status: 204 }));
    expect(await setEnabled(rule, false)).toBeUndefined();
    const [url, init] = spy.mock.calls[0];
    expect(url).toBe("/api/rules/a%2Fb%20c?scope=global");
    expect(init?.method).toBe("PATCH");
    expect(new Headers(init?.headers).get("Content-Type")).toBe("application/json");
    expect(JSON.parse(String(init?.body))).toStrictEqual({ enabled: false });
  });

  test("deleting a rule addresses the one of its name and scope, with no body", async () => {
    const spy = answer(new Response(null, { status: 204 }));
    expect(await deleteRule(rule)).toBeUndefined();
    const [url, init] = spy.mock.calls[0];
    expect(url).toBe("/api/rules/a%2Fb%20c?scope=global");
    expect(init?.method).toBe("DELETE");
    expect(init?.body).toBeUndefined();
    expect(new Headers(init?.headers).has("Content-Type")).toBe(false);
  });
});
