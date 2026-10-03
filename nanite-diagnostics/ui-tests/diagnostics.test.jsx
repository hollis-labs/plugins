import React, { useLayoutEffect } from "react";
import { afterEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import samples from "../testdata/core-queries.json";
import {
  DiagnosticsPanel,
  SystemPromptsViewer,
  formatTokens,
  formatUsageCost,
  formatMetricCost,
  formatDuration,
} from "../ui/index.js";
const fixture = (name) =>
  structuredClone(samples.find((s) => s.name === name).data);
const results = (slots = "slots", metrics = "metrics") => ({
  usage: { data: fixture("usage") },
  execution_metrics: { data: fixture(metrics) },
  context_slots: { data: fixture(slots) },
});
const response = (data) => ({ ok: true, json: async () => data });
const query = (url) => new URL(url, "http://test").searchParams;
const envelope = (url, result) => ({
  session_id: query(url).get("session_id"),
  resource: query(url).get("resource"),
  result,
});
function host(data = results()) {
  const fetch = vi.fn((url) =>
    Promise.resolve(response(envelope(url, data[query(url).get("resource")]))),
  );
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
const region = (name) => screen.getByRole("region", { name });
const done = () =>
  waitFor(() =>
    expect(screen.getByRole("button", { name: "Refresh" }).disabled).toBe(
      false,
    ),
  );
const field = (scope, name) =>
  within(scope).getByText(name, { selector: "dt" }).nextElementSibling
    .textContent;
const rows = () =>
  within(screen.getByRole("table")).getAllByRole("row").slice(1);
const ids = () =>
  rows()
    .filter((row) => within(row).queryByRole("button"))
    .map((row) =>
      Number(
        within(row)
          .getByRole("button")
          .getAttribute("aria-label")
          .match(/\d+/)[0],
      ),
    );
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
it("renders core usage values per label and metrics per column", async () => {
  const fetch = host();
  const { container } = render(<DiagnosticsPanel session_id="session-a" />);
  await done();
  for (const [label, value] of Object.entries({
    Input: "2.0k",
    "Content input": "1.8k",
    "Tool input": "250",
    Output: "500",
    Total: "2.5k",
    "Cache creation": "120",
    "Cache read": "700",
    "Recorded estimated cost": "$0.006",
    Messages: "2",
  }))
    expect(field(region("Recorded session usage"), label)).toBe(value);
  expect(screen.getByText(/completeness details are unavailable/)).toBeTruthy();
  expect(screen.getByText(/More execution metrics exist/)).toBeTruthy();
  expect(ids().slice(0, 3)).toEqual([51, 50, 49]);
  expect(
    within(rows()[0])
      .getAllByRole("cell")
      .map((c) => c.textContent)
      .slice(0, 9),
  ).toEqual([
    new Date("2026-10-03T09:00:00Z").toLocaleString(),
    "fixture-provider",
    "sub",
    "fixture-model",
    "1.0m",
    "510",
    "153",
    "$0.0050",
    "No error recorded",
  ]);
  expect(within(rows()[1]).getAllByRole("cell")[8].textContent).toBe("Failed");
  expect(
    within(screen.getByRole("table"))
      .getAllByRole("columnheader")
      .map((th) => th.textContent)
      .slice(4, 8),
  ).toEqual(["Duration", "In", "Out", "Recorded estimated cost"]);
  fireEvent.click(screen.getByRole("button", { name: "Execution 51 details" }));
  const detail = rows()[1];
  expect(field(detail, "Utility call")).toBe("no");
  expect(field(detail, "Cache creation")).toBe("51");
  expect(field(detail, "Cache read")).toBe("102");
  expect(field(detail, "Profile digest")).toBe("fixture-digest");
  fireEvent.click(screen.getByRole("button", { name: "Execution 50 details" }));
  const utility = screen
    .getByRole("button", { name: "Execution 50 details" })
    .closest("tr").nextElementSibling;
  expect(field(utility, "Utility call")).toBe("yes");
  expect(container.textContent).not.toContain("private");
  expect(screen.queryByLabelText(/reveal/i)).toBeNull();
  expect(container.firstChild.style.overflowY).toBe("auto");
  expect(
    screen.queryByRole("heading", { name: "Session Diagnostics" }),
  ).toBeNull();
  expect(
    fetch.mock.calls.map(([url]) => query(url).get("resource")).sort(),
  ).toEqual(["context_slots", "execution_metrics", "usage"]);
  for (const [url, options] of fetch.mock.calls) {
    expect(query(url).get("session_id")).toBe("session-a");
    expect(options.cache).toBe("no-store");
  }
});
it("keeps slot flags attached to each slot, including red zero-token mode", async () => {
  host();
  render(<DiagnosticsPanel session_id="session-a" />);
  await done();
  const slots = region("Latest captured slot accounting");
  expect(within(slots).getByText(/Latest captured turn: latest/)).toBeTruthy();
  for (const [name, tokens, cache, sensitive, light] of [
    ["system", "1.2k", "cached", "yes", "green"],
    ["agent", "500", "not cached", "no", "yellow"],
    ["mode", "0", "not cached", "no", "red"],
  ]) {
    const slot = within(slots).getByText(name, {
      selector: "strong",
    }).parentElement;
    expect(field(slot, "Tokens")).toBe(tokens);
    expect(field(slot, "Cache")).toBe(cache);
    expect(field(slot, "Sensitive")).toBe(sensitive);
    expect(field(slot, "Traffic light")).toBe(light);
  }
  expect(
    field(
      within(slots).getByText("system", { selector: "strong" }).parentElement,
      "Cache key",
    ),
  ).toBe("fixture-key");
});
it("clamps content input when tool tokens exceed input", async () => {
  const data = results();
  data.usage.data.tool_input_tokens = 2100;
  host(data);
  render(<DiagnosticsPanel session_id="session-a" />);
  await done();
  expect(field(region("Recorded session usage"), "Content input")).toBe("0");
});
it("distinguishes missing and empty capture", async () => {
  const data = results("missing-capture");
  host(data);
  render(<DiagnosticsPanel session_id="session-a" />);
  await done();
  expect(screen.getByText(/inspector may be disabled/)).toBeTruthy();
  data.context_slots = { data: fixture("empty-capture") };
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await done();
  expect(screen.getByText("Capture exists; no slots recorded.")).toBeTruthy();
  expect(screen.queryByText(/inspector may be disabled/)).toBeNull();
});
it("renders healthy resources while usage stalls, then independently shows its timeout", async () => {
  let finish;
  const data = results();
  vi.stubGlobal(
    "fetch",
    vi.fn((url) =>
      query(url).get("resource") === "usage"
        ? new Promise((resolve) => {
            finish = () =>
              resolve(
                response(
                  envelope(url, {
                    error: "Host accounting read timed out or was canceled",
                    code: "canceled",
                  }),
                ),
              );
          })
        : Promise.resolve(
            response(envelope(url, data[query(url).get("resource")])),
          ),
    ),
  );
  render(<DiagnosticsPanel session_id="session-a" />);
  await screen.findByRole("table");
  expect(
    within(region("Recorded session usage")).getByRole("status").textContent,
  ).toContain("Loading");
  expect(screen.getByText(/Latest captured turn: latest/)).toBeTruthy();
  await act(async () => finish());
  await done();
  expect(
    within(region("Recorded session usage")).getByRole("alert").textContent,
  ).toContain("timed out");
  expect(screen.getByRole("table")).toBeTruthy();
});
it("distinguishes denied usage, empty metrics and missing capture from zero", async () => {
  const data = results("missing-capture", "empty-metrics");
  data.usage = {
    error: "Session or resource is outside the approved read scope",
    code: "host_403",
  };
  host(data);
  render(<DiagnosticsPanel session_id="session-a" />);
  await done();
  expect(screen.getByRole("alert")).toBeTruthy();
  expect(screen.getByText("No execution metrics recorded yet.")).toBeTruthy();
  expect(screen.getByText(/Captured slot accounting unavailable/)).toBeTruthy();
  expect(screen.queryByText("$0.00")).toBeNull();
});
it("sorts both directions and descending ID ties using the non-tied core capture", async () => {
  host(results("slots", "metrics-time-order"));
  render(<DiagnosticsPanel session_id="session-a" />);
  await done();
  expect(ids().slice(0, 3)).toEqual([2, 3, 51]);
  fireEvent.click(
    screen.getByRole("button", { name: "Sort recent metrics by Duration" }),
  );
  expect(ids().slice(0, 3)).toEqual([51, 50, 49]);
  fireEvent.click(
    screen.getByRole("button", { name: "Sort recent metrics by Duration" }),
  );
  expect(ids().slice(0, 3)).toEqual([2, 3, 4]);
  fireEvent.click(
    screen.getByRole("button", {
      name: "Sort recent metrics by Recorded estimated cost",
    }),
  );
  expect(ids().slice(0, 3)).toEqual([51, 50, 49]);
});
it("preserves sort/details on refresh and resets on session changes", async () => {
  host(results("slots", "metrics-time-order"));
  const { rerender } = render(<DiagnosticsPanel session_id="session-a" />);
  await done();
  fireEvent.click(
    screen.getByRole("button", { name: "Sort recent metrics by Duration" }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Execution 51 details" }));
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await done();
  expect(ids()[0]).toBe(51);
  expect(
    screen
      .getByRole("button", { name: "Execution 51 details" })
      .getAttribute("aria-expanded"),
  ).toBe("true");
  rerender(<DiagnosticsPanel session_id="session-b" />);
  await done();
  expect(ids()[0]).toBe(2);
  expect(
    screen
      .getByRole("button", { name: "Execution 51 details" })
      .getAttribute("aria-expanded"),
  ).toBe("false");
  expect(
    screen
      .getByRole("button", { name: "Sort recent metrics by Duration" })
      .closest("th")
      .getAttribute("aria-sort"),
  ).toBe("none");
});
it("does not poll or refetch on focus/visibility, with timers installed before mount", async () => {
  vi.useFakeTimers();
  const fetch = host();
  await act(async () => {
    render(<DiagnosticsPanel session_id="session-a" />);
  });
  expect(fetch).toHaveBeenCalledTimes(3);
  fireEvent(window, new Event("focus"));
  fireEvent(document, new Event("visibilitychange"));
  await act(async () => {
    vi.advanceTimersByTime(120000);
  });
  expect(fetch).toHaveBeenCalledTimes(3);
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  });
  expect(fetch).toHaveBeenCalledTimes(6);
});
it("discards late responses even if fetch ignores abort and preserves new-session data", async () => {
  const late = [];
  const fetch = vi.fn((url) =>
    query(url).get("session_id") === "session-a"
      ? new Promise((resolve) =>
          late.push(() =>
            resolve(
              response(envelope(url, results()[query(url).get("resource")])),
            ),
          ),
        )
      : Promise.resolve(
          response(
            envelope(
              url,
              results("missing-capture")[query(url).get("resource")],
            ),
          ),
        ),
  );
  vi.stubGlobal("fetch", fetch);
  const { rerender } = render(<DiagnosticsPanel session_id="session-a" />);
  rerender(<DiagnosticsPanel session_id="session-b" />);
  await done();
  const before = screen.getByRole("table").textContent;
  expect(fetch.mock.calls[0][1].signal.aborted).toBe(true);
  await act(async () => late.forEach((f) => f()));
  expect(screen.getByRole("table").textContent).toBe(before);
  expect(screen.getByText(/Captured slot accounting unavailable/)).toBeTruthy();
  expect(screen.queryByText(/Latest captured turn: latest/)).toBeNull();
});
it("hides stale data in the commit before passive effects clear state", async () => {
  let selected = "session-a";
  vi.stubGlobal(
    "fetch",
    vi.fn((url) =>
      selected === "session-a"
        ? Promise.resolve(
            response(envelope(url, results()[query(url).get("resource")])),
          )
        : new Promise(() => {}),
    ),
  );
  const observed = [];
  function Probe({ session }) {
    useLayoutEffect(() => {
      observed.push(document.querySelector("table") !== null);
    }, [session]);
    return null;
  }
  const view = (session) => (
    <>
      <DiagnosticsPanel session_id={session} />
      <Probe session={session} />
    </>
  );
  const { rerender } = render(view("session-a"));
  await done();
  selected = "session-b";
  rerender(view("session-b"));
  expect(observed.at(-1)).toBe(false);
  expect(screen.queryByRole("table")).toBeNull();
});
it("clears A -> no session -> A while the new read is pending", async () => {
  const fetch = host();
  const { rerender } = render(<DiagnosticsPanel session_id={null} />);
  expect(fetch).not.toHaveBeenCalled();
  rerender(<DiagnosticsPanel session_id="session-a" />);
  await done();
  rerender(<DiagnosticsPanel session_id={null} />);
  expect(screen.queryByRole("table")).toBeNull();
  fetch.mockImplementation(() => new Promise(() => {}));
  rerender(<DiagnosticsPanel session_id="session-a" />);
  expect(screen.queryByRole("table")).toBeNull();
  expect(fetch).toHaveBeenCalledTimes(6);
});
it("clears old accounting after each resource refresh fails", async () => {
  const fetch = host();
  render(<DiagnosticsPanel session_id="session-a" />);
  await done();
  fetch.mockRejectedValue(new Error("Connection lost"));
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await done();
  expect(screen.queryByRole("table")).toBeNull();
  expect(screen.queryByText(/Latest captured turn: latest/)).toBeNull();
  expect(screen.queryByText("$0.006")).toBeNull();
  expect(screen.getAllByRole("alert")).toHaveLength(3);
});
it("shows validation messages and rejects mismatched response coordinates", async () => {
  const fetch = host();
  fetch.mockResolvedValue({
    ok: false,
    text: async () => "One valid calling session_id is required",
  });
  render(<DiagnosticsPanel session_id="session-a" />);
  await done();
  expect(
    screen.getAllByText("One valid calling session_id is required"),
  ).toHaveLength(3);
  fetch.mockImplementation((url) =>
    Promise.resolve(
      response({
        ...envelope(url, { data: fixture("usage") }),
        session_id: "other",
      }),
    ),
  );
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await done();
  expect(screen.getAllByText("Invalid diagnostics response")).toHaveLength(3);
  expect(screen.queryByRole("table")).toBeNull();
});
it("shows unknown lights honestly and escapes supplied labels", async () => {
  const data = results();
  data.context_slots.data.slots[0].name = "<script>window.bad=true</script>";
  data.context_slots.data.slots[0].traffic_light = "unexpected";
  host(data);
  const { container } = render(<DiagnosticsPanel session_id="session-a" />);
  await done();
  expect(screen.getByText("unknown")).toBeTruthy();
  expect(screen.getByText("<script>window.bad=true</script>")).toBeTruthy();
  expect(container.querySelector("script")).toBeNull();
});
it("labels every static card with STATIC, source and SHA without a read", () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  render(<SystemPromptsViewer />);
  expect(
    screen.getByRole("heading", { name: "STATIC Prompt References" }),
  ).toBeTruthy();
  expect(screen.getByText(/goes stale by design/)).toBeTruthy();
  for (const heading of screen.getAllByRole("heading", { level: 3 })) {
    expect(heading.textContent).toMatch(/ · STATIC$/);
    const card = heading.closest("article");
    expect(
      within(card).getByText(/^Source: internal\/agent\/builtin\/profiles\//),
    ).toBeTruthy();
    expect(within(card).getByText(/^SHA: [0-9a-f]{40}$/)).toBeTruthy();
  }
  expect(fetch).not.toHaveBeenCalled();
});
it("retains pinned core formatter boundary outputs", () => {
  expect([999, 1000, 1000000].map(formatTokens)).toEqual([
    "999",
    "1.0k",
    "1.0M",
  ]);
  expect([0, 0.0005, 0.005, 0.5].map(formatUsageCost)).toEqual([
    "$0.00",
    "$0.0005",
    "$0.005",
    "$0.50",
  ]);
  expect([0, 0.005, 0.5].map(formatMetricCost)).toEqual([
    "—",
    "$0.0050",
    "$0.50",
  ]);
  expect([999, 1000, 60000].map(formatDuration)).toEqual([
    "999ms",
    "1.0s",
    "1.0m",
  ]);
});
