import React from 'react';
import {afterEach, describe, expect, it, vi} from 'vitest';
import {act, cleanup, fireEvent, render, screen, waitFor, within} from '@testing-library/react';
import samples from '../testdata/core-queries.json';
import {DiagnosticsPanel, SystemPromptsViewer, formatTokens, formatUsageCost, formatMetricCost, formatDuration} from '../ui/index.js';

const fixture = name => samples.find(sample => sample.name === name).data;
const reply = (session = 'session-a', slots = 'slots') => ({session_id: session, usage: {data: fixture('usage')}, execution_metrics: {data: fixture('metrics')}, context_slots: {data: fixture(slots)}});
const response = data => ({ok: true, json: async () => data});
afterEach(() => {cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks();});

describe('recorded diagnostics', () => {
 it('renders genuine core accounting, newest-first bounded metrics and captured slot metadata', async () => {
  const fetch = vi.fn().mockResolvedValue(response(reply())); vi.stubGlobal('fetch', fetch);
  const {container} = render(<DiagnosticsPanel session_id="session-a" />);
  const usage = await screen.findByRole('region', {name: 'Recorded session usage'});
  expect(within(usage).getByText('2.0k')).toBeTruthy();
  expect(within(usage).getByText('1.8k')).toBeTruthy(); // max(0, 2000 - 250)
  expect(within(usage).getByText('250')).toBeTruthy();
  expect(within(usage).getByText('$0.006')).toBeTruthy();
  expect(within(usage).getByText(/completeness details are unavailable/)).toBeTruthy();
  const metrics = screen.getByRole('region', {name: 'Recent execution metrics'});
  expect(within(metrics).getByText(/More execution metrics exist/)).toBeTruthy();
  const table = within(metrics).getByRole('table');
  const firstRow = within(table).getAllByRole('row')[1];
  expect(within(firstRow).getByRole('button', {name: 'Execution 51 details'})).toBeTruthy();
  expect(within(firstRow).getByText('1.0m')).toBeTruthy();
  expect(within(firstRow).getByText('No error recorded')).toBeTruthy();
  fireEvent.click(within(firstRow).getByRole('button', {name: 'Execution 51 details'}));
  expect(within(metrics).getByText('fixture-digest')).toBeTruthy();
  const slots = screen.getByRole('region', {name: 'Latest captured slot accounting'});
  expect(within(slots).getByText(/Latest captured turn: latest/)).toBeTruthy();
  expect(within(slots).getByText('mode')).toBeTruthy();
  expect(within(slots).getByText('0')).toBeTruthy();
  expect(within(slots).getByText('fixture-key')).toBeTruthy();
  expect(container.textContent).not.toContain('private');
  expect(screen.queryByLabelText(/reveal/i)).toBeNull();
  expect(container.firstChild.style.overflowY).toBe('auto');
  expect(fetch.mock.calls[0][0]).toBe('/api/plugins/nanite.diagnostics/diagnostics?session_id=session-a');
  expect(fetch.mock.calls[0][1].cache).toBe('no-store');
 });

 it('distinguishes no capture from an existing capture with no recorded slots', async () => {
  const fetch = vi.fn().mockResolvedValueOnce(response(reply('session-a', 'missing-capture'))).mockResolvedValueOnce(response(reply('session-a', 'empty-capture'))); vi.stubGlobal('fetch', fetch);
  render(<DiagnosticsPanel session_id="session-a" />);
  expect(await screen.findByText(/inspector may be disabled/)).toBeTruthy();
  fireEvent.click(screen.getByRole('button', {name: 'Refresh'}));
  expect(await screen.findByText('Capture exists; no slots recorded.')).toBeTruthy();
  expect(screen.queryByText(/inspector may be disabled/)).toBeNull();
 });

 it('keeps resource failures separate from unavailable captures and recorded zeros', async () => {
  const data = reply('session-a', 'missing-capture');
  data.usage = {error: 'Session or resource is outside the approved read scope', code: 'host_403'};
  data.execution_metrics = {data: fixture('empty-metrics')};
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(data)));
  render(<DiagnosticsPanel session_id="session-a" />);
  expect(await screen.findByRole('alert')).toBeTruthy();
  expect(screen.getByText('No execution metrics recorded yet.')).toBeTruthy();
  expect(screen.getByText(/Captured slot accounting unavailable/)).toBeTruthy();
  expect(screen.queryByText('$0.00')).toBeNull();
 });

 it('loads only on initial/session change or manual refresh, retaining sort and details', async () => {
  const fetch = vi.fn().mockResolvedValue(response(reply())); vi.stubGlobal('fetch', fetch);
  render(<DiagnosticsPanel session_id="session-a" />);
  await screen.findByRole('table');
  fireEvent.click(screen.getByRole('button', {name: 'Sort recent metrics by Duration'}));
  fireEvent.click(screen.getByRole('button', {name: 'Sort recent metrics by Duration'}));
  fireEvent.click(screen.getByRole('button', {name: 'Execution 2 details'}));
  fireEvent.click(screen.getByRole('button', {name: 'Refresh'}));
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
  await waitFor(() => expect(screen.getByRole('button', {name: 'Refresh'}).disabled).toBe(false));
  expect(screen.getByRole('button', {name: 'Execution 2 details'}).getAttribute('aria-expanded')).toBe('true');
  expect(screen.getByRole('button', {name: 'Sort recent metrics by Duration'}).closest('th').getAttribute('aria-sort')).toBe('ascending');
  fireEvent(window, new Event('focus')); fireEvent(document, new Event('visibilitychange'));
  vi.useFakeTimers();
  await act(async () => {vi.advanceTimersByTime(120000);});
  vi.useRealTimers();
  expect(fetch).toHaveBeenCalledTimes(2);
 });

 it('discards a late response after changing sessions and aborts the previous read', async () => {
  let finishFirst;
  const fetch = vi.fn().mockImplementationOnce(() => new Promise(resolve => {finishFirst = resolve;})).mockResolvedValueOnce(response(reply('session-b', 'missing-capture')));
  vi.stubGlobal('fetch', fetch);
  const {rerender} = render(<DiagnosticsPanel session_id="session-a" />);
  rerender(<DiagnosticsPanel session_id="session-b" />);
  await screen.findByText(/Captured slot accounting unavailable/);
  expect(fetch.mock.calls[0][1].signal.aborted).toBe(true);
  await act(async () => {finishFirst(response(reply('session-a')));});
  expect(screen.getByText('Session: session-b')).toBeTruthy();
  expect(screen.queryByText(/Latest captured turn: latest/)).toBeNull();
 });

 it('clears old accounting immediately when no session is selected and does not fetch', async () => {
  const fetch = vi.fn().mockResolvedValue(response(reply())); vi.stubGlobal('fetch', fetch);
  const {rerender} = render(<DiagnosticsPanel session_id={null} />);
  expect(screen.getByText(/Select a session/)).toBeTruthy(); expect(fetch).not.toHaveBeenCalled();
  rerender(<DiagnosticsPanel session_id="session-a" />); await screen.findByRole('table');
  rerender(<DiagnosticsPanel session_id={null} />);
  expect(screen.queryByRole('table')).toBeNull(); expect(fetch).toHaveBeenCalledTimes(1);
 });

 it('shows top-level 4xx messages and rejects mismatched responses', async () => {
  const fetch = vi.fn().mockResolvedValueOnce({ok: false, text: async () => 'One valid calling session_id is required'}).mockResolvedValueOnce(response(reply('other'))); vi.stubGlobal('fetch', fetch);
  render(<DiagnosticsPanel session_id="session-a" />);
  expect(await screen.findByText('One valid calling session_id is required')).toBeTruthy();
  fireEvent.click(screen.getByRole('button', {name: 'Refresh'}));
  expect(await screen.findByText('Invalid diagnostics response')).toBeTruthy();
  expect(screen.queryByRole('table')).toBeNull();
 });

 it('renders unknown slot light honestly and escapes supplied labels', async () => {
  const data = reply();
  data.context_slots = {data: {available: true, turn_id: 'turn', started_at: 'bad time', slots: [{name: '<script>window.bad=true</script>', tokens: 0, traffic_light: 'unexpected', cached: false, sensitive: false}]}};
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(data)));
  const {container} = render(<DiagnosticsPanel session_id="session-a" />);
  expect(await screen.findByText('unknown')).toBeTruthy();
  expect(screen.getByText('<script>window.bad=true</script>')).toBeTruthy();
  expect(container.querySelector('script')).toBeNull();
 });
});

