// The kithara-server web page. Plain modules, no build step, no dependencies. It signs
// in like an app and uses the same API; every bit of text from the server is set with
// textContent, never parsed as HTML. It follows the apps' look: the lyre mark, chips,
// the purple-to-gold progress bar, the Listening tiles.

const app = document.getElementById("app");
const dialog = document.getElementById("book");
const TOKEN_KEY = "kithara.token";
const PREFS_KEY = "kithara.web";

const state = {
  token: null,
  me: null,
  server: null,
  libraries: [],
  books: [],
  progress: new Map(),
  query: "",
  view: "library",
  prefs: { layout: "list", sort: "recent", filter: "all", library: "" },
};

// ------------------------------------------------------------------ helpers

function el(tag, attrs = {}, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") n.className = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else if (k === "value") n.value = v;
    else if (k === "checked") n.checked = !!v;
    else if (k === "style") n.style.cssText = v; // through the CSSOM: the CSP forbids style attributes
    else n.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat(Infinity)) {
    if (c === undefined || c === null || c === false) continue;
    n.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return n;
}

// Material icons, as paths, drawn into inline SVG.
const ICONS = {
  library: "M4 6H2v14c0 1.1.9 2 2 2h14v-2H4V6zm16-4H8c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h12c1.1 0 2-.9 2-2V4c0-1.1-.9-2-2-2zm-1 9H9V9h10v2zm-4 4H9v-2h6v2zm4-8H9V5h10v2z",
  stats: "M3.5 18.49l6-6.01 4 4L22 6.92l-1.41-1.41-7.09 7.970-4-4L2 16.99z",
  devices: "M4 6h18V4H4c-1.1 0-2 .9-2 2v11H0v3h14v-3H4V6zm19 2h-6c-.55 0-1 .45-1 1v10c0 .55.45 1 1 1h6c.55 0 1-.45 1-1V9c0-.55-.45-1-1-1zm-1 9h-4v-7h4v7z",
  users: "M16 11c1.66 0 2.99-1.34 2.99-3S17.66 5 16 5c-1.66 0-3 1.34-3 3s1.34 3 3 3zm-8 0c1.66 0 2.99-1.34 2.99-3S9.66 5 8 5C6.34 5 5 6.34 5 8s1.34 3 3 3zm0 2c-2.33 0-7 1.17-7 3.5V19h14v-2.5c0-2.33-4.67-3.5-7-3.5zm8 0c-.29 0-.62.02-.97.05 1.16.84 1.97 1.97 1.97 3.45V19h6v-2.5c0-2.33-4.67-3.5-7-3.5z",
  search: "M15.5 14h-.79l-.28-.27C15.41 12.59 16 11.11 16 9.5 16 5.91 13.09 3 9.5 3S3 5.91 3 9.5 5.91 16 9.5 16c1.61 0 3.09-.59 4.230-1.57l.27.28v.79l5 4.99L20.49 19l-4.99-5zm-6 0C7.01 14 5 11.99 5 9.5S7.01 5 9.5 5 14 7.01 14 9.5 11.99 14 9.5 14z",
  check: "M9 16.17L4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41z",
  sort: "M16 17.01V10h-2v7.01h-3L15 21l4-3.99h-3zM9 3L5 6.99h3V14h2V6.99h3L9 3z",
  grid: "M3 3v8h8V3H3zm6 6H5V5h4v4zm-6 4v8h8v-8H3zm6 6H5v-4h4v4zm4-16v8h8V3h-8zm6 6h-4V5h4v4zm-6 4v8h8v-8h-8zm6 6h-4v-4h4v4z",
  list: "M3 13h2v-2H3v2zm0 4h2v-2H3v2zm0-8h2V7H3v2zm4 4h14v-2H7v2zm0 4h14v-2H7v2zM7 7v2h14V7H7z",
  down: "M16.59 8.59L12 13.17 7.41 8.59 6 10l6 6 6-6z",
  back: "M20 11H7.830l5.59-5.59L12 4l-8 8 8 8 1.41-1.41L7.83 13H20v-2z",
  download: "M19 9h-4V3H9v6H5l7 7 7-7zM5 18v2h14v-2H5z",
  refresh: "M17.65 6.35C16.2 4.9 14.21 4 12 4c-4.42 0-7.99 3.58-7.99 8s3.57 8 7.99 8c3.73 0 6.84-2.55 7.73-6h-2.08c-.82 2.33-3.04 4-5.65 4-3.31 0-6-2.69-6-6s2.69-6 6-6c1.66 0 3.14.69 4.22 1.78L13 11h7V4l-2.35 2.35z",
  logout: "M17 7l-1.41 1.41L18.17 11H8v2h10.17l-2.58 2.58L17 17l5-5zM4 5h8V3H4c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h8v-2H4V5z",
  edit: "M3 17.25V21h3.75L17.81 9.94l-3.75-3.75L3 17.25zM20.71 7.04a1 1 0 000-1.41l-2.34-2.34a1 1 0 00-1.41 0l-1.83 1.83 3.75 3.75 1.83-1.83z",
  magic: "M7.5 5.6L10 7 8.6 4.5 10 2 7.5 3.4 5 2l1.4 2.5L5 7zm12 9.8L17 14l1.4 2.5L17 19l2.5-1.4L22 19l-1.4-2.5L22 14zM22 2l-2.5 1.4L17 2l1.4 2.5L17 7l2.5-1.4L22 7l-1.4-2.5zm-7.63 5.29a.996.996 0 00-1.41 0L1.29 18.96a.996.996 0 000 1.41l2.34 2.34c.39.39 1.02.39 1.41 0L16.7 11.05a.996.996 0 000-1.41l-2.33-2.35zm-1.03 5.49l-2.12-2.12 2.44-2.44 2.12 2.12-2.44 2.44z",
  chapters: "M3 13h2v-2H3v2zm0 4h2v-2H3v2zm0-8h2V7H3v2zm4 4h14v-2H7v2zm0 4h14v-2H7v2zM7 7v2h14V7H7z",
  files: "M14 2H6c-1.1 0-1.99.9-1.99 2L4 20c0 1.1.89 2 1.99 2H18c1.1 0 2-.9 2-2V8l-6-6zm2 16H8v-2h8v2zm0-4H8v-2h8v2zm-3-5V3.5L18.5 9H13z",
  phone: "M17 1.01L7 1c-1.1 0-2 .9-2 2v18c0 1.1.9 2 2 2h10c1.1 0 2-.9 2-2V3c0-1.1-.9-1.99-2-1.99zM17 19H7V5h10v14z",
  tablet: "M21 4H3c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h18c1.1 0 1.99-.9 1.99-2L23 6c0-1.1-.9-2-2-2zm-2 14H5V6h14v12z",
  computer: "M20 18c1.1 0 1.99-.9 1.99-2L22 6c0-1.1-.9-2-2-2H4c-1.1 0-2 .9-2 2v10c0 1.1.9 2 2 2H0v2h24v-2h-4zM4 6h16v10H4V6z",
  web: "M11.99 2C6.47 2 2 6.48 2 12s4.47 10 9.99 10C17.52 22 22 17.52 22 12S17.52 2 11.99 2zm6.93 6h-2.95a15.650 15.650 0 00-1.38-3.56A8.03 8.03 0 0118.92 8zM12 4.04c.83 1.2 1.48 2.53 1.91 3.96h-3.82c.43-1.43 1.08-2.76 1.91-3.96zM4.26 14C4.1 13.36 4 12.69 4 12s.1-1.36.26-2h3.38c-.08.66-.14 1.32-.14 2s.06 1.34.14 2H4.26zm.82 2h2.95c.32 1.25.78 2.45 1.38 3.56A7.987 7.987 0 015.08 16zm2.95-8H5.08a7.987 7.987 0 014.33-3.56A15.65 15.65 0 008.03 8zM12 19.96c-.83-1.2-1.48-2.53-1.91-3.96h3.82c-.43 1.43-1.08 2.76-1.91 3.96zM14.34 14H9.66c-.09-.66-.16-1.32-.16-2s.07-1.35.16-2h4.68c.09.65.16 1.32.16 2s-.07 1.34-.16 2zm.25 5.56c.6-1.11 1.06-2.31 1.38-3.56h2.95a8.03 8.03 0 01-4.33 3.56zM16.36 14c.08-.66.14-1.32.14-2s-.06-1.34-.14-2h3.38c.16.64.26 1.31.26 2s-.1 1.36-.26 2h-3.38z",
};

