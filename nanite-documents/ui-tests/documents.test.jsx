import React from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { DocumentsTab } from "../ui/index.js";

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
	vi.unstubAllGlobals();
});
const row = {
	id: "doc/a",
	name: "Reference",
	mime_type: "text/plain",
	size_bytes: 7,
	included: false,
	full_content: false,
	summary: "",
};
const reply = (data) => ({ ok: true, json: async () => data });
const page = (rows) =>
	reply({ documents: rows, more: false, next_offset: rows.length });
const setup = (handler) => {
	const fetch = vi.fn(handler || (() => Promise.resolve(page([row]))));
	vi.stubGlobal("fetch", fetch);
	return fetch;
};
describe("Documents drawer", () => {
	it("creates pasted text with the current session and displays uploaded text as text", async () => {
		const fetch = setup((url, options) => {
			if (options?.method === "POST") return Promise.resolve(reply(row));
			if (url.includes("/doc%2Fa"))
				return Promise.resolve(
					reply({
						document: { content: "<script>unsafe()</script>" },
						more: false,
						next_offset: 25,
					}),
				);
			return Promise.resolve(page([row]));
		});
		render(<DocumentsTab session_id="session/a" />);
		await screen.findByText("Reference");
		fireEvent.change(screen.getByLabelText("Document name"), {
			target: { value: "Pasted" },
		});
		fireEvent.change(screen.getByLabelText("Paste content"), {
			target: { value: "keep\n spacing" },
		});
		fireEvent.click(screen.getByText("Add document"));
		await waitFor(() =>
			expect(
				fetch.mock.calls.some(
					([url, o]) =>
						o?.method === "POST" &&
						url.includes("session_id=session%2Fa") &&
						JSON.parse(o.body).content === "keep\n spacing",
				),
			).toBe(true),
		);
		await waitFor(() =>
			expect(screen.getByText("View content").disabled).toBe(false),
		);
		fireEvent.click(screen.getByText("View content"));
		await screen.findByText("<script>unsafe()</script>");
		expect(document.querySelector("script")).toBeNull();
	});
	it("uploads through file.text() and rejects oversized files before reading", async () => {
		const fetch = setup((url, o) =>
			Promise.resolve(o?.method === "POST" ? reply(row) : page([row])),
		);
		render(<DocumentsTab session_id="session-a" />);
		await screen.findByText("Reference");
		const text = vi.fn(async () => "uploaded text");
		fireEvent.change(screen.getByLabelText("Upload files"), {
			target: {
				files: [{ name: "file.txt", type: "text/plain", size: 13, text }],
			},
		});
		await waitFor(() =>
			expect(
				fetch.mock.calls.some(
					([, o]) =>
						o?.method === "POST" &&
						JSON.parse(o.body).content === "uploaded text",
				),
			).toBe(true),
		);
		expect(text).toHaveBeenCalledOnce();
		await waitFor(() =>
			expect(screen.getByLabelText("Upload files").disabled).toBe(false),
		);
		const tooLarge = vi.fn(async () => "");
		fireEvent.change(screen.getByLabelText("Upload files"), {
			target: { files: [{ name: "big", size: 524289, text: tooLarge }] },
		});
		await screen.findByRole("alert");
		expect(tooLarge).not.toHaveBeenCalled();
	});
	it("patches one setting at a time and deletes by encoded document ID", async () => {
		const fetch = setup((url, o) =>
			Promise.resolve(o?.method ? reply({ ok: true }) : page([row])),
		);
		render(<DocumentsTab session_id="session-a" />);
		await screen.findByText("Reference");
		fireEvent.click(screen.getByLabelText("Include in context"));
		await waitFor(() =>
			expect(
				fetch.mock.calls.some(
					([url, o]) =>
						o?.method === "PATCH" &&
						url.includes("/doc%2Fa?") &&
						o.body === '{"included":true}',
				),
			).toBe(true),
		);
		await waitFor(() =>
			expect(screen.getByText("Delete").disabled).toBe(false),
		);
		fireEvent.click(screen.getByLabelText("Full content"));
		await waitFor(() =>
			expect(
				fetch.mock.calls.some(([, o]) => o?.body === '{"full_content":true}'),
			).toBe(true),
		);
		await waitFor(() =>
			expect(screen.getByText("Delete").disabled).toBe(false),
		);
		fireEvent.click(screen.getByText("Delete"));
		await waitFor(() =>
			expect(
				fetch.mock.calls.some(
					([url, o]) =>
						o?.method === "DELETE" && url.includes("session_id=session-a"),
				),
			).toBe(true),
		);
	});
	it("hides old rows and ignores a stale upload after session switches", async () => {
		let release;
		const text = vi.fn(
			() =>
				new Promise((resolve) => {
					release = resolve;
				}),
		);
		const fetch = setup((url) =>
			Promise.resolve(
				page(
					url.includes("session-b")
						? [{ ...row, name: "Other session" }]
						: [row],
				),
			),
		);
		const view = render(<DocumentsTab session_id="session-a" />);
		await screen.findByText("Reference");
		fireEvent.change(screen.getByLabelText("Upload files"), {
			target: { files: [{ name: "late", size: 4, text }] },
		});
		expect(text).toHaveBeenCalledOnce();
		view.rerender(<DocumentsTab session_id="session-b" />);
		expect(screen.queryByText("Reference")).toBeNull();
		await screen.findByText("Other session");
		release("late");
		await new Promise((resolve) => setTimeout(resolve, 20));
		expect(fetch.mock.calls.some(([, o]) => o?.method === "POST")).toBe(false);
	});
	it("loads additional document pages and content chunks", async () => {
		const fetch = setup((url) => {
			if (url.includes("/doc%2Fa"))
				return Promise.resolve(
					reply({
						document: { content: url.includes("offset=3") ? "two" : "one" },
						more: !url.includes("offset=3"),
						next_offset: url.includes("offset=3") ? 6 : 3,
					}),
				);
			if (url.includes("offset=100"))
				return Promise.resolve(
					page([{ ...row, id: "second", name: "Second" }]),
				);
			return Promise.resolve(
				reply({ documents: [row], more: true, next_offset: 100 }),
			);
		});
		render(<DocumentsTab session_id="session-a" />);
		await screen.findByText("Reference");
		fireEvent.click(screen.getByText("Load more documents"));
		await screen.findByText("Second");
		fireEvent.click(screen.getAllByText("View content")[0]);
		await screen.findByText("one");
		fireEvent.click(screen.getByText("Load more content"));
		await screen.findByText("onetwo");
		expect(
			fetch.mock.calls.some(([url]) =>
				url.includes("offset=3&session_id=session-a"),
			),
		).toBe(true);
	});
});