it('labels the shipped reference as STATIC, with source and SHA, without any session read', () => {
 const fetch = vi.fn(); vi.stubGlobal('fetch', fetch);
 render(<SystemPromptsViewer />);
 expect(screen.getByRole('heading', {name: 'STATIC Prompt References'})).toBeTruthy();
 expect(screen.getByText(/goes stale by design/)).toBeTruthy();
 expect(screen.getByText(/Source: internal\/agent\/builtin\/profiles\/default.md/)).toBeTruthy();
 expect(screen.getAllByText(/^SHA: [0-9a-f]{40}$/)[0]).toBeTruthy();
 expect(fetch).not.toHaveBeenCalled();
 // No fixture-to-core or bundle-to-core text equality check: release-time reconciliation owns it.
});

it('retains pinned core token, cost and duration formatting at boundaries', () => {
 expect([999, 1000, 1000000].map(formatTokens)).toEqual(['999', '1.0k', '1.0M']);
 expect([0, 0.0005, 0.005, 0.5].map(formatUsageCost)).toEqual(['$0.00', '$0.0005', '$0.005', '$0.50']);
 expect([0, 0.005, 0.5].map(formatMetricCost)).toEqual(['—', '$0.0050', '$0.50']);
 expect([999, 1000, 60000].map(formatDuration)).toEqual(['999ms', '1.0s', '1.0m']);
});