function icon(name, cls = "") {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("class", ("i " + cls).trim());
  svg.setAttribute("aria-hidden", "true");
  const p = document.createElementNS(ns, "path");
  p.setAttribute("d", ICONS[name]);
  svg.append(p);
  return svg;
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

function loadPrefs() {
  try { Object.assign(state.prefs, JSON.parse(storage()?.getItem(PREFS_KEY) || "{}")); } catch { /* defaults */ }
}

function savePrefs() {
  try { storage()?.setItem(PREFS_KEY, JSON.stringify(state.prefs)); } catch { /* not kept */ }
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
    renderSignIn("You were signed out. Sign in again.");
    throw new ApiError(401, "signed out");
  }
  if (raw) return res;
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) throw new ApiError(res.status, data?.error || res.statusText);
  return data;
}

const fmtDuration = (ms) => {
  const m = Math.round((ms || 0) / 60000);
  const h = Math.floor(m / 60);
  return h ? `${h}h ${m % 60}m` : `${m}m`;
};
const fmtClock = (ms) => {
  const s = Math.floor((ms || 0) / 1000);
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
  return h ? `${h}:${String(m).padStart(2, "0")}:${String(sec).padStart(2, "0")}` : `${m}:${String(sec).padStart(2, "0")}`;
};
const fmtDate = (ms) => (ms ? new Date(ms).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }) : "");
const fmtBytes = (n) => (n >= 1e9 ? (n / 1e9).toFixed(1) + " GB" : n >= 1e6 ? (n / 1e6).toFixed(0) + " MB" : Math.max(1, Math.round(n / 1e3)) + " KB");
const startOfDay = (ms) => { const d = new Date(ms); d.setHours(0, 0, 0, 0); return d.getTime(); };
const relative = (ms) => {
  if (!ms) return "never";
  const days = Math.round((startOfDay(Date.now()) - startOfDay(ms)) / 86400000);
  if (days <= 0) return "today";
  if (days === 1) return "yesterday";
  if (days < 30) return `${days} days ago`;
  return fmtDate(ms);
};
const plural = (n, one, many = one + "s") => `${n} ${n === 1 ? one : many}`;
const isAdmin = () => state.me?.role === "admin";

// ------------------------------------------------------------------ covers

