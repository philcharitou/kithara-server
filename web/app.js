// The kithara-server web page. Plain modules, no build step, no dependencies. It signs
// in like an app and uses the same API; every bit of text from the server is set with
// textContent, never parsed as HTML.

const app = document.getElementById("app");
const dialog = document.getElementById("book");
const TOKEN_KEY = "kithara.token";

const state = {
  token: null,
  me: null,
  server: null,
  libraries: [],
  library: "",
  books: [],
  progress: new Map(),
  query: "",
  view: "library",
};

// ------------------------------------------------------------------ helpers

function el(tag, attrs = {}, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") n.className = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else if (k === "value") n.value = v;
    else if (k === "style") n.style.cssText = v; // through the CSSOM: the CSP forbids style attributes
    else if (k === "checked") n.checked = !!v;
    else n.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat(Infinity)) {
    if (c === undefined || c === null || c === false) continue;
    n.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return n;
}

function toast(msg) {
  const t = document.getElementById("toast");
  t.textContent = msg;
  t.classList.add("show");
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => t.classList.remove("show"), 3200);
}

function storage() {
  try { return window.localStorage; } catch { return null; }
}

function saveToken(token, remember) {
  state.token = token;
  try {
    sessionStorage.setItem(TOKEN_KEY, token);
    if (remember) storage()?.setItem(TOKEN_KEY, token);
  } catch { /* private window: the token lives in memory only */ }
}

function loadToken() {
  try { return sessionStorage.getItem(TOKEN_KEY) || storage()?.getItem(TOKEN_KEY); } catch { return null; }
}

function forgetToken() {
  state.token = null;
  try { sessionStorage.removeItem(TOKEN_KEY); storage()?.removeItem(TOKEN_KEY); } catch { /* nothing kept */ }
}

class ApiError extends Error {
  constructor(status, message) { super(message); this.status = status; }
}

async function api(path, { method = "GET", body, raw = false } = {}) {
  const headers = {};
  if (state.token) headers.Authorization = "Bearer " + state.token;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  if (res.status === 401 && state.token) {
    forgetToken();
    renderSignIn("Signed out. Sign in again.");
    throw new ApiError(401, "signed out");
  }
  if (raw) return res;
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) throw new ApiError(res.status, data?.error || res.statusText);
  return data;
}

const fmtDuration = (ms) => {
  const m = Math.round(ms / 60000);
  const h = Math.floor(m / 60);
  return h ? `${h}h ${String(m % 60).padStart(2, "0")}m` : `${m}m`;
};
const fmtDate = (ms) => (ms ? new Date(ms).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }) : "");
const fmtDateTime = (ms) => (ms ? new Date(ms).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : "never");
const fmtBytes = (n) => (n >= 1e9 ? (n / 1e9).toFixed(1) + " GB" : n >= 1e6 ? (n / 1e6).toFixed(0) + " MB" : Math.max(1, Math.round(n / 1e3)) + " KB");
const isAdmin = () => state.me?.role === "admin";

// Covers need the bearer token, which an <img> cannot send: fetch them, show blobs,
// and only when they scroll into view.
const coverCache = new Map();
const coverObserver = new IntersectionObserver((entries) => {
  for (const e of entries) {
    if (!e.isIntersecting) continue;
    coverObserver.unobserve(e.target);
    fillCover(e.target);
  }
}, { rootMargin: "300px" });

async function fillCover(box) {
  const url = box.dataset.src;
  try {
    let blobURL = coverCache.get(url);
    if (!blobURL) {
      const res = await api(url, { raw: true });
      if (!res.ok) return;
      blobURL = URL.createObjectURL(await res.blob());
      coverCache.set(url, blobURL);
    }
    const img = el("img", { class: box.className, src: blobURL, alt: "" });
    box.replaceWith(img);
  } catch { /* leave the placeholder */ }
}

