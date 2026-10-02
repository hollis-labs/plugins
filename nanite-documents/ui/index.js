import React, { useEffect, useState, useRef } from "react";
const h = React.createElement;
const base = "/api/plugins/nanite.documents";
const maxContent = 524288;
export function DocumentsTab({ session_id: sessionId }) {
	const lifetime = useRef({ session: sessionId, epoch: 0 });
	if (lifetime.current.session !== sessionId)
		lifetime.current = {
			session: sessionId,
			epoch: lifetime.current.epoch + 1,
		};
	const [state, setState] = useState({
		session: null,
		rows: [],
		more: false,
		offset: 0,
	});
	const [error, setError] = useState(""),
		[busy, setBusy] = useState(false),
		[revision, setRevision] = useState(0);
	const [name, setName] = useState(""),
		[text, setText] = useState(""),
		[selected, setSelected] = useState(null);
	const paging = useRef(false),
		wanted = useRef(100);
	const shown = state.session === sessionId ? state.rows : [];
	const preview = selected?.session === sessionId ? selected : null;
	useEffect(() => {
		setState({ session: null, rows: [], more: false, offset: 0 });
		setSelected(null);
		setBusy(false);
		setError("");
		setName("");
		setText("");
		paging.current = false;
		wanted.current = 100;
	}, [sessionId]);
	useEffect(() => {
		const abort = new AbortController();
		let current = true,
			loading = false;
		const load = async () => {
			if (loading || paging.current) return;
			loading = true;
			try {
				let rows = [],
					data,
					cursor = 0;
				do {
					const res = await fetch(
						`${base}/documents?session_id=${encodeURIComponent(sessionId)}&offset=${cursor}`,
						{ signal: abort.signal },
					);
					if (!res.ok) throw new Error("Could not load documents");
					data = await res.json();
					rows.push(...data.documents);
					if (data.more && data.next_offset <= cursor)
						throw new Error("Invalid document pagination");
					cursor = data.next_offset;
				} while (current && data.more && cursor < wanted.current);
				if (current) {
					setState({
						session: sessionId,
						rows,
						more: data.more,
						offset: cursor,
					});
				}
			} catch (e) {
				if (current && e.name !== "AbortError") setError(e.message);
			} finally {
				loading = false;
			}
		};
		const onFocus = () => {
			if (document.visibilityState !== "hidden") void load();
		};
		if (sessionId) {
			void load();
			window.addEventListener("focus", onFocus);
			document.addEventListener("visibilitychange", onFocus);
		}
		const timer = sessionId ? setInterval(load, 30000) : null;
		return () => {
			current = false;
			abort.abort();
			if (timer) clearInterval(timer);
			window.removeEventListener("focus", onFocus);
			document.removeEventListener("visibilitychange", onFocus);
		};
	}, [sessionId, revision]);
	const operation = async (fn) => {
		if (!sessionId || busy || state.session !== sessionId) return;
		const session = sessionId,
			epoch = lifetime.current.epoch;
		const live = () => lifetime.current.epoch === epoch;
		setBusy(true);
		setError("");
		const request = async (path, method = "GET", body) => {
			if (!live()) throw new Error("Session changed");
			const res = await fetch(
				`${base}${path}${path.includes("?") ? "&" : "?"}session_id=${encodeURIComponent(session)}`,
				{
					method,
					headers: body ? { "Content-Type": "application/json" } : undefined,
					body: body ? JSON.stringify(body) : undefined,
				},
			);
			if (!res.ok) throw new Error("Document request failed");
			return res.json();
		};
		try {
			await fn(request, live, session);
		} catch (e) {
			if (live()) setError(e.message);
		} finally {
			if (live()) setBusy(false);
		}
	};
	const create = async (request, body) => {
		if (new TextEncoder().encode(body.content).length > maxContent)
			throw new Error("Document content exceeds 512 KiB");
		await request("/documents", "POST", body);
	};
	const mutate = (id, method, body) =>
		operation(async (request, live) => {
			await request(`/documents/${encodeURIComponent(id)}`, method, body);
			if (live()) {
				if (method === "DELETE") setSelected(null);
				setRevision((r) => r + 1);
			}
		});
	const upload = (files) =>
		operation(async (request, live) => {
			try {
				for (const file of files) {
					if (
						!(
							file.type?.startsWith("text/") ||
							/\.(md|txt|json|yaml|yml|csv)$/i.test(file.name)
						)
					)
						throw new Error("Choose a supported text file");
					if (file.size > maxContent) throw new Error("File exceeds 512 KiB");
					const content = await file.text();
					if (!live()) return;
					await create(request, {
						name: file.name,
						mime_type: file.type || "text/plain",
						content,
					});
				}
			} finally {
				if (live()) setRevision((r) => r + 1);
			}
		});
	const view = (id) =>
		operation(async (request, live, session) => {
			const data = await request(`/documents/${encodeURIComponent(id)}`);
			if (live())
				setSelected({
					session,
					id,
					content: data.document.content || "",
					more: data.more,
					offset: data.next_offset,
				});
		});
	const nextContent = () =>
		operation(async (request, live) => {
			const data = await request(
				`/documents/${encodeURIComponent(preview.id)}?offset=${preview.offset}`,
			);
			if (data.more && data.next_offset <= preview.offset)
				throw new Error("Invalid content pagination");
			if (live())
				setSelected({
					...preview,
					content: preview.content + (data.document.content || ""),
					more: data.more,
					offset: data.next_offset,
				});
		});
	const nextPage = () =>
		operation(async (request, live) => {
			paging.current = true;
			try {
				const data = await request(`/documents?offset=${state.offset}`);
				if (data.more && data.next_offset <= state.offset)
					throw new Error("Invalid document pagination");
				if (live()) {
					wanted.current = data.next_offset;
					setState({
						...state,
						rows: [...state.rows, ...data.documents],
						more: data.more,
						offset: data.next_offset,
					});
				}
			} finally {
				if (live()) paging.current = false;
			}
		});
	if (!sessionId) return h("p", null, "Select a session to view documents.");
	const disabled = busy || state.session !== sessionId;
	return h(
		"section",
		{
			"aria-label": "Documents",
			style: { overflow: "auto", height: "100%", minHeight: 0 },
		},
		h(
			"p",
			null,
			"Upload or paste text documents (up to 512 KiB each). Include selected documents as full content or a summary.",
		),
		error && h("p", { role: "alert" }, error),
		h(
			"label",
			null,
			"Upload files",
			h("input", {
				type: "file",
				accept: "text/*,.md,.txt,.json,.yaml,.yml,.csv",
				multiple: true,
				disabled,
				onChange: (e) => {
					const files = Array.from(e.target.files || []);
					e.target.value = "";
					void upload(files);
				},
			}),
		),
		h(
			"form",
			{
				onSubmit: (e) => {
					e.preventDefault();
					void operation(async (request, live) => {
						const pastedName = name.trim(),
							content = text.trim();
						if (!pastedName || !content)
							throw new Error("Paste a document name and non-empty content");
						await create(request, { name: pastedName, content });
						if (live()) {
							setName("");
							setText("");
							setRevision((r) => r + 1);
						}
					});
				},
			},
			h(
				"label",
				null,
				"Document name",
				h("input", {
					value: name,
					maxLength: 512,
					required: true,
					onChange: (e) => setName(e.target.value),
				}),
			),
			h(
				"label",
				null,
				"Paste content",
				h("textarea", {
					value: text,
					maxLength: maxContent,
					onChange: (e) => setText(e.target.value),
				}),
			),
			h("button", { type: "submit", disabled }, "Add document"),
		),
		shown.length === 0 && h("p", null, "No documents."),
		h(
			"ul",
			null,
			...shown.map((row) =>
				h(
					"li",
					{ key: row.id },
					h("strong", null, row.name),
					h("small", null, ` ${row.size_bytes} bytes · ${row.mime_type}`),
					h(
						"label",
						null,
						h("input", {
							type: "checkbox",
							checked: row.included,
							disabled,
							onChange: (e) =>
								void mutate(row.id, "PATCH", { included: e.target.checked }),
						}),
						"Include in context",
					),
					h(
						"label",
						null,
						h("input", {
							type: "checkbox",
							checked: row.full_content,
							disabled,
							onChange: (e) =>
								void mutate(row.id, "PATCH", {
									full_content: e.target.checked,
								}),
						}),
						"Full content",
					),
					h(
						"p",
						null,
						row.summary ||
							"Pointer mode uses the document ID and size when no summary is set.",
					),
					h(
						"button",
						{
							disabled,
							onClick: () => {
								const summary = window.prompt("Document summary", row.summary);
								if (summary !== null) void mutate(row.id, "PATCH", { summary });
							},
						},
						"Edit summary",
					),
					h(
						"button",
						{ disabled, onClick: () => void view(row.id) },
						"View content",
					),
					h(
						"button",
						{ disabled, onClick: () => void mutate(row.id, "DELETE") },
						"Delete",
					),
				),
			),
		),
		state.session === sessionId &&
			state.more &&
			h(
				"button",
				{ disabled, onClick: () => void nextPage() },
				"Load more documents",
			),
		preview &&
			h(
				"section",
				{ "aria-label": "Document content" },
				h("pre", null, preview.content),
				preview.more &&
					h(
						"button",
						{ disabled, onClick: () => void nextContent() },
						"Load more content",
					),
			),
	);
}