// Covers need the bearer token, which an <img> cannot send: fetch them, show blobs,
// and only when they scroll into view.
const coverCache = new Map();
const coverObserver = new IntersectionObserver((entries) => {
  for (const e of entries) {
    if (!e.isIntersecting) continue;
    coverObserver.unobserve(e.target);
    fillCover(e.target);
  }
}, { rootMargin: "400px" });

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
    box.replaceWith(el("img", { class: box.dataset.cls, src: blobURL, alt: "" }));
  } catch { /* keep the typeset cover */ }
}

// The typeset stand-in, in the manner of the apps' sample covers: a colour pair picked
// from the title, the title set in a serif, a ring in the corner.
const MADE = [
  ["#123b3a", "#f1e6c8", "#d4a64a"], ["#f3efe6", "#1d1b20", "#d4543a"], ["#5c6b3a", "#f5edd6", "#e2c56a"],
  ["#0e0e12", "#f4f4f4", "#4ade80"], ["#162447", "#f1e6c8", "#d4a64a"], ["#eef1f5", "#1f2d4d", "#3b5b8c"],
  ["#3b1f4f", "#f3e8ff", "#c9a24d"], ["#5a1f1f", "#fbe9e0", "#e6b85c"],
];

function madeCover(book, cls) {
  let h = 0;
  for (const ch of book.title || "") h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  const [bg, ink, accent] = MADE[h % MADE.length];
  const author = book.author || (book.authors || []).join(", ");
  return el("div", { class: cls + " made", "aria-hidden": "true", style: `--made-bg:${bg};--made-ink:${ink};--made-accent:${accent}` },
    el("span", { class: "t" }, book.title), el("span", { class: "a" }, author));
}

function cover(book, extra = "") {
  const cls = ("cover " + extra).trim();
  const box = madeCover(book, cls);
  if (book.coverUrl) {
    // updatedAt in the address, so a changed cover is fetched again.
    box.dataset.src = book.coverUrl + "?v=" + book.updatedAt;
    box.dataset.cls = cls;
    coverObserver.observe(box);
  }
  return box;
}

// ------------------------------------------------------------------ sign in

function renderSignIn(message = "") {
  dialog.open && dialog.close();
  const err = el("p", { class: "error small", role: "alert" }, message);
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
    el("label", {}, "User name", user),
    el("label", {}, "Password", pass),
    el("label", { class: "check" }, remember, "Keep me signed in on this browser"),
    err,
    submit,
  );
  app.replaceChildren(el("div", { class: "signin" },
    el("img", { class: "mark", src: "logo.svg", alt: "" }),
    el("h1", {}, state.server?.name || "Kithara"),
    el("p", { class: "muted" }, "Sign in with your Kithara account. New accounts come from whoever runs this server."),
    form));
}

function browserName() {
  const ua = navigator.userAgent;
  const b = /Firefox\//.test(ua) ? "Firefox" : /Edg\//.test(ua) ? "Edge" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "Browser";
  const os = /Windows/.test(ua) ? "Windows" : /Mac OS X/.test(ua) ? "macOS" : /Android/.test(ua) ? "Android" : /iPhone|iPad/.test(ua) ? "iOS" : /Linux/.test(ua) ? "Linux" : "";
  return `Web (${b}${os ? " on " + os : ""})`;
}

// ------------------------------------------------------------------ shell

const VIEWS = [
  { id: "library", label: "Library", icon: "library" },
  { id: "stats", label: "Listening", icon: "stats" },
  { id: "devices", label: "Devices", icon: "devices" },
  { id: "users", label: "Users", icon: "users", admin: true },
];

function renderShell(current) {
  const tabs = el("nav", { class: "tabs", "aria-label": "Sections" },
    VIEWS.filter((v) => !v.admin || isAdmin()).map((v) =>
      el("button", { type: "button", "aria-current": state.view === v.id ? "page" : null, onclick: () => go(v.id) }, icon(v.icon), el("span", {}, v.label))));
  const top = el("header", { class: "topbar" },
    el("div", { class: "title" },
      el("img", { src: "logo.svg", alt: "" }),
      el("div", {}, el("h1", {}, current.label), el("div", { class: "server" }, state.server?.name || ""))),
    tabs,
    el("div", { class: "end" },
      el("span", { class: "who", title: state.me.name },
        el("span", { class: "avatar", "aria-hidden": "true" }, state.me.name.slice(0, 1)),
        el("span", { class: "name" }, state.me.name, isAdmin() ? el("span", { class: "badge", style: "margin-left:.4rem" }, "admin") : null)),
      el("button", { class: "icon-btn", type: "button", title: "Sign out", "aria-label": "Sign out", onclick: signOut }, icon("logout"))));
  const main = el("main", { id: "main" });
  app.replaceChildren(el("div", { class: "shell" }, top, main));
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
  const current = VIEWS.find((v) => v.id === state.view && (!v.admin || isAdmin())) || VIEWS[0];
  state.view = current.id;
  const main = renderShell(current);
  main.append(el("p", { class: "muted" }, "Loading…"));
  try {
    const view = { library: viewLibrary, stats: viewStats, devices: viewDevices, users: viewUsers }[current.id];
    main.replaceChildren(...[await view()].flat().filter(Boolean));
  } catch (e) {
    if (e.status !== 401) main.replaceChildren(el("p", { class: "error" }, e.message));
  }
}

async function refreshMain(view) {
  const main = document.getElementById("main");
  if (main) main.replaceChildren(...[await view()].flat().filter(Boolean));
}

// ------------------------------------------------------------------ library