function coverBox(book, extra = "") {
  const box = el("div", { class: "cover " + extra, "aria-hidden": "true" }, book.coverUrl ? "" : "No cover");
  if (book.coverUrl) {
    // updatedAt in the address, so a changed cover is fetched again.
    box.dataset.src = book.coverUrl + "?v=" + book.updatedAt;
    coverObserver.observe(box);
  }
  return box;
}

// ------------------------------------------------------------------ sign in

function renderSignIn(message = "") {
  dialog.open && dialog.close();
  const err = el("p", { class: "error small" }, message);
  const user = el("input", { type: "text", name: "username", autocomplete: "username", required: true, autofocus: true });
  const pass = el("input", { type: "password", name: "password", autocomplete: "current-password", required: true });
  const remember = el("input", { type: "checkbox", name: "remember" });
  const submit = el("button", { class: "btn primary", type: "submit" }, "Sign in");
  const form = el("form", {
    onsubmit: async (ev) => {
      ev.preventDefault();
      submit.disabled = true;
      err.textContent = "";
      try {
        const out = await api("/api/auth", { method: "POST", body: { username: user.value, password: pass.value, device: browserName() } });
        saveToken(out.token, remember.checked);
        await start();
      } catch (e) {
        err.textContent = e.status === 429 ? "Too many failed attempts. Try again in a few minutes." : e.message;
        submit.disabled = false;
      }
    },
  },
    el("h1", {}, state.server?.name || "Kithara server"),
    el("p", { class: "muted small" }, "Sign in with your Kithara account."),
    el("label", {}, el("span", {}, "User name"), user),
    el("label", {}, el("span", {}, "Password"), pass),
    el("label", { class: "check" }, remember, "Keep me signed in on this browser"),
    err,
    submit,
  );
  app.replaceChildren(el("div", { class: "signin" }, form));
}

function browserName() {
  const ua = navigator.userAgent;
  const b = /Firefox\//.test(ua) ? "Firefox" : /Edg\//.test(ua) ? "Edge" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "Browser";
  const os = /Windows/.test(ua) ? "Windows" : /Mac OS X/.test(ua) ? "macOS" : /Android/.test(ua) ? "Android" : /iPhone|iPad/.test(ua) ? "iOS" : /Linux/.test(ua) ? "Linux" : "";
  return `Web (${b}${os ? " on " + os : ""})`;
}

// ------------------------------------------------------------------ shell

const VIEWS = [
  ["library", "Library"],
  ["stats", "Listening"],
  ["devices", "Devices"],
  ["users", "Users", true],
];

function renderShell() {
  const tabs = el("nav", { class: "tabs", "aria-label": "Sections" },
    VIEWS.filter(([, , admin]) => !admin || isAdmin()).map(([id, label]) =>
      el("button", { type: "button", "aria-current": state.view === id ? "page" : null, onclick: () => go(id) }, label)));
  const header = el("header", { class: "top" }, el("div", { class: "inner" },
    el("span", { class: "brand" }, state.server?.name || "Kithara"),
    tabs,
    el("div", { class: "who" },
      el("span", { class: "small muted" }, state.me.name, isAdmin() ? " · admin" : ""),
      el("button", { class: "btn small", type: "button", onclick: signOut }, "Sign out")),
  ));
  const main = el("main", { id: "main" });
  app.replaceChildren(header, main);
  return main;
}

async function signOut() {
  try { await api("/api/auth/logout", { method: "POST" }); } catch { /* signed out regardless */ }
  forgetToken();
  renderSignIn();
}

function go(view) {
  state.view = view;
  try { history.replaceState(null, "", "#" + view); } catch { /* fine */ }
  render();
}

async function render() {
  const main = renderShell();
  main.append(el("p", { class: "muted" }, "Loading…"));
  try {
    const view = { library: viewLibrary, stats: viewStats, devices: viewDevices, users: viewUsers }[state.view] || viewLibrary;
    main.replaceChildren(...[await view()].flat());
  } catch (e) {
    if (e.status !== 401) main.replaceChildren(el("p", { class: "error" }, e.message));
  }
}

// ------------------------------------------------------------------ library

async function loadLibrary() {
  const q = state.library ? "?library=" + encodeURIComponent(state.library) : "";
  const [cat, prog, libs] = await Promise.all([api("/api/library" + q), api("/api/progress"), api("/api/libraries")]);
  state.books = cat.books;
  state.libraries = libs.libraries;
  state.progress = new Map(prog.progress.map((p) => [p.bookId, p]));
}

function matches(b, q) {
  if (!q) return true;
  const hay = [b.title, b.subtitle, b.author, b.narrator, b.series, ...(b.genres || [])].join(" ").toLowerCase();
  return q.toLowerCase().split(/\s+/).every((w) => hay.includes(w));
}

async function viewLibrary() {
  await loadLibrary();
  const grid = el("div", { class: "grid" });
  const count = el("span", { class: "muted small" });
  const fill = () => {
    const shown = state.books.filter((b) => matches(b, state.query));
    count.textContent = `${shown.length} of ${state.books.length} books`;
    grid.replaceChildren(...shown.map(bookCard));
    if (!shown.length) grid.append(el("p", { class: "muted" }, state.books.length ? "Nothing matches." : "No books here yet."));
  };
  const search = el("input", { type: "search", class: "grow", placeholder: "Search title, author, narrator, series", value: state.query,
    oninput: (e) => { state.query = e.target.value; fill(); } });
  const toolbar = el("div", { class: "toolbar" }, search);
  if (state.libraries.length > 1) {
    toolbar.append(el("select", { "aria-label": "Library", onchange: (e) => { state.library = e.target.value; render(); } },
      el("option", { value: "" }, "All libraries"),
      state.libraries.map((l) => el("option", { value: l.name, selected: l.name === state.library ? true : null }, `${l.name} (${l.books})`))));
  }
  if (isAdmin()) {
    const btn = el("button", { class: "btn", type: "button", onclick: async () => {
      btn.disabled = true;
      try { const r = await api("/api/rescan", { method: "POST" }); toast(`Rescanned: ${r.books} books`); render(); }
      catch (e) { toast(e.message); btn.disabled = false; }
    } }, "Rescan");
    toolbar.append(btn);
  }
  toolbar.append(count);
  fill();
  return [toolbar, grid];
}

function progressOf(b) {
  const p = state.progress.get(b.id);
  if (!p) return null;
  return { ...p, fraction: b.durationMs ? Math.min(1, p.positionMs / b.durationMs) : 0 };
}

function bookCard(b) {
  const p = progressOf(b);
  return el("button", { class: "card", type: "button", onclick: () => openBook(b.id) },
    coverBox(b),
    p && !p.finished && p.positionMs > 0 ? el("div", { class: "bar", title: Math.round(p.fraction * 100) + "%" }, el("i", { style: `width:${(p.fraction * 100).toFixed(1)}%` })) : null,
    el("span", { class: "title" }, b.title),
    el("span", { class: "by" }, b.author || "Unknown author"),
    p?.finished ? el("span", { class: "badge done" }, "Finished") : null,
  );
}

// ------------------------------------------------------------------ one book

async function openBook(id, mode = "view") {
  const b = state.books.find((x) => x.id === id);
  if (!b) return;
  const sheet = el("div", { class: "sheet" });
  dialog.replaceChildren(el("button", { class: "btn small close", type: "button", onclick: () => dialog.close(), "aria-label": "Close" }, "Close"), sheet);
  if (!dialog.open) dialog.showModal();
  sheet.append(...(mode === "edit" ? bookEditor(b) : mode === "match" ? bookMatcher(b) : bookDetail(b)).filter(Boolean));
}

dialog.addEventListener("click", (e) => { if (e.target === dialog) dialog.close(); });