async function loadLibrary() {
  const [cat, prog, libs] = await Promise.all([api("/api/library"), api("/api/progress"), api("/api/libraries")]);
  state.books = cat.books;
  state.libraries = libs.libraries;
  state.progress = new Map(prog.progress.map((p) => [p.bookId, p]));
  if (state.prefs.library && !state.libraries.some((l) => l.name === state.prefs.library)) state.prefs.library = "";
}

function progressOf(b) {
  const p = state.progress.get(b.id);
  if (!p) return null;
  return { ...p, fraction: b.durationMs ? Math.min(1, p.positionMs / b.durationMs) : 0 };
}

function statusOf(b) {
  const p = progressOf(b);
  if (p?.finished) return "finished";
  if (p && p.positionMs > 0) return "progress";
  return "new";
}

function matches(b, q) {
  if (!q) return true;
  const hay = [b.title, b.subtitle, b.author, b.narrator, b.series, ...(b.genres || [])].join(" ").toLowerCase();
  return q.toLowerCase().split(/\s+/).every((w) => hay.includes(w));
}

const byTitle = (a, b) => a.title.localeCompare(b.title, undefined, { numeric: true, sensitivity: "base" });
const SORTS = {
  recent: ["Recent", (a, b) => (state.progress.get(b.id)?.updatedAt || 0) - (state.progress.get(a.id)?.updatedAt || 0) || byTitle(a, b)],
  title: ["Title", byTitle],
  author: ["Author", (a, b) => (a.author || "~").localeCompare(b.author || "~") || byTitle(a, b)],
  series: ["Series", (a, b) => (a.series || "~").localeCompare(b.series || "~") || (a.seriesIndex ?? 1e9) - (b.seriesIndex ?? 1e9) || byTitle(a, b)],
  updated: ["Updated", (a, b) => b.updatedAt - a.updatedAt],
};

function metaLine(b) {
  const p = progressOf(b);
  const s = statusOf(b);
  if (s === "finished") return el("span", { class: "meta done" }, "Finished");
  if (s === "progress") return el("span", { class: "meta" }, `${Math.round(p.fraction * 100)}% · ${fmtDuration(b.durationMs - p.positionMs)} left`);
  return el("span", { class: "meta" }, `Not started · ${fmtDuration(b.durationMs)}`);
}

function progressBar(b) {
  const p = progressOf(b);
  if (!p || p.finished || !p.positionMs) return null;
  return el("div", { class: "bar", role: "progressbar", "aria-valuenow": Math.round(p.fraction * 100), "aria-valuemin": 0, "aria-valuemax": 100 },
    el("i", { style: `width:${Math.max(2, p.fraction * 100).toFixed(1)}%` }));
}

function chip(label, on, onclick) {
  return el("button", { class: "chip", type: "button", "aria-pressed": on ? "true" : "false", onclick }, on ? icon("check") : null, label);
}

async function viewLibrary() {
  await loadLibrary();
  const prefs = state.prefs;
  const shelf = el("div");
  const count = el("span");

  const fill = () => {
    const shown = state.books
      .filter((b) => !prefs.library || b.library === prefs.library)
      .filter((b) => prefs.filter === "all" || statusOf(b) === prefs.filter)
      .filter((b) => matches(b, state.query))
      .sort((SORTS[prefs.sort] || SORTS.recent)[1]);
    count.textContent = plural(shown.length, "title");
    shelf.className = prefs.layout === "grid" ? "grid-view" : "list-view";
    if (!shown.length) {
      shelf.className = "";
      shelf.replaceChildren(el("div", { class: "empty" }, state.books.length ? "Nothing here matches." : "No books yet. Add audiobooks to the library folder and rescan."));
      return;
    }
    shelf.replaceChildren(...shown.map(prefs.layout === "grid" ? bookCard : bookItem));
  };

  const filters = el("div", { class: "chips", role: "group", "aria-label": "Show" });
  const libChips = el("div", { class: "chips", role: "group", "aria-label": "Library" });
  const drawChips = () => {
    filters.replaceChildren(...[["all", "All"], ["progress", "In progress"], ["new", "Not started"], ["finished", "Finished"]].map(([id, label]) =>
      chip(label, prefs.filter === id, () => { prefs.filter = id; savePrefs(); drawChips(); fill(); })));
    libChips.replaceChildren(
      chip("All libraries", !prefs.library, () => { prefs.library = ""; savePrefs(); drawChips(); fill(); }),
      ...state.libraries.map((l) => chip(`${l.name} · ${l.books}`, prefs.library === l.name, () => { prefs.library = l.name; savePrefs(); drawChips(); fill(); })));
  };
  drawChips();

  const sortLabel = el("span", {}, (SORTS[prefs.sort] || SORTS.recent)[0]);
  const sortBtn = el("button", { class: "sort", type: "button", title: "Change the order", onclick: () => {
    const keys = Object.keys(SORTS);
    prefs.sort = keys[(keys.indexOf(prefs.sort) + 1) % keys.length];
    sortLabel.textContent = SORTS[prefs.sort][0];
    savePrefs(); fill();
  } }, icon("sort"), sortLabel);
  const layoutBtns = [["list", "List"], ["grid", "Grid"]].map(([id, label]) => el("button", {
    class: "icon-btn", type: "button", title: label, "aria-label": label, "aria-pressed": prefs.layout === id ? "true" : "false",
    onclick: () => { prefs.layout = id; savePrefs(); layoutBtns.forEach((b, i) => b.setAttribute("aria-pressed", i === (id === "list" ? 0 : 1) ? "true" : "false")); fill(); },
  }, icon(id)));

  const end = el("div", { class: "end" }, sortBtn, layoutBtns);
  if (isAdmin()) {
    const rescan = el("button", { class: "icon-btn", type: "button", title: "Rescan the library folders", "aria-label": "Rescan", onclick: async () => {
      rescan.disabled = true;
      try { const r = await api("/api/rescan", { method: "POST" }); toast(`Rescanned: ${plural(r.books, "book")}`); render(); }
      catch (e) { toast(e.message); rescan.disabled = false; }
    } }, icon("refresh"));
    end.append(rescan);
  }

  const search = el("div", { class: "search" }, icon("search"),
    el("input", { type: "search", placeholder: "Search titles, authors, narrators, series", "aria-label": "Search", value: state.query,
      oninput: (e) => { state.query = e.target.value; fill(); } }));

  fill();
  return [
    el("div", { class: "controls" }, search, filters, state.libraries.length > 1 ? libChips : null,
      el("div", { class: "countbar" }, count, end)),
    shelf,
  ];
}