function bookDetail(b) {
  const p = progressOf(b);
  const facts = el("dl", { class: "facts" });
  const fact = (k, v) => v && facts.append(el("dt", {}, k), el("dd", {}, v));
  fact("Author", b.author);
  fact("Narrator", b.narrator);
  fact("Series", b.series && (b.series + (b.seriesIndex != null ? " #" + b.seriesIndex : "")));
  fact("Length", fmtDuration(b.durationMs));
  fact("Published", [b.publishedYear, b.publisher].filter(Boolean).join(", "));
  fact("Genres", (b.genres || []).join(", "));
  fact("Language", b.language);
  fact("ISBN", b.isbn);
  fact("ASIN", b.asin);
  fact("Library", state.libraries.length > 1 ? b.library : "");
  if (p) {
    fact("Progress", p.finished ? `Finished ${fmtDate(p.finishedAt)}` : `${fmtDuration(p.positionMs)} of ${fmtDuration(b.durationMs)} (${Math.round(p.fraction * 100)}%)`);
    fact("Started", fmtDate(p.startedAt));
  }
  const size = b.files.reduce((n, f) => n + (f.sizeBytes || 0), 0);
  const download = el("button", { class: "btn primary", type: "button", onclick: async () => {
    download.disabled = true;
    try {
      const link = await api(`/api/books/${encodeURIComponent(b.id)}/download-link`, { method: "POST" });
      const a = el("a", { href: link.url, download: "" });
      document.body.append(a); a.click(); a.remove();
    } catch (e) { toast(e.message); }
    download.disabled = false;
  } }, `Download (${fmtBytes(size)})`);
  const actions = el("div", { class: "row" }, download);
  if (isAdmin()) {
    actions.append(
      el("button", { class: "btn", type: "button", onclick: () => openBook(b.id, "match") }, "Find metadata"),
      el("button", { class: "btn", type: "button", onclick: () => openBook(b.id, "edit") }, "Edit"));
  }
  return [
    el("div", { class: "head" }, coverBox(b), el("div", {},
      el("h2", {}, b.title),
      b.subtitle ? el("p", { class: "muted" }, b.subtitle) : null,
      facts)),
    actions,
    b.description ? el("p", { class: "desc" }, b.description) : null,
    b.chapters.length ? el("details", {}, el("summary", {}, `${b.chapters.length} chapters`),
      el("div", { class: "list small" }, b.chapters.map((c) => el("div", { class: "row" }, el("span", { class: "grow" }, c.title), el("span", { class: "muted" }, fmtDuration(c.startMs)))))) : null,
    el("details", {}, el("summary", {}, `${b.files.length} file${b.files.length === 1 ? "" : "s"}`),
      el("div", { class: "list small" }, b.files.map((f) => el("div", { class: "row" }, el("span", { class: "grow" }, f.name), el("span", { class: "muted" }, fmtDuration(f.durationMs), " · ", fmtBytes(f.sizeBytes)))))),
  ];
}

// Fields an admin can set by hand; saved values replace what the library says.
const FIELDS = [
  ["title", "Title"], ["subtitle", "Subtitle"], ["authors", "Authors (comma-separated)"], ["narrators", "Narrators (comma-separated)"],
  ["series", "Series"], ["seriesIndex", "Number in series"], ["publishedYear", "Year"], ["publisher", "Publisher"],
  ["language", "Language"], ["genres", "Genres (comma-separated)"], ["isbn", "ISBN"], ["asin", "ASIN"],
];
const LISTS = new Set(["authors", "narrators", "genres"]);
const split = (s) => s.split(",").map((x) => x.trim()).filter(Boolean);

function bookEditor(b) {
  const initial = {
    title: b.title, subtitle: b.subtitle, authors: b.author, narrators: b.narrator, series: b.series,
    seriesIndex: b.seriesIndex ?? "", publishedYear: b.publishedYear, publisher: b.publisher, language: b.language,
    genres: (b.genres || []).join(", "), isbn: b.isbn, asin: b.asin,
  };
  const inputs = {};
  const form = el("form", { class: "form", onsubmit: async (ev) => {
    ev.preventDefault();
    const body = {};
    for (const [k] of FIELDS) {
      const v = inputs[k].value.trim();
      if (!v) continue;
      if (LISTS.has(k)) body[k] = split(v);
      else if (k === "seriesIndex") body[k] = Number(v);
      else body[k] = v;
    }
    const d = desc.value.trim();
    if (d) body.description = d;
    await saveMetadata(b, body);
  } },
  FIELDS.map(([k, label]) => el("label", {}, el("span", {}, label), inputs[k] = el("input", { type: k === "seriesIndex" ? "number" : "text", step: "any", value: initial[k] ?? "" }))));
  const desc = el("textarea", { value: b.description || "" });
  form.append(
    el("label", { class: "wide" }, el("span", {}, "Description"), desc),
    el("p", { class: "wide small muted" }, "Saved metadata is kept in the server's data folder and takes priority over what the files say. Reset brings back what the library says."),
    el("div", { class: "row wide" },
      el("button", { class: "btn primary", type: "submit" }, "Save"),
      el("button", { class: "btn", type: "button", onclick: () => openBook(b.id) }, "Cancel"),
      el("button", { class: "btn danger", type: "button", onclick: () => resetMetadata(b) }, "Reset to library")),
  );
  return [el("h2", {}, "Edit ", b.title), form];
}

async function saveMetadata(b, body) {
  try {
    const updated = await api(`/api/books/${encodeURIComponent(b.id)}/metadata`, { method: "PUT", body });
    replaceBook(updated);
    toast("Saved");
    openBook(updated.id);
  } catch (e) { toast(e.message); }
}

async function resetMetadata(b) {
  if (!confirm(`Forget the saved metadata and cover for “${b.title}”?`)) return;
  try {
    const updated = await api(`/api/books/${encodeURIComponent(b.id)}/metadata`, { method: "DELETE" });
    replaceBook(updated);
    toast("Back to what the library says");
    openBook(updated.id);
  } catch (e) { toast(e.message); }
}

function replaceBook(updated) {
  const i = state.books.findIndex((x) => x.id === updated.id);
  if (i >= 0) state.books[i] = updated;
  render();
}

function bookMatcher(b) {
  const provider = el("select", { "aria-label": "Provider" },
    el("option", { value: "audible" }, "Audible"), el("option", { value: "openlibrary" }, "Open Library"), el("option", { value: "google" }, "Google Books"));
  const region = el("select", { "aria-label": "Audible region" },
    ["us", "uk", "ca", "au", "de", "fr", "it", "es", "in", "jp"].map((r) => el("option", { value: r }, r.toUpperCase())));
  const title = el("input", { type: "text", value: b.title, "aria-label": "Title" });
  const author = el("input", { type: "text", value: (b.author || "").split(",")[0], "aria-label": "Author" });
  const results = el("div", { class: "results" });
  provider.addEventListener("change", () => { region.hidden = provider.value !== "audible"; });
  const search = async (ev) => {
    ev?.preventDefault();
    results.replaceChildren(el("p", { class: "muted" }, "Searching…"));
    const q = new URLSearchParams({ provider: provider.value, title: title.value, author: author.value });
    if (provider.value === "audible") q.set("region", region.value);
    try {
      const out = await api(`/api/books/${encodeURIComponent(b.id)}/match?` + q);
      results.replaceChildren(...(out.results.length ? out.results.map((c) => matchResult(b, c)) : [el("p", { class: "muted" }, "Nothing found. Try a shorter title or another provider.")]));
    } catch (e) {
      results.replaceChildren(el("p", { class: "error" }, e.message));
    }
  };
  const form = el("form", { class: "row", onsubmit: search },
    provider, region, el("div", { class: "grow" }, title), el("div", { class: "grow" }, author),
    el("button", { class: "btn primary", type: "submit" }, "Search"));
  queueMicrotask(search);
  return [
    el("h2", {}, "Find metadata for ", b.title),
    el("p", { class: "small muted" }, "The server asks the provider only when you search. The result you choose, and its cover, replace what the library says until you reset it."),
    form,
    results,
    el("div", { class: "row" }, el("button", { class: "btn", type: "button", onclick: () => openBook(b.id) }, "Back")),
  ];
}