function bookItem(b) {
  return el("button", { class: "item", type: "button", onclick: () => openBook(b.id) },
    cover(b),
    el("div", { class: "body" },
      el("span", { class: "t" }, b.title),
      el("span", { class: "by" }, b.author || "Unknown author"),
      progressBar(b),
      metaLine(b)));
}

function bookCard(b) {
  return el("button", { class: "card", type: "button", onclick: () => openBook(b.id) },
    cover(b),
    progressBar(b) || el("div", { class: "bar", style: "visibility:hidden" }),
    el("span", { class: "t" }, b.title),
    el("span", { class: "by" }, b.author || "Unknown author"),
    metaLine(b));
}

// ------------------------------------------------------------------ one book

function openBook(id, mode = "view") {
  const b = state.books.find((x) => x.id === id);
  if (!b) return;
  const parts = mode === "edit" ? bookEditor(b) : mode === "match" ? bookMatcher(b) : bookDetail(b);
  const back = mode === "view"
    ? el("button", { class: "icon-btn", type: "button", "aria-label": "Close", title: "Close", onclick: () => dialog.close() }, icon("down"))
    : el("button", { class: "icon-btn", type: "button", "aria-label": "Back", title: "Back", onclick: () => openBook(b.id) }, icon("back"));
  dialog.replaceChildren(el("div", { class: "sheet" }, el("div", { class: "sheet-top" }, back), ...parts.filter(Boolean)));
  if (!dialog.open) dialog.showModal();
  dialog.scrollTop = 0;
}

dialog.addEventListener("click", (e) => { if (e.target === dialog) dialog.close(); });

function currentChapter(b, p) {
  if (!p || p.finished || !b.chapters.length) return -1;
  return b.chapters.findIndex((c) => p.positionMs >= c.startMs && p.positionMs < c.endMs);
}