function matchResult(b, c) {
  const series = c.series ? ` · ${c.series}${c.seriesIndex != null ? " #" + c.seriesIndex : ""}` : "";
  const keepCover = el("input", { type: "checkbox", checked: !!c.coverUrl, disabled: !c.coverUrl });
  return el("div", { class: "result" },
    c.coverUrl ? el("img", { src: c.coverUrl, alt: "", loading: "lazy", referrerpolicy: "no-referrer" }) : el("div", { class: "cover" }, "—"),
    el("div", {},
      el("strong", {}, c.title), c.subtitle ? el("span", { class: "muted" }, ": ", c.subtitle) : null,
      el("div", { class: "small" }, (c.authors || []).join(", "), c.narrators?.length ? " · read by " + c.narrators.join(", ") : ""),
      el("div", { class: "small muted" }, [c.publishedYear, c.publisher].filter(Boolean).join(", "), series, c.durationMs ? " · " + fmtDuration(c.durationMs) : "")),
    el("div", { class: "row" },
      c.coverUrl ? el("label", { class: "check small" }, keepCover, "Cover") : null,
      el("button", { class: "btn primary small", type: "button", onclick: () => {
        const body = { ...c };
        if (!keepCover.checked) delete body.coverUrl;
        saveMetadata(b, body);
      } }, "Use")));
}

// ------------------------------------------------------------------ listening stats

async function viewStats() {
  const tz = -new Date().getTimezoneOffset();
  const [st] = await Promise.all([api("/api/stats?tzOffset=" + tz), state.books.length ? null : loadLibrary()]);
  const byId = new Map(state.books.map((b) => [b.id, b]));
  const tile = (v, k) => el("div", { class: "tile" }, el("div", { class: "v" }, v), el("div", { class: "k" }, k));

  // The last 30 days, today included, gaps as empty bars.
  const days = new Map(st.days.map((d) => [d.date, d.listenedMs]));
  const series = [];
  for (let i = 29; i >= 0; i--) {
    const d = new Date(Date.now() - i * 86400000 + tz * 60000).toISOString().slice(0, 10);
    series.push([d, days.get(d) || 0]);
  }
  const peak = Math.max(1, ...series.map(([, ms]) => ms));
  const chart = el("div", { class: "chart", role: "img", "aria-label": "Listening per day, last 30 days" },
    series.map(([d, ms]) => el("div", { class: ms ? "" : "zero", style: `height:${Math.max(1, (ms / peak) * 100)}%`, title: `${d}: ${fmtDuration(ms)}` })));

  const top = st.books.slice(0, 15);
  return [
    el("div", { class: "tiles" },
      tile(fmtDuration(st.totalListenedMs), "listened in all"),
      tile(st.currentStreakDays + (st.currentStreakDays === 1 ? " day" : " days"), "current streak"),
      tile(st.longestStreakDays + (st.longestStreakDays === 1 ? " day" : " days"), "longest streak"),
      tile(st.daysListened, "days with listening"),
      tile(st.booksFinished, "books finished"),
      tile(st.booksStarted, "books started")),
    el("section", { class: "panel" },
      el("h2", {}, "Last 30 days"),
      chart,
      el("div", { class: "chart-axis" }, el("span", {}, series[0][0]), el("span", {}, "today"))),
    el("section", { class: "panel" },
      el("h2", {}, "Recently listened"),
      top.length ? el("div", { class: "scroll" }, el("table", { class: "table" },
        el("thead", {}, el("tr", {}, el("th", {}, "Book"), el("th", {}, "Listened"), el("th", {}, "Last"), el("th", {}, "Finished"))),
        el("tbody", {}, top.map((r) => el("tr", {},
          el("td", {}, byId.get(r.bookId)?.title || el("span", { class: "muted" }, "(not in your library)")),
          el("td", {}, fmtDuration(r.listenedMs)),
          el("td", {}, fmtDate(r.lastListenedAt)),
          el("td", {}, fmtDate(r.finishedAt))))))) : el("p", { class: "muted" }, "Nothing yet. Listening time shows up here as the apps sync."),
      el("p", { class: "small muted" }, "Worked out from the apps' progress syncs, so it is an estimate and leans low.")),
  ];
}

// ------------------------------------------------------------------ devices

async function viewDevices(who = state.me.name) {
  const q = who === state.me.name ? "" : "?user=" + encodeURIComponent(who);
  const { devices } = await api("/api/devices" + q);
  const out = [];
  if (isAdmin()) {
    const { users } = await api("/api/users");
    out.push(el("div", { class: "toolbar" }, el("label", { class: "row" }, el("span", {}, "Devices of"),
      el("select", { onchange: async (e) => { const main = document.getElementById("main"); main.replaceChildren(...(await viewDevices(e.target.value))); } },
        users.map((u) => el("option", { value: u.name, selected: u.name === who ? true : null }, u.name))))));
  }
  const revoke = async (d) => {
    if (!confirm(d.current ? "Sign out this browser?" : `Sign out “${d.name || d.id}”? It will ask for the password again.`)) return;
    try {
      await api(`/api/devices/${encodeURIComponent(d.id)}${q}`, { method: "DELETE" });
      if (d.current) { forgetToken(); renderSignIn(); return; }
      toast("Signed out");
      document.getElementById("main").replaceChildren(...(await viewDevices(who)));
    } catch (e) { toast(e.message); }
  };
  out.push(el("section", { class: "panel" },
    el("h2", {}, who === state.me.name ? "Your devices" : `${who}’s devices`),
    devices.length ? el("div", { class: "scroll" }, el("table", { class: "table" },
      el("thead", {}, el("tr", {}, el("th", {}, "Device"), el("th", {}, "Signed in"), el("th", {}, "Last seen"), el("th", {}))),
      el("tbody", {}, devices.map((d) => el("tr", {},
        el("td", {}, d.name || el("span", { class: "muted" }, "Unnamed"), d.current ? el("span", { class: "badge" }, " this browser") : null),
        el("td", {}, fmtDate(d.createdAt)),
        el("td", {}, fmtDateTime(d.lastSeenAt)),
        el("td", {}, el("button", { class: "btn small danger", type: "button", onclick: () => revoke(d) }, "Sign out"))))))) : el("p", { class: "muted" }, "No devices signed in."),
    el("p", { class: "small muted" }, "Last-seen times are updated about once an hour.")));
  return out;
}

// ------------------------------------------------------------------ users (admin)