function bookDetail(b) {
  const p = progressOf(b);
  const byline = [b.author, b.narrator ? "read by " + b.narrator : ""].filter(Boolean).join(" · ");
  const tags = [
    b.series ? `${b.series}${b.seriesIndex != null ? ", book " + b.seriesIndex : ""}` : "",
    fmtDuration(b.durationMs), b.publishedYear, ...(b.genres || []).slice(0, 4),
    state.libraries.length > 1 ? b.library : "",
  ].filter(Boolean);

  const size = b.files.reduce((n, f) => n + (f.sizeBytes || 0), 0);
  const download = el("button", { class: "btn primary", type: "button", onclick: async () => {
    download.disabled = true;
    try {
      const link = await api(`/api/books/${encodeURIComponent(b.id)}/download-link`, { method: "POST" });
      const a = el("a", { href: link.url, download: "" });
      document.body.append(a); a.click(); a.remove();
    } catch (e) { toast(e.message); }
    download.disabled = false;
  } }, icon("download"), `Download · ${fmtBytes(size)}`);
  const actions = el("div", { class: "row actions", style: "margin-top:1.1rem" }, download);
  if (isAdmin()) {
    actions.append(
      el("button", { class: "btn tonal", type: "button", onclick: () => openBook(b.id, "match") }, icon("magic"), "Find details"),
      el("button", { class: "btn tonal", type: "button", onclick: () => openBook(b.id, "edit") }, icon("edit"), "Edit"));
  }

  let progress = null;
  const cur = currentChapter(b, p);
  if (p && (p.positionMs > 0 || p.finished)) {
    progress = el("div", { class: "panel-card progress-card" },
      cur >= 0 ? el("div", {}, el("div", {}, b.chapters[cur].title), el("div", { class: "small muted" }, `${cur + 1} of ${b.chapters.length}`)) : null,
      el("div", { class: "bar" }, el("i", { style: `width:${Math.max(2, p.fraction * 100).toFixed(1)}%` })),
      el("div", { class: "line" },
        el("span", {}, p.finished ? "Finished" : fmtClock(p.positionMs)),
        el("span", {}, p.finished ? (p.finishedAt ? fmtDate(p.finishedAt) : "") : `${fmtDuration(b.durationMs - p.positionMs)} left in book`),
        el("span", {}, p.startedAt ? "Started " + fmtDate(p.startedAt) : "")));
  }

  const facts = el("dl", { class: "facts" });
  const fact = (k, v) => v && facts.append(el("dt", {}, k), el("dd", {}, v));
  fact("Publisher", b.publisher);
  fact("Language", b.language);
  fact("ISBN", b.isbn);
  fact("ASIN", b.asin);

  return [
    el("div", { class: "hero" }, cover(b), el("div", {},
      el("h2", {}, b.title),
      b.subtitle ? el("div", { class: "sub" }, b.subtitle) : null,
      el("div", { class: "who-line muted" }, byline || "Unknown author"),
      el("div", { class: "pill-row" }, tags.map((t) => el("span", { class: "tag" }, t))),
      actions)),
    progress,
    b.description ? el("p", { class: "desc" }, b.description) : null,
    facts.childElementCount ? el("div", { class: "panel-card" }, facts) : null,
    b.chapters.length ? el("details", { class: "panel-card" },
      el("summary", {}, icon("chapters"), plural(b.chapters.length, "chapter"), el("span", { class: "muted small", style: "margin-left:auto" }, cur >= 0 ? `you're on ${cur + 1}` : "")),
      el("div", { class: "rows" }, b.chapters.map((c, i) => el("div", { class: i === cur ? "accent" : "" },
        el("span", { class: "n" }, i + 1), el("span", { class: "grow" }, c.title), el("span", { class: "end" }, fmtClock(c.startMs)))))) : null,
    el("details", { class: "panel-card" },
      el("summary", {}, icon("files"), plural(b.files.length, "file"), el("span", { class: "muted small", style: "margin-left:auto" }, fmtBytes(size))),
      el("div", { class: "rows" }, b.files.map((f, i) => el("div", {},
        el("span", { class: "n" }, i + 1), el("span", { class: "grow" }, f.name), el("span", { class: "end" }, fmtDuration(f.durationMs), " · ", fmtBytes(f.sizeBytes)))))),
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
  const desc = el("textarea", { value: b.description || "" });
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
  FIELDS.map(([k, label]) => el("label", {}, label, inputs[k] = el("input", { type: k === "seriesIndex" ? "number" : "text", step: "any", value: initial[k] ?? "" }))),
  el("label", { class: "wide" }, "Description", desc),
  el("p", { class: "wide note" }, "Saved details are kept in the server's data folder and take priority over the files. Reset brings back what the library says."),
  el("div", { class: "row wide" },
    el("button", { class: "btn primary", type: "submit" }, "Save"),
    el("button", { class: "btn", type: "button", onclick: () => openBook(b.id) }, "Cancel"),
    el("button", { class: "btn danger", type: "button", style: "margin-left:auto", onclick: () => resetMetadata(b) }, "Reset to library")));
  return [el("h2", {}, "Edit details"), form];
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
  if (!confirm(`Forget the saved details and cover for “${b.title}”?`)) return;
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
  if (state.view === "library") refreshMain(viewLibrary);
}

function bookMatcher(b) {
  const providers = [["audible", "Audible"], ["openlibrary", "Open Library"], ["google", "Google Books"]];
  let provider = "audible";
  const region = el("select", { "aria-label": "Audible region", onchange: () => search() },
    ["us", "uk", "ca", "au", "de", "fr", "it", "es", "in", "jp"].map((r) => el("option", { value: r }, r.toUpperCase())));
  const providerChips = el("div", { class: "segmented", role: "group", "aria-label": "Provider" });
  const drawProviders = () => providerChips.replaceChildren(...providers.map(([id, label]) =>
    chip(label, provider === id, () => { provider = id; region.hidden = id !== "audible"; drawProviders(); search(); })));
  drawProviders();
  const title = el("input", { type: "text", value: b.title, "aria-label": "Title" });
  const author = el("input", { type: "text", value: (b.author || "").split(",")[0], "aria-label": "Author" });
  const results = el("div", { class: "results" });
  async function search(ev) {
    ev?.preventDefault();
    results.replaceChildren(el("p", { class: "muted" }, "Searching…"));
    const q = new URLSearchParams({ provider, title: title.value, author: author.value });
    if (provider === "audible") q.set("region", region.value);
    try {
      const out = await api(`/api/books/${encodeURIComponent(b.id)}/match?` + q);
      results.replaceChildren(...(out.results.length ? out.results.map((c) => matchResult(b, c)) : [el("p", { class: "muted" }, "Nothing found. Try a shorter title or another provider.")]));
    } catch (e) {
      results.replaceChildren(el("p", { class: "error" }, e.message));
    }
  }
  const form = el("form", { class: "row", onsubmit: search },
    el("div", { class: "grow" }, title), el("div", { class: "grow" }, author),
    el("button", { class: "btn primary", type: "submit" }, icon("search"), "Search"));
  queueMicrotask(search);
  return [
    el("div", {}, el("h2", {}, "Find details"), el("p", { class: "muted small", style: "margin:0" },
      "The server asks the provider only when you search. The result you pick, and its cover, replace what the library says until you reset it.")),
    el("div", { class: "row" }, providerChips, region),
    form,
    results,
  ];
}

function matchResult(b, c) {
  const series = c.series ? ` · ${c.series}${c.seriesIndex != null ? " #" + c.seriesIndex : ""}` : "";
  const keepCover = el("input", { type: "checkbox", checked: !!c.coverUrl, disabled: !c.coverUrl });
  return el("div", { class: "result" },
    c.coverUrl ? el("img", { src: c.coverUrl, alt: "", loading: "lazy", referrerpolicy: "no-referrer" }) : madeCover(c, "cover"),
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

// ------------------------------------------------------------------ listening

async function viewStats() {
  const tz = -new Date().getTimezoneOffset();
  const [st] = await Promise.all([api("/api/stats?tzOffset=" + tz), state.books.length ? null : loadLibrary()]);
  const byId = new Map(state.books.map((b) => [b.id, b]));
  const tile = (k, v) => el("div", { class: "tile" }, el("div", { class: "k" }, k), el("div", { class: "v" }, v));

  // The last 30 days, today included and in gold, gaps as empty bars.
  const days = new Map(st.days.map((d) => [d.date, d.listenedMs]));
  const series = [];
  for (let i = 29; i >= 0; i--) {
    const d = new Date(Date.now() - i * 86400000 + tz * 60000).toISOString().slice(0, 10);
    series.push([d, days.get(d) || 0, i === 0]);
  }
  const peak = Math.max(1, ...series.map(([, ms]) => ms));
  const month = series.reduce((n, [, ms]) => n + ms, 0);
  const activeDays = series.filter(([, ms]) => ms > 0).length;
  const chart = el("div", { class: "chart", role: "img", "aria-label": "Listening per day, last 30 days" },
    series.map(([d, ms, today]) => el("div", { class: today ? "today" : ms ? "" : "zero", style: `height:${Math.max(2, (ms / peak) * 100)}%`, title: `${d}: ${fmtDuration(ms)}` })));

  const avg = st.daysListened ? st.totalListenedMs / st.daysListened : 0;
  return [
    el("div", { class: "big" },
      el("div", {}, "Last 30 days"),
      el("div", { class: "v" }, fmtDuration(month)),
      el("div", { class: "k" }, `${plural(activeDays, "day")} with listening · ${fmtDuration(st.totalListenedMs)} all time`)),
    el("div", { class: "tiles" },
      tile("Streak", plural(st.currentStreakDays, "day")),
      tile("Longest streak", plural(st.longestStreakDays, "day")),
      tile("Average per listening day", fmtDuration(avg)),
      tile("Days listened", st.daysListened),
      tile("Books finished", plural(st.booksFinished, "book")),
      tile("Books started", plural(st.booksStarted, "book"))),
    el("section", {},
      el("div", { class: "section-head" }, el("h2", {}, "By day")),
      chart,
      el("div", { class: "chart-axis" }, el("span", {}, fmtDate(Date.parse(series[0][0]))), el("span", {}, "today"))),
    el("section", {},
      el("div", { class: "section-head" }, el("h2", {}, "Recently listened")),
      st.books.length ? el("div", {}, st.books.slice(0, 12).map((r) => {
        const b = byId.get(r.bookId);
        return el("div", { class: "book-row" },
          b ? cover(b) : el("div", { class: "cover" }),
          el("div", { style: "min-width:0" },
            el("div", { class: "t" }, b?.title || "A book no longer in your library"),
            el("div", { class: "small muted" }, r.finishedAt ? `Finished ${fmtDate(r.finishedAt)}` : `Last listened ${relative(r.lastListenedAt)}`)),
          el("div", { class: "meta" }, fmtDuration(r.listenedMs)));
      })) : el("p", { class: "muted" }, "Nothing yet. Listening time appears here as the apps sync."),
      el("p", { class: "note" }, "Worked out from the apps' progress syncs, so these figures are an estimate and lean low.")),
  ];
}

// ------------------------------------------------------------------ devices

function deviceIcon(name = "") {
  const n = name.toLowerCase();
  if (n.startsWith("web")) return "web";
  if (/ipad|tab|fire/.test(n)) return "tablet";
  if (/mac|book|pc|desktop|laptop|windows|linux/.test(n)) return "computer";
  return "phone";
}

async function viewDevices(who = state.me.name) {
  const q = who === state.me.name ? "" : "?user=" + encodeURIComponent(who);
  const { devices } = await api("/api/devices" + q);
  const out = [];
  if (isAdmin()) {
    const { users } = await api("/api/users");
    if (users.length > 1) {
      out.push(el("div", { class: "chips", style: "margin-bottom:1rem", role: "group", "aria-label": "Whose devices" },
        users.map((u) => chip(u.name === state.me.name ? "You" : u.name, u.name === who, () => refreshMain(() => viewDevices(u.name))))));
    }
  }
  const revoke = async (d) => {
    if (!confirm(d.current ? "Sign out this browser?" : `Sign out “${d.name || "this device"}”? It will ask for the password again.`)) return;
    try {
      await api(`/api/devices/${encodeURIComponent(d.id)}${q}`, { method: "DELETE" });
      if (d.current) { forgetToken(); renderSignIn(); return; }
      toast("Signed out");
      refreshMain(() => viewDevices(who));
    } catch (e) { toast(e.message); }
  };
  out.push(devices.length ? el("div", { class: "cards" }, devices.map((d) => el("div", { class: "dev" },
    el("div", { class: "ico" }, icon(deviceIcon(d.name))),
    el("div", {},
      el("div", { class: "name" }, d.name || "Unnamed device", d.current ? el("span", { class: "badge" }, "This browser") : null),
      el("div", { class: "small muted" }, `Signed in ${fmtDate(d.createdAt)} · last seen ${relative(d.lastSeenAt)}`)),
    el("button", { class: "btn small danger", type: "button", onclick: () => revoke(d) }, "Sign out")))) : el("p", { class: "empty" }, "No devices signed in."));
  out.push(el("p", { class: "note" }, "Signing a device out makes it ask for the password again. Changing a password signs out every device."));
  return out;
}

// ------------------------------------------------------------------ users (admin)

async function viewUsers() {
  const [{ users }, { libraries }] = await Promise.all([api("/api/users"), api("/api/libraries")]);
  const rerender = () => refreshMain(viewUsers);

  // Library access as chips; none chosen means every library.
  const libraryPicker = (selected = []) => {
    const chosen = new Set(selected.map((s) => s.toLowerCase()));
    const node = el("div", { class: "chips", role: "group", "aria-label": "Libraries" });
    const draw = () => node.replaceChildren(
      chip("All libraries", !chosen.size, () => { chosen.clear(); draw(); }),
      ...libraries.map((l) => {
        const key = l.name.toLowerCase();
        return chip(l.name, chosen.has(key), () => { chosen.has(key) ? chosen.delete(key) : chosen.add(key); draw(); });
      }));
    draw();
    return { node, value: () => libraries.filter((l) => chosen.has(l.name.toLowerCase())).map((l) => l.name) };
  };
  const rolePicker = (role, onchange) => {
    let value = role;
    const node = el("div", { class: "segmented", role: "group", "aria-label": "Role" });
    const draw = () => node.replaceChildren(...[["user", "Listener"], ["admin", "Admin"]].map(([id, label]) =>
      chip(label, value === id, () => { value = id; draw(); onchange?.(value); })));
    draw();
    return { node, value: () => value };
  };
  const caption = (text) => el("div", { class: "small muted", style: "margin-bottom:.35rem" }, text);

  const cards = users.map((u) => {
    const libs = libraryPicker(u.libraries || []);
    const libsRow = el("div", { hidden: u.role === "admin" || libraries.length < 2 ? true : null }, caption("Can see"), libs.node);
    const role = rolePicker(u.role, (v) => { libsRow.hidden = v === "admin" || libraries.length < 2; });
    const save = el("button", { class: "btn small primary", type: "button", onclick: async () => {
      try {
        await api("/api/users/" + encodeURIComponent(u.name), { method: "PATCH", body: { role: role.value(), libraries: libs.value() } });
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
    const remove = u.name === state.me.name ? null : el("button", { class: "btn small danger", type: "button", onclick: async () => {
      if (!confirm(`Remove ${u.name}? Their devices are signed out; their listening history is kept aside on the server.`)) return;
      try { await api("/api/users/" + encodeURIComponent(u.name), { method: "DELETE" }); toast("Removed " + u.name); rerender(); }
      catch (e) { toast(e.message); }
    } }, "Remove");
    const access = u.role === "admin" ? "Admin · sees everything" : u.libraries?.length ? "Sees " + u.libraries.join(", ") : "Sees every library";
    return el("div", { class: "user" },
      el("div", { class: "avatar", "aria-hidden": "true" }, u.name.slice(0, 1)),
      el("div", {},
        el("div", { class: "name" }, u.name, u.name === state.me.name ? el("span", { class: "badge plain" }, "You") : null),
        el("div", { class: "small muted" }, `${access} · ${plural(u.devices, "device")}`)),
      el("div", { class: "row actions" }, password, remove),
      el("div", { class: "settings" }, el("div", { class: "row" }, role.node, save), libsRow));
  });

  const name = el("input", { type: "text", required: true, autocomplete: "off", pattern: "[A-Za-z0-9._@\\-]{1,64}", title: "Letters, digits and . _ @ -" });
  const pass = el("input", { type: "password", required: true, minlength: 8, autocomplete: "new-password" });
  const newLibs = libraryPicker();
  const newLibsRow = libraries.length > 1 ? el("div", { class: "wide" }, caption("Can see"), newLibs.node) : null;
  const newRole = rolePicker("user", (v) => { if (newLibsRow) newLibsRow.hidden = v === "admin"; });
  const add = el("form", { class: "form", onsubmit: async (ev) => {
    ev.preventDefault();
    try {
      await api("/api/users", { method: "POST", body: { username: name.value, password: pass.value, role: newRole.value(), libraries: newLibs.value() } });
      toast("Added " + name.value);
      rerender();
    } catch (e) { toast(e.message); }
  } },
  el("label", {}, "User name", name),
  el("label", {}, "Password (8 or more characters)", pass),
  el("div", { class: "wide" }, caption("Role"), newRole.node),
  newLibsRow,
  el("div", { class: "wide" }, el("button", { class: "btn primary", type: "submit" }, "Add user")));

  return [
    el("section", {}, el("div", { class: "cards" }, cards),
      el("p", { class: "note" }, "Listeners see only the libraries chosen for them, on every device. Admins can rescan, edit details and manage users.")),
    el("section", { class: "panel-card" }, el("h2", {}, "Add a user"), add),
  ];
}

// ------------------------------------------------------------------ start

async function start() {
  state.me = await api("/api/me");
  const hash = location.hash.slice(1);
  if (VIEWS.some((v) => v.id === hash)) state.view = hash;
  render();
}

(async () => {
  loadPrefs();
  try { state.server = await api("/api/health"); } catch { /* shown as "Kithara" */ }
  document.title = state.server?.name ? `${state.server.name} · Kithara` : "Kithara";
  state.token = loadToken();
  if (!state.token) return renderSignIn();
  try { await start(); } catch (e) { if (e.status !== 401) renderSignIn(e.message); }
})();