async function viewUsers() {
  const [{ users }, { libraries }] = await Promise.all([api("/api/users"), api("/api/libraries")]);
  const rerender = async () => document.getElementById("main").replaceChildren(...(await viewUsers()));
  const libraryPicker = (selected = []) => {
    const boxes = libraries.map((l) => el("input", { type: "checkbox", value: l.name, checked: selected.some((s) => s.toLowerCase() === l.name.toLowerCase()) }));
    const node = el("div", { class: "row" }, libraries.length > 1 ? boxes.map((b, i) => el("label", { class: "check" }, b, libraries[i].name)) : el("span", { class: "muted small" }, "One library; nothing to limit."));
    // None ticked means all libraries.
    return { node, value: () => boxes.filter((b) => b.checked).map((b) => b.value) };
  };

  const rows = users.map((u) => {
    const role = el("select", { "aria-label": "Role" }, ["user", "admin"].map((r) => el("option", { value: r, selected: u.role === r ? true : null }, r)));
    const libs = libraryPicker(u.libraries || []);
    const save = el("button", { class: "btn small", type: "button", onclick: async () => {
      try {
        await api("/api/users/" + encodeURIComponent(u.name), { method: "PATCH", body: { role: role.value, libraries: libs.value() } });
        toast("Saved " + u.name);
        if (u.name === state.me.name) { state.me = await api("/api/me"); render(); } else rerender();
      } catch (e) { toast(e.message); }
    } }, "Save");
    const password = el("button", { class: "btn small", type: "button", onclick: async () => {
      const pw = prompt(`New password for ${u.name} (8 or more characters). Their devices will be signed out.`);
      if (!pw) return;
      try { await api("/api/users/" + encodeURIComponent(u.name), { method: "PATCH", body: { password: pw } }); toast("Password changed"); rerender(); }
      catch (e) { toast(e.message); }
    } }, "Password…");
    const remove = el("button", { class: "btn small danger", type: "button", disabled: u.name === state.me.name, onclick: async () => {
      if (!confirm(`Remove ${u.name}? Their devices are signed out; their listening history is kept aside on the server.`)) return;
      try { await api("/api/users/" + encodeURIComponent(u.name), { method: "DELETE" }); toast("Removed " + u.name); rerender(); }
      catch (e) { toast(e.message); }
    } }, "Remove");
    return el("tr", {},
      el("td", {}, el("strong", {}, u.name), el("div", { class: "small muted" }, `${u.devices} device${u.devices === 1 ? "" : "s"}`)),
      el("td", {}, role),
      el("td", {}, u.role === "admin" ? el("span", { class: "muted small" }, "Admins see everything") : libs.node),
      el("td", {}, el("div", { class: "row" }, save, password, remove)));
  });

  const name = el("input", { type: "text", required: true, autocomplete: "off", pattern: "[A-Za-z0-9._@\\-]{1,64}" });
  const pass = el("input", { type: "password", required: true, minlength: 8, autocomplete: "new-password" });
  const newRole = el("select", {}, el("option", { value: "user" }, "user"), el("option", { value: "admin" }, "admin"));
  const newLibs = libraryPicker();
  const add = el("form", { class: "form", onsubmit: async (ev) => {
    ev.preventDefault();
    try {
      await api("/api/users", { method: "POST", body: { username: name.value, password: pass.value, role: newRole.value, libraries: newLibs.value() } });
      toast("Added " + name.value);
      rerender();
    } catch (e) { toast(e.message); }
  } },
  el("label", {}, el("span", {}, "User name"), name),
  el("label", {}, el("span", {}, "Password"), pass),
  el("label", {}, el("span", {}, "Role"), newRole),
  el("div", { class: "wide" }, el("span", { class: "small muted" }, "Libraries (none ticked: all)"), newLibs.node),
  el("div", { class: "wide" }, el("button", { class: "btn primary", type: "submit" }, "Add user")));

  return [
    el("section", { class: "panel" }, el("h2", {}, "Users"),
      el("div", { class: "scroll" }, el("table", { class: "table" },
        el("thead", {}, el("tr", {}, el("th", {}, "User"), el("th", {}, "Role"), el("th", {}, "Libraries"), el("th", {}))),
        el("tbody", {}, rows))),
      el("p", { class: "small muted" }, "A user limited to some libraries sees nothing else on any device. Untick every library to give access to all of them.")),
    el("section", { class: "panel" }, el("h2", {}, "Add a user"), add),
  ];
}

// ------------------------------------------------------------------ start

async function start() {
  state.me = await api("/api/me");
  const hash = location.hash.slice(1);
  if (VIEWS.some(([id, , admin]) => id === hash && (!admin || isAdmin()))) state.view = hash;
  render();
}

(async () => {
  try { state.server = await api("/api/health"); } catch { /* shown as "Kithara server" */ }
  document.title = state.server?.name ? `${state.server.name} · Kithara` : "Kithara server";
  state.token = loadToken();
  if (!state.token) return renderSignIn();
  try { await start(); } catch (e) { if (e.status !== 401) renderSignIn(e.message); }
})();
