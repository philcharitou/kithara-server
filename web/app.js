// The kithara-server console. Plain modules, no build step, no dependencies. It signs
// in like an app and uses the same API; every bit of text from the server is set with
// textContent, never parsed as HTML. Laid out as an appliance console: a header bar, a
// sidebar, a dashboard of widgets, and data grids.

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
  view: "dashboard",
  prefs: { layout: "table", sort: "title", filter: "all", library: "", theme: "dark" },
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
const span = (cls, ...c) => el("span", { class: cls }, ...c);

// Material icons, drawn into inline SVG.
const ICONS = {
  dashboard: "M3 13h8V3H3v10zm0 8h8v-6H3v6zm10 0h8V11h-8v10zm0-18v6h8V3h-8z",
  library: "M4 6H2v14c0 1.1.9 2 2 2h14v-2H4V6zm16-4H8c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h12c1.1 0 2-.9 2-2V4c0-1.1-.9-2-2-2zm-1 9H9V9h10v2zm-4 4H9v-2h6v2zm4-8H9V5h10v2z",
  stats: "M3.5 18.49l6-6.01 4 4L22 6.92l-1.41-1.41-7.09 7.97-4-4L2 16.99z",
  devices: "M4 6h18V4H4c-1.1 0-2 .9-2 2v11H0v3h14v-3H4V6zm19 2h-6c-.55 0-1 .45-1 1v10c0 .55.45 1 1 1h6c.55 0 1-.45 1-1V9c0-.55-.45-1-1-1zm-1 9h-4v-7h4v7z",
  users: "M16 11c1.66 0 2.99-1.34 2.99-3S17.66 5 16 5c-1.66 0-3 1.34-3 3s1.34 3 3 3zm-8 0c1.66 0 2.99-1.34 2.99-3S9.66 5 8 5C6.34 5 5 6.34 5 8s1.34 3 3 3zm0 2c-2.33 0-7 1.17-7 3.5V19h14v-2.5c0-2.33-4.67-3.5-7-3.5zm8 0c-.29 0-.62.02-.97.05 1.16.84 1.97 1.97 1.97 3.45V19h6v-2.5c0-2.33-4.67-3.5-7-3.5z",
  folder: "M10 4H4c-1.1 0-1.99.9-1.99 2L2 18c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2h-8l-2-2z",
  server: "M20 13H4c-.55 0-1 .45-1 1v6c0 .55.45 1 1 1h16c.55 0 1-.45 1-1v-6c0-.55-.45-1-1-1zM7 19c-1.1 0-2-.9-2-2s.9-2 2-2 2 .9 2 2-.9 2-2 2zM20 3H4c-.55 0-1 .45-1 1v6c0 .55.45 1 1 1h16c.55 0 1-.45 1-1V4c0-.55-.45-1-1-1zM7 9c-1.1 0-2-.9-2-2s.9-2 2-2 2 .9 2 2-.9 2-2 2z",
  search: "M15.5 14h-.79l-.28-.27C15.41 12.59 16 11.11 16 9.5 16 5.91 13.09 3 9.5 3S3 5.91 3 9.5 5.91 16 9.5 16c1.61 0 3.09-.59 4.23-1.57l.27.28v.79l5 4.99L20.49 19l-4.99-5zm-6 0C7.01 14 5 11.99 5 9.5S7.01 5 9.5 5 14 7.01 14 9.5 11.99 14 9.5 14z",
  check: "M9 16.17L4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41z",
  grid: "M3 3v8h8V3H3zm6 6H5V5h4v4zm-6 4v8h8v-8H3zm6 6H5v-4h4v4zm4-16v8h8V3h-8zm6 6h-4V5h4v4zm-6 4v8h8v-8h-8zm6 6h-4v-4h4v4z",
  table: "M3 3v18h18V3H3zm8 16H5v-6h6v6zm0-8H5V5h6v6zm8 8h-6v-6h6v6zm0-8h-6V5h6v6z",
  close: "M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z",
  back: "M20 11H7.83l5.59-5.59L12 4l-8 8 8 8 1.41-1.41L7.83 13H20v-2z",
  menu: "M3 18h18v-2H3v2zm0-5h18v-2H3v2zm0-7v2h18V6H3z",
  open: "M19 19H5V5h7V3H5a2 2 0 00-2 2v14a2 2 0 002 2h14c1.1 0 2-.9 2-2v-7h-2v7zM14 3v2h3.59l-9.83 9.83 1.41 1.41L19 6.41V10h2V3h-7z",
  download: "M19 9h-4V3H9v6H5l7 7 7-7zM5 18v2h14v-2H5z",
  refresh: "M17.65 6.35C16.2 4.9 14.21 4 12 4c-4.42 0-7.99 3.58-7.99 8s3.57 8 7.99 8c3.73 0 6.84-2.55 7.73-6h-2.08c-.82 2.33-3.04 4-5.65 4-3.31 0-6-2.69-6-6s2.69-6 6-6c1.66 0 3.14.69 4.22 1.78L13 11h7V4l-2.35 2.35z",
  logout: "M17 7l-1.41 1.41L18.17 11H8v2h10.17l-2.58 2.58L17 17l5-5zM4 5h8V3H4c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h8v-2H4V5z",
  edit: "M3 17.25V21h3.75L17.81 9.94l-3.75-3.75L3 17.25zM20.71 7.04a1 1 0 000-1.41l-2.34-2.34a1 1 0 00-1.41 0l-1.83 1.83 3.75 3.75 1.83-1.83z",
  magic: "M7.5 5.6L10 7 8.6 4.5 10 2 7.5 3.4 5 2l1.4 2.5L5 7zm12 9.8L17 14l1.4 2.5L17 19l2.5-1.4L22 19l-1.4-2.5L22 14zM22 2l-2.5 1.4L17 2l1.4 2.5L17 7l2.5-1.4L22 7l-1.4-2.5zm-7.63 5.29a.996.996 0 00-1.41 0L1.29 18.96a.996.996 0 000 1.41l2.34 2.34c.39.39 1.02.39 1.41 0L16.7 11.05a.996.996 0 000-1.41l-2.33-2.35zm-1.03 5.49l-2.12-2.12 2.44-2.44 2.12 2.12-2.44 2.44z",
  chapters: "M3 13h2v-2H3v2zm0 4h2v-2H3v2zm0-8h2V7H3v2zm4 4h14v-2H7v2zm0 4h14v-2H7v2zM7 7v2h14V7H7z",
  files: "M14 2H6c-1.1 0-1.99.9-1.99 2L4 20c0 1.1.89 2 1.99 2H18c1.1 0 2-.9 2-2V8l-6-6zm2 16H8v-2h8v2zm0-4H8v-2h8v2zm-3-5V3.5L18.5 9H13z",
  remove: "M6 19c0 1.1.9 2 2 2h8c1.1 0 2-.9 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z",
  add: "M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6v2z",
  up: "M4 12l1.41 1.41L11 7.83V20h2V7.83l5.58 5.59L20 12l-8-8-8 8z",
  phone: "M17 1.01L7 1c-1.1 0-2 .9-2 2v18c0 1.1.9 2 2 2h10c1.1 0 2-.9 2-2V3c0-1.1-.9-1.99-2-1.99zM17 19H7V5h10v14z",
  tablet: "M21 4H3c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h18c1.1 0 1.99-.9 1.99-2L23 6c0-1.1-.9-2-2-2zm-2 14H5V6h14v12z",
  computer: "M20 18c1.1 0 1.99-.9 1.99-2L22 6c0-1.1-.9-2-2-2H4c-1.1 0-2 .9-2 2v10c0 1.1.9 2 2 2H0v2h24v-2h-4zM4 6h16v10H4V6z",
  web: "M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-1 17.93c-3.95-.49-7-3.85-7-7.93 0-.62.08-1.21.21-1.79L9 15v1c0 1.1.9 2 2 2v1.93zm6.9-2.54c-.26-.81-1-1.39-1.9-1.39h-1v-3c0-.55-.45-1-1-1H8v-2h2c.55 0 1-.45 1-1V7h2c1.1 0 2-.9 2-2v-.41c2.93 1.19 5 4.06 5 7.41 0 2.08-.8 3.97-2.1 5.39z",
  dark: "M12 3a9 9 0 109 9c0-.46-.04-.92-.1-1.36a5.389 5.389 0 01-4.4 2.26 5.403 5.403 0 01-3.14-9.8c-.44-.06-.9-.1-1.36-.1z",
  light: "M12 7c-2.76 0-5 2.24-5 5s2.24 5 5 5 5-2.24 5-5-2.24-5-5-5zM2 13h2c.55 0 1-.45 1-1s-.45-1-1-1H2c-.55 0-1 .45-1 1s.45 1 1 1zm18 0h2c.55 0 1-.45 1-1s-.45-1-1-1h-2c-.55 0-1 .45-1 1s.45 1 1 1zM11 2v2c0 .55.45 1 1 1s1-.45 1-1V2c0-.55-.45-1-1-1s-1 .45-1 1zm0 18v2c0 .55.45 1 1 1s1-.45 1-1v-2c0-.55-.45-1-1-1s-1 .45-1 1zM5.99 4.58a.996.996 0 00-1.41 0 .996.996 0 000 1.41l1.06 1.06c.39.39 1.03.39 1.41 0s.39-1.03 0-1.41L5.99 4.58zm12.37 12.37a.996.996 0 00-1.41 0 .996.996 0 000 1.41l1.06 1.06c.39.39 1.03.39 1.41 0a.996.996 0 000-1.41l-1.06-1.06zm1.06-10.96a.996.996 0 000-1.41.996.996 0 00-1.41 0l-1.06 1.06c-.39.39-.39 1.03 0 1.41s1.03.39 1.41 0l1.06-1.06zM7.05 18.36a.996.996 0 000-1.41.996.996 0 00-1.41 0l-1.06 1.06c-.39.39-.39 1.03 0 1.41s1.03.39 1.41 0l1.06-1.06z",
};

function icon(name) {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("class", "i");
  svg.setAttribute("aria-hidden", "true");
  const p = document.createElementNS(ns, "path");
  p.setAttribute("d", ICONS[name]);
  svg.append(p);
  return svg;
}

const iconBtn = (name, label, onclick, cls = "icon-btn") =>
  el("button", { class: cls, type: "button", title: label, "aria-label": label, onclick }, icon(name));

function toast(msg) {
  const t = document.getElementById("toast");
  // An open sheet sits in the top layer; the toast goes with it so it shows above the scrim.
  (dialog.open ? dialog : document.body).append(t);
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
  if (state.prefs.theme !== "light") state.prefs.theme = "dark";
  if (!["table", "grid"].includes(state.prefs.layout)) state.prefs.layout = "table";
}
function savePrefs() {
  try { storage()?.setItem(PREFS_KEY, JSON.stringify(state.prefs)); } catch { /* not kept */ }
}

// Dark unless light is picked.
function applyTheme() {
  if (state.prefs.theme === "light") document.documentElement.dataset.theme = "light";
  else delete document.documentElement.dataset.theme;
  document.querySelector('meta[name="theme-color"]')?.setAttribute("content", state.prefs.theme === "light" ? "#f3edf7" : "#100f14");
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
  const txt = await res.text();
  const data = txt ? JSON.parse(txt) : null;
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
const fmtDateTime = (ms) => (ms ? new Date(ms).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : "");
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
const fmtUptime = (ms) => {
  const m = Math.floor(ms / 60000), d = Math.floor(m / 1440), h = Math.floor((m % 1440) / 60);
  return d ? `${d} day${d === 1 ? "" : "s"}, ${h}h ${m % 60}m` : h ? `${h}h ${m % 60}m` : `${m % 60}m`;
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
    box.replaceWith(el("img", { class: "cover", src: blobURL, alt: "" }));
  } catch { /* keep the stand-in */ }
}

// The apps' stand-in for a book without artwork (the title's first two letters), and
// their FinishedBadge for a finished one.
function cover(book) {
  const box = el("div", { class: "cover blank", "aria-hidden": "true" }, (book.title || "").slice(0, 2));
  if (book.coverUrl) {
    box.dataset.src = book.coverUrl + "?v=" + book.updatedAt;
    coverObserver.observe(box);
  }
  const finished = book.id && progressOf(book)?.finished;
  return el("div", { class: "cover-box" }, box, finished ? el("span", { class: "finished", title: "Finished" }, icon("check")) : null);
}

// ------------------------------------------------------------------ sign in

function renderSignIn(message = "") {
  dialog.open && dialog.close();
  const err = el("p", { class: "small err", role: "alert" }, message);
  const user = el("input", { type: "text", name: "username", autocomplete: "username", required: true, autofocus: true });
  const pass = el("input", { type: "password", name: "password", autocomplete: "current-password", required: true });
  const remember = el("input", { type: "checkbox", name: "remember" });
  const submit = el("button", { class: "btn", type: "submit" }, "Sign in");
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
    el("label", { class: "field" }, el("span", {}, "Username"), user),
    el("label", { class: "field" }, el("span", {}, "Password"), pass),
    el("label", { class: "check small" }, remember, "Keep me signed in on this browser"),
    err,
    submit,
    el("p", { class: "tiny muted" }, "Accounts are made by whoever runs this server."));
  app.replaceChildren(el("div", { class: "signin" }, el("div", { class: "box" },
    el("div", { class: "box-head" }, el("img", { src: "logo.svg", alt: "" }),
      el("div", {}, el("div", { class: "strong", style: "font-size:16px" }, "Kithara"), el("div", { class: "small muted" }, state.server?.name || "Audiobook server"))),
    form)));
}

function browserName() {
  const ua = navigator.userAgent;
  const b = /Firefox\//.test(ua) ? "Firefox" : /Edg\//.test(ua) ? "Edge" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "Browser";
  const os = /Windows/.test(ua) ? "Windows" : /Mac OS X/.test(ua) ? "macOS" : /Android/.test(ua) ? "Android" : /iPhone|iPad/.test(ua) ? "iOS" : /Linux/.test(ua) ? "Linux" : "";
  return `Web (${b}${os ? " on " + os : ""})`;
}

// ------------------------------------------------------------------ shell

const VIEWS = [
  { id: "dashboard", label: "Dashboard", icon: "dashboard" },
  { id: "library", label: "Library", icon: "library" },
  { id: "stats", label: "Listening", icon: "stats" },
  { id: "devices", label: "Devices", icon: "devices" },
  { id: "libraries", label: "Libraries", icon: "folder", admin: true },
  { id: "users", label: "Users", icon: "users", admin: true },
];

function renderShell(current) {
  const side = el("aside", { class: "side" });
  const navBtn = (v) => el("button", { type: "button", "aria-current": state.view === v.id ? "page" : null, onclick: () => { side.classList.remove("open"); go(v.id); } }, icon(v.icon), v.label);
  side.append(
    el("div", { class: "me" },
      el("span", { class: "avatar", "aria-hidden": "true" }, state.me.name.slice(0, 1)),
      el("div", { style: "min-width:0" }, el("div", { class: "strong clip" }, state.me.name), el("div", { class: "small muted" }, isAdmin() ? "Administrator" : "Listener"))),
    el("nav", { "aria-label": "Sections" },
      VIEWS.filter((v) => !v.admin).map(navBtn),
      isAdmin() ? [el("div", { class: "group" }, "Administration"), VIEWS.filter((v) => v.admin).map(navBtn)] : null));

  const themeBtn = iconBtn(state.prefs.theme === "light" ? "dark" : "light", state.prefs.theme === "light" ? "Dark theme" : "Light theme", () => {
    state.prefs.theme = state.prefs.theme === "light" ? "dark" : "light";
    savePrefs(); applyTheme(); render();
  });
  const hdr = el("header", { class: "hdr" },
    el("button", { class: "icon-btn menu", type: "button", "aria-label": "Menu", onclick: () => side.classList.toggle("open") }, icon("menu")),
    el("a", { class: "brand", href: "#dashboard", onclick: (e) => { e.preventDefault(); go("dashboard"); } },
      el("img", { src: "logo.svg", alt: "" }), el("span", { class: "name" }, "Kithara")),
    el("span", { class: "server small clip" }, state.server?.name || ""),
    el("span", { class: "spacer" }),
    themeBtn,
    iconBtn("logout", "Sign out", signOut));

  const tools = el("div", { class: "tools", id: "tools" });
  const page = el("main", { class: "page" },
    el("div", { class: "crumb" }, el("h1", {}, current.label), tools),
    el("div", { class: "body", id: "main" }));
  app.replaceChildren(hdr, side, page);
  return { main: page.querySelector("#main"), tools };
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
  const { main, tools } = renderShell(current);
  main.append(el("p", { class: "muted" }, "Loading…"));
  try {
    const view = { dashboard: viewDashboard, library: viewLibrary, stats: viewStats, devices: viewDevices, libraries: viewLibraries, users: viewUsers }[current.id];
    main.replaceChildren(...[await view(tools)].flat().filter(Boolean));
  } catch (e) {
    if (e.status !== 401) main.replaceChildren(el("p", { class: "err" }, e.message));
  }
}

async function refreshMain(view) {
  const main = document.getElementById("main");
  const tools = document.getElementById("tools");
  if (main) main.replaceChildren(...[await view(tools)].flat().filter(Boolean));
}

// A dashboard widget: a header with actions, then a visual and an overview list, or a
// full-width body.
function widget(title, { actions = [], visual, overview, body, wide, foot } = {}) {
  return el("section", { class: "widget" + (wide ? " wide" : "") },
    el("div", { class: "w-head" }, el("h2", { class: "clip" }, title), actions),
    visual || overview ? el("div", { class: "w-body" },
      el("div", { class: "w-visual" }, visual),
      el("div", { class: "overview" }, el("div", { class: "label" }, "Overview"), overview)) :
      el("div", { class: "w-body full" }, body),
    foot ? el("div", { class: "w-foot" }, foot) : null);
}

function kv(pairs) {
  const dl = el("dl", { class: "kv" });
  for (const [k, v] of pairs) {
    if (v === undefined || v === null || v === "") continue;
    dl.append(el("dt", {}, k + ":"), el("dd", { title: typeof v === "string" ? v : null }, v));
  }
  return dl;
}

// ------------------------------------------------------------------ data

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

function gradientProgress(fraction, thick = false) {
  return el("div", { class: "progress" + (thick ? " thick" : ""), role: "progressbar", "aria-valuenow": Math.round(fraction * 100), "aria-valuemin": 0, "aria-valuemax": 100 },
    el("i", { style: `width:${Math.max(1, fraction * 100).toFixed(1)}%` }));
}

function statusText(b) {
  const p = progressOf(b);
  const s = statusOf(b);
  if (s === "finished") return span("small accent", "Finished");
  if (s === "progress") return span("small muted", `${Math.round(p.fraction * 100)}% · ${fmtDuration(b.durationMs - p.positionMs)} left`);
  return span("small muted", "Not started");
}

function chip(label, on, onclick) {
  return el("button", { class: "chip", type: "button", "aria-pressed": on ? "true" : "false", onclick }, on ? icon("check") : null, label);
}

// ------------------------------------------------------------------ dashboard

async function viewDashboard(tools) {
  const tz = -new Date().getTimezoneOffset();
  const [status, stats] = await Promise.all([api("/api/status"), api("/api/stats?tzOffset=" + tz), loadLibrary()]);
  const out = [];
  const byId = new Map(state.books.map((b) => [b.id, b]));
  const totalMs = state.books.reduce((n, b) => n + b.durationMs, 0);

  if (isAdmin()) {
    const rescan = el("button", { class: "btn secondary small", type: "button", onclick: async () => {
      rescan.disabled = true;
      try { const r = await api("/api/rescan", { method: "POST" }); toast(`Rescanned: ${plural(r.books, "book")}`); render(); }
      catch (e) { toast(e.message); rescan.disabled = false; }
    } }, icon("refresh"), "Rescan");
    tools.append(rescan);
  }

  // System information
  out.push(widget("System Information", {
    visual: [el("img", { class: "logo", src: "logo.svg", alt: "" }), span("strong", "Kithara"), span("tiny muted", "kithara-server")],
    overview: kv([
      ["Name", status.name],
      ["Version", status.version],
      ["Protocol", "v" + status.protocol],
      ["Platform", status.platform],
      ["Uptime", fmtUptime(Date.now() - status.startedAt)],
      ["Last scan", status.lastScanAt ? `${relative(status.lastScanAt)}, ${status.lastScanMs} ms` : "not yet"],
      ["ffprobe", status.ffprobe === undefined ? undefined : status.ffprobe ? span("ok", "found") : span("err", "missing")],
    ]),
  }));

  // Listening, as a donut of where the time went
  const top = [...stats.books].sort((a, b) => b.listenedMs - a.listenedMs);
  const slices = top.slice(0, 3).map((r) => ({ label: byId.get(r.bookId)?.title || "Removed book", ms: r.listenedMs }));
  const rest = top.slice(3).reduce((n, r) => n + r.listenedMs, 0);
  if (rest) slices.push({ label: "Other books", ms: rest });
  const colors = ["var(--deep)", "var(--mid)", "var(--glint)", "var(--outline)"];
  out.push(widget("Listening", {
    actions: [iconBtn("open", "Open Listening", () => go("stats"), "icon-btn sm")],
    visual: donut(slices.map((s, i) => ({ value: s.ms, color: colors[i] })), fmtDuration(stats.totalListenedMs), "all time"),
    overview: [
      kv([["Streak", plural(stats.currentStreakDays, "day")], ["Longest", plural(stats.longestStreakDays, "day")], ["Days listened", stats.daysListened], ["Finished", plural(stats.booksFinished, "book")]]),
      slices.length ? el("div", { class: "legend", style: "margin-top:10px" }, slices.map((s, i) =>
        el("div", { class: "clip" }, el("span", { class: "sw", style: `background:${colors[i]}` }), `${s.label} · ${fmtDuration(s.ms)}`))) : null,
    ],
  }));

  // Continue listening
  const going = state.books.filter((b) => statusOf(b) === "progress")
    .sort((a, b) => (state.progress.get(b.id)?.updatedAt || 0) - (state.progress.get(a.id)?.updatedAt || 0)).slice(0, 4);
  out.push(widget("Continue Listening", {
    actions: [iconBtn("open", "Open Library", () => go("library"), "icon-btn sm")],
    body: going.length ? going.map((b) => el("button", { class: "mini", type: "button", onclick: () => openBook(b.id) },
      cover(b), el("div", { style: "min-width:0" }, el("div", { class: "strong clip" }, b.title), gradientProgress(progressOf(b).fraction), statusText(b)))) :
      el("p", { class: "muted" }, "Nothing in progress. Books you start in the apps show up here."),
  }));

  // One widget per library, like a storage pool
  for (const l of state.libraries) {
    const books = state.books.filter((b) => b.library === l.name);
    const done = books.filter((b) => statusOf(b) === "finished").length;
    out.push(widget("Library", {
      actions: [iconBtn("open", `Show ${l.name}`, () => { state.prefs.library = l.name; savePrefs(); go("library"); }, "icon-btn sm")],
      visual: [el("span", { class: "ico gold" }, icon("folder")), span("big clip", l.name)],
      overview: kv([
        ["Status", l.error ? span("status err", "Unreadable") : span("status ok", "Online")],
        ["Path", isAdmin() ? span("mono", l.path || "") : undefined],
        ["Books", l.books],
        ["Length", fmtDuration(l.durationMs)],
        ["Finished", `${done} of ${books.length}`],
        ["Updated", l.updatedAt ? relative(l.updatedAt) : "never"],
        ["Error", l.error ? span("err", l.error) : undefined],
      ]),
    }));
  }
  if (!state.libraries.length) {
    out.push(widget("Library", {
      body: [el("p", { class: "muted" }, isAdmin() ? "No libraries yet. Add a folder of audiobooks to get started." : "No libraries yet."),
        isAdmin() ? el("div", { style: "margin-top:12px" }, el("button", { class: "btn", type: "button", onclick: () => go("libraries") }, icon("add"), "Add a library")) : null],
    }));
  }

  // Devices, and for admins, users
  const { devices } = await api("/api/devices");
  out.push(widget("Devices", {
    actions: [iconBtn("open", "Open Devices", () => go("devices"), "icon-btn sm")],
    visual: [el("span", { class: "ico" }, icon("devices")), span("big", devices.length), span("tiny muted", "signed in")],
    overview: kv(devices.slice(0, 5).map((d) => [d.name || "Unnamed", relative(d.lastSeenAt)])),
  }));
  if (isAdmin()) {
    out.push(widget("Server", {
      actions: [iconBtn("open", "Open Users", () => go("users"), "icon-btn sm")],
      visual: [el("span", { class: "ico" }, icon("server")), span("big", status.books), span("tiny muted", "books")],
      overview: kv([["Libraries", state.libraries.length], ["Total length", fmtDuration(totalMs)], ["Users", status.users], ["Devices", status.devices], ["Go", status.goVersion]]),
    }));
  }

  return el("div", { class: "widgets" }, out);
}

// An SVG ring of slices (stroke-dasharray on circles), the total in the middle.
function donut(slices, center, caption) {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 42 42");
  const ring = (color, dash, offset) => {
    const c = document.createElementNS(ns, "circle");
    for (const [k, v] of Object.entries({ cx: 21, cy: 21, r: 15.915, fill: "none", "stroke-width": 5, "stroke-dasharray": dash, "stroke-dashoffset": offset })) c.setAttribute(k, v);
    c.style.stroke = color;
    return c;
  };
  svg.append(ring("var(--track)", "100 0", 0));
  const total = slices.reduce((n, s) => n + s.value, 0);
  let at = 0;
  for (const s of slices) {
    if (!total || !s.value) continue;
    const pct = (s.value / total) * 100;
    svg.append(ring(s.color, `${Math.max(0, pct - 0.8)} ${100 - pct + 0.8}`, -at));
    at += pct;
  }
  return el("div", { class: "donut" }, svg, el("div", { class: "mid" }, span("strong", center), span("tiny muted", caption)));
}

// ------------------------------------------------------------------ library

function matches(b, q) {
  if (!q) return true;
  const hay = [b.title, b.subtitle, b.author, b.narrator, b.series, ...(b.genres || [])].join(" ").toLowerCase();
  return q.toLowerCase().split(/\s+/).every((w) => hay.includes(w));
}

const byTitle = (a, b) => a.title.localeCompare(b.title, undefined, { numeric: true, sensitivity: "base" });
const SORTS = {
  title: ["Title", byTitle],
  author: ["Author", (a, b) => (a.author || "~").localeCompare(b.author || "~") || byTitle(a, b)],
  series: ["Series", (a, b) => (a.series || "~").localeCompare(b.series || "~") || (a.seriesIndex ?? 1e9) - (b.seriesIndex ?? 1e9) || byTitle(a, b)],
  recent: ["Recently played", (a, b) => (state.progress.get(b.id)?.updatedAt || 0) - (state.progress.get(a.id)?.updatedAt || 0) || byTitle(a, b)],
  length: ["Length", (a, b) => b.durationMs - a.durationMs],
  updated: ["Recently changed", (a, b) => b.updatedAt - a.updatedAt],
};

async function viewLibrary(tools) {
  await loadLibrary();
  const prefs = state.prefs;
  if (isAdmin()) {
    const rescan = el("button", { class: "btn secondary small", type: "button", onclick: async () => {
      rescan.disabled = true;
      try { const r = await api("/api/rescan", { method: "POST" }); toast(`Rescanned: ${plural(r.books, "book")}`); render(); }
      catch (e) { toast(e.message); rescan.disabled = false; }
    } }, icon("refresh"), "Rescan");
    tools.append(rescan);
  }
  if (!state.libraries.length) {
    return el("div", { class: "widget" }, el("div", { class: "w-body full" },
      el("p", { class: "muted" }, isAdmin() ? "No libraries yet. Add a folder of audiobooks and they will show up here and in the apps." : "Whoever runs this server hasn't added a library yet."),
      isAdmin() ? el("div", { style: "margin-top:12px" }, el("button", { class: "btn", type: "button", onclick: () => go("libraries") }, icon("add"), "Add a library")) : null));
  }

  const shelf = el("div");
  const count = span("small muted");
  const fill = () => {
    const shown = state.books
      .filter((b) => !prefs.library || b.library === prefs.library)
      .filter((b) => prefs.filter === "all" || statusOf(b) === prefs.filter)
      .filter((b) => matches(b, state.query))
      .sort((SORTS[prefs.sort] || SORTS.title)[1]);
    count.textContent = shown.length === state.books.length ? plural(shown.length, "book") : `${shown.length} of ${state.books.length} books`;
    if (!shown.length) {
      shelf.replaceChildren(el("div", { class: "table-wrap" }, el("p", { class: "muted", style: "padding:24px;text-align:center" }, "Nothing matches.")));
      return;
    }
    shelf.replaceChildren(prefs.layout === "grid" ? el("div", { class: "cover-grid" }, shown.map(bookTile)) : bookTable(shown));
  };

  const filters = el("div", { class: "chips", role: "group", "aria-label": "Show" });
  const drawFilters = () => filters.replaceChildren(...[["all", "All"], ["progress", "In progress"], ["new", "Not started"], ["finished", "Finished"]].map(([id, label]) =>
    chip(label, prefs.filter === id, () => { prefs.filter = id; savePrefs(); drawFilters(); fill(); })));
  drawFilters();

  const libSelect = state.libraries.length > 1 ? el("select", { "aria-label": "Library", onchange: (e) => { prefs.library = e.target.value; savePrefs(); fill(); } },
    el("option", { value: "" }, "All libraries"),
    state.libraries.map((l) => el("option", { value: l.name, selected: prefs.library === l.name ? true : null }, `${l.name} (${l.books})`))) : null;
  const sortSelect = el("select", { "aria-label": "Sort by", onchange: (e) => { prefs.sort = e.target.value; savePrefs(); fill(); } },
    Object.entries(SORTS).map(([id, [label]]) => el("option", { value: id, selected: prefs.sort === id ? true : null }, "Sort: " + label)));
  const layoutBtns = [["table", "Table"], ["grid", "Covers"]].map(([id, label], i) => el("button", {
    class: "icon-btn", type: "button", title: label, "aria-label": label, "aria-pressed": prefs.layout === id ? "true" : "false",
    onclick: () => { prefs.layout = id; savePrefs(); layoutBtns.forEach((b, j) => b.setAttribute("aria-pressed", i === j ? "true" : "false")); fill(); },
  }, icon(id)));

  fill();
  return [
    el("div", { class: "toolbar" },
      el("div", { class: "search" }, icon("search"), el("input", { type: "search", placeholder: "Search title, author, narrator, series", "aria-label": "Search", value: state.query,
        oninput: (e) => { state.query = e.target.value; fill(); } })),
      libSelect, sortSelect, layoutBtns),
    el("div", { class: "toolbar" }, filters, el("span", { class: "grow" }), count),
    shelf,
  ];
}

function bookTable(books) {
  const multi = state.libraries.length > 1;
  return el("div", { class: "table-wrap" }, el("table", { class: "grid" },
    el("thead", {}, el("tr", {},
      el("th", { class: "c-cover" }), el("th", {}, "Title"), el("th", { class: "hide-sm" }, "Author"), el("th", { class: "hide-sm" }, "Narrator"),
      el("th", { class: "hide-sm" }, "Series"), multi ? el("th", { class: "hide-sm" }, "Library") : null,
      el("th", { class: "c-num" }, "Length"), el("th", { class: "c-prog" }, "Progress"))),
    el("tbody", {}, books.map((b) => {
      const p = progressOf(b);
      return el("tr", { tabindex: 0, onclick: () => openBook(b.id), onkeydown: (e) => { if (e.key === "Enter") openBook(b.id); } },
        el("td", { class: "c-cover" }, cover(b)),
        el("td", {}, el("div", { class: "strong clip2" }, b.title), b.subtitle ? el("div", { class: "small muted clip" }, b.subtitle) : null),
        el("td", { class: "hide-sm" }, b.author || span("muted", "—")),
        el("td", { class: "hide-sm" }, b.narrator || span("muted", "—")),
        el("td", { class: "hide-sm" }, b.series ? `${b.series}${b.seriesIndex != null ? " #" + b.seriesIndex : ""}` : span("muted", "—")),
        multi ? el("td", { class: "hide-sm muted" }, b.library) : null,
        el("td", { class: "c-num" }, fmtDuration(b.durationMs)),
        el("td", { class: "c-prog" }, p && !p.finished && p.positionMs > 0 ? gradientProgress(p.fraction) : null, statusText(b)));
    }))));
}

function bookTile(b) {
  const p = progressOf(b);
  return el("button", { class: "tile", type: "button", onclick: () => openBook(b.id) },
    cover(b),
    p && !p.finished && p.positionMs > 0 ? gradientProgress(p.fraction) : null,
    el("div", { class: "strong clip2" }, b.title),
    el("div", { class: "small muted clip" }, b.author || "Unknown author"),
    statusText(b));
}

// ------------------------------------------------------------------ sheets

function openSheet(title, parts, { onBack, wide } = {}) {
  dialog.replaceChildren(
    el("div", { class: "sheet-head" },
      onBack ? iconBtn("back", "Back", onBack) : null,
      el("h2", { class: "clip" }, title || ""),
      iconBtn("close", "Close", () => dialog.close())),
    el("div", { class: "sheet" }, ...parts.filter(Boolean)));
  dialog.style.width = wide ? "min(900px, calc(100vw - 24px))" : "";
  if (!dialog.open) dialog.showModal();
  dialog.scrollTop = 0;
}

dialog.addEventListener("click", (e) => { if (e.target === dialog) dialog.close(); });

// ------------------------------------------------------------------ one book

function openBook(id, mode = "view") {
  const b = state.books.find((x) => x.id === id);
  if (!b) return;
  if (mode === "edit") return openSheet("Edit details · " + b.title, bookEditor(b), { onBack: () => openBook(id) });
  if (mode === "match") return openSheet("Find details · " + b.title, bookMatcher(b), { onBack: () => openBook(id), wide: true });
  openSheet(b.title, bookDetail(b), { wide: true });
}

function currentChapter(b, p) {
  if (!p || p.finished || !b.chapters.length) return -1;
  return b.chapters.findIndex((c) => p.positionMs >= c.startMs && p.positionMs < c.endMs);
}

function bookDetail(b) {
  const p = progressOf(b);
  const size = b.files.reduce((n, f) => n + (f.sizeBytes || 0), 0);
  const download = el("button", { class: "btn", type: "button", onclick: async () => {
    download.disabled = true;
    try {
      const link = await api(`/api/books/${encodeURIComponent(b.id)}/download-link`, { method: "POST" });
      const a = el("a", { href: link.url, download: "" });
      document.body.append(a); a.click(); a.remove();
    } catch (e) { toast(e.message); }
    download.disabled = false;
  } }, icon("download"), `Download (${fmtBytes(size)})`);
  const actions = el("div", { class: "row", style: "margin-top:14px" }, download);
  if (isAdmin()) {
    actions.append(
      el("button", { class: "btn secondary", type: "button", onclick: () => openBook(b.id, "match") }, icon("magic"), "Find details"),
      el("button", { class: "btn secondary", type: "button", onclick: () => openBook(b.id, "edit") }, icon("edit"), "Edit"));
  }

  const cur = currentChapter(b, p);
  const progress = p && (p.positionMs > 0 || p.finished) ? el("div", { class: "panel stack" },
    el("div", { class: "row" },
      span("strong grow", cur >= 0 ? b.chapters[cur].title : p.finished ? "Finished" : "In progress"),
      cur >= 0 ? span("small muted", `Chapter ${cur + 1} of ${b.chapters.length}`) : null),
    gradientProgress(p.finished ? 1 : p.fraction, true),
    el("div", { class: "row small muted", style: "justify-content:space-between" },
      span("", p.finished ? (p.finishedAt ? "Finished " + fmtDate(p.finishedAt) : "") : fmtClock(p.positionMs) + " / " + fmtClock(b.durationMs)),
      p.finished ? null : span("", `${fmtDuration(b.durationMs - p.positionMs)} left`),
      span("", p.startedAt ? "Started " + fmtDate(p.startedAt) : ""))) : null;

  return [
    el("div", { class: "hero" }, cover(b), el("div", { style: "min-width:0" },
      el("h2", {}, b.title),
      b.subtitle ? el("p", { class: "muted" }, b.subtitle) : null,
      el("div", { style: "margin-top:12px" }, kv([
        ["Author", b.author], ["Narrator", b.narrator],
        ["Series", b.series ? `${b.series}${b.seriesIndex != null ? " #" + b.seriesIndex : ""}` : ""],
        ["Length", fmtDuration(b.durationMs)], ["Published", [b.publishedYear, b.publisher].filter(Boolean).join(", ")],
        ["Language", b.language], ["ISBN", b.isbn], ["ASIN", b.asin],
        ["Library", state.libraries.length > 1 ? b.library : ""],
      ])),
      (b.genres || []).length ? el("div", { class: "tags" }, b.genres.slice(0, 6).map((g) => el("span", { class: "tag" }, g))) : null,
      actions)),
    progress,
    b.description ? el("p", { class: "desc" }, b.description) : null,
    b.chapters.length ? el("details", { class: "panel" },
      el("summary", {}, icon("chapters"), span("strong grow", plural(b.chapters.length, "chapter")), cur >= 0 ? span("small muted", `on ${cur + 1}`) : null),
      el("div", { class: "lines" }, b.chapters.map((c, i) => el("div", { class: i === cur ? "now" : "" },
        span("n", i + 1), span("t", c.title), span("end", fmtClock(c.startMs)))))) : null,
    el("details", { class: "panel" },
      el("summary", {}, icon("files"), span("strong grow", plural(b.files.length, "file")), span("small muted", fmtBytes(size))),
      el("div", { class: "lines" }, b.files.map((f, i) => el("div", {},
        span("n", i + 1), span("t clip", f.name), span("end", fmtDuration(f.durationMs), " · ", fmtBytes(f.sizeBytes)))))),
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
  FIELDS.map(([k, label]) => el("label", { class: "field" }, el("span", {}, label), inputs[k] = el("input", { type: k === "seriesIndex" ? "number" : "text", step: "any", value: initial[k] ?? "" }))),
  el("label", { class: "field wide" }, el("span", {}, "Description"), desc),
  el("p", { class: "wide small muted" }, "Saved details are kept in the server's data folder and take priority over the files. Reset brings back what the library says."),
  el("div", { class: "row wide" },
    el("button", { class: "btn danger", type: "button", onclick: () => resetMetadata(b) }, "Reset to library"),
    el("span", { class: "grow" }),
    el("button", { class: "btn secondary", type: "button", onclick: () => openBook(b.id) }, "Cancel"),
    el("button", { class: "btn", type: "submit" }, "Save")));
  return [form];
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
  if (state.view === "library" || state.view === "dashboard") render();
}

function bookMatcher(b) {
  const providers = [["audible", "Audible"], ["openlibrary", "Open Library"], ["google", "Google Books"]];
  let provider = "audible";
  const region = el("select", { "aria-label": "Audible region", style: "width:auto", onchange: () => search() },
    ["us", "uk", "ca", "au", "de", "fr", "it", "es", "in", "jp"].map((r) => el("option", { value: r }, "Region: " + r.toUpperCase())));
  const providerChips = el("div", { class: "segmented", role: "group", "aria-label": "Provider" });
  const drawProviders = () => providerChips.replaceChildren(...providers.map(([id, label]) =>
    chip(label, provider === id, () => { provider = id; region.hidden = id !== "audible"; drawProviders(); search(); })));
  drawProviders();
  const title = el("input", { type: "text", value: b.title, "aria-label": "Title", placeholder: "Title" });
  const author = el("input", { type: "text", value: (b.author || "").split(",")[0], "aria-label": "Author", placeholder: "Author" });
  const results = el("div", { class: "stack" });
  async function search(ev) {
    ev?.preventDefault();
    results.replaceChildren(el("p", { class: "muted" }, "Searching…"));
    const q = new URLSearchParams({ provider, title: title.value, author: author.value });
    if (provider === "audible") q.set("region", region.value);
    try {
      const out = await api(`/api/books/${encodeURIComponent(b.id)}/match?` + q);
      results.replaceChildren(...(out.results.length ? out.results.map((c) => matchResult(b, c)) : [el("p", { class: "muted" }, "Nothing found. Try a shorter title or another provider.")]));
    } catch (e) {
      results.replaceChildren(el("p", { class: "err" }, e.message));
    }
  }
  const form = el("form", { class: "row", onsubmit: search },
    el("div", { class: "grow" }, title), el("div", { class: "grow" }, author),
    el("button", { class: "btn", type: "submit" }, icon("search"), "Search"));
  queueMicrotask(search);
  return [
    el("div", { class: "row" }, providerChips, region),
    form,
    el("p", { class: "small muted" }, "The server asks the provider only when you search. The result you pick, and its cover, replace what the library says until you reset it."),
    results,
  ];
}

function matchResult(b, c) {
  const series = c.series ? ` · ${c.series}${c.seriesIndex != null ? " #" + c.seriesIndex : ""}` : "";
  const keepCover = el("input", { type: "checkbox", checked: !!c.coverUrl, disabled: !c.coverUrl });
  return el("div", { class: "result" },
    c.coverUrl ? el("img", { src: c.coverUrl, alt: "", loading: "lazy", referrerpolicy: "no-referrer" }) : el("div", { class: "cover blank" }, (c.title || "").slice(0, 2)),
    el("div", { style: "min-width:0" },
      el("div", { class: "strong" }, c.title, c.subtitle ? span("muted", ": " + c.subtitle) : null),
      el("div", { class: "small" }, (c.authors || []).join(", "), c.narrators?.length ? " · read by " + c.narrators.join(", ") : ""),
      el("div", { class: "small muted" }, [c.publishedYear, c.publisher].filter(Boolean).join(", "), series, c.durationMs ? " · " + fmtDuration(c.durationMs) : "")),
    el("div", { class: "row" },
      c.coverUrl ? el("label", { class: "check small" }, keepCover, "Cover") : null,
      el("button", { class: "btn small", type: "button", onclick: () => {
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

  const days = new Map(st.days.map((d) => [d.date, d.listenedMs]));
  const series = [];
  for (let i = 29; i >= 0; i--) {
    const d = new Date(Date.now() - i * 86400000 + tz * 60000).toISOString().slice(0, 10);
    series.push([d, days.get(d) || 0, i === 0]);
  }
  const peak = Math.max(1, ...series.map(([, ms]) => ms));
  const month = series.reduce((n, [, ms]) => n + ms, 0);
  const activeDays = series.filter(([, ms]) => ms > 0).length;
  const avg = st.daysListened ? st.totalListenedMs / st.daysListened : 0;

  return el("div", { class: "widgets" },
    widget("Summary", {
      visual: [span("big", fmtDuration(month)), span("tiny muted", "last 30 days")],
      overview: kv([
        ["All time", fmtDuration(st.totalListenedMs)], ["Days with listening", `${activeDays} of 30`],
        ["Current streak", plural(st.currentStreakDays, "day")], ["Longest streak", plural(st.longestStreakDays, "day")],
        ["Average per day", fmtDuration(avg)], ["Books started", st.booksStarted], ["Books finished", st.booksFinished],
      ]),
    }),
    widget("Last 30 Days", {
      wide: true,
      body: [
        el("div", { class: "bars tall", role: "img", "aria-label": "Listening per day, last 30 days" },
          series.map(([d, ms, today]) => el("div", { class: today && ms ? "today" : ms ? "on" : "zero", style: `height:${Math.max(2, (ms / peak) * 100)}%`, title: `${d}: ${fmtDuration(ms)}` }))),
        el("div", { class: "row tiny muted", style: "justify-content:space-between;margin-top:6px" }, span("", fmtDate(Date.parse(series[0][0]))), span("", "Today")),
      ],
    }),
    el("section", { class: "widget", style: "grid-column:1/-1" },
      el("div", { class: "w-head" }, el("h2", {}, "Books")),
      st.books.length ? el("table", { class: "grid" },
        el("thead", {}, el("tr", {}, el("th", { class: "c-cover" }), el("th", {}, "Title"), el("th", { class: "c-num" }, "Listened"), el("th", { class: "hide-sm" }, "Last listened"), el("th", { class: "hide-sm" }, "Finished"))),
        el("tbody", {}, [...st.books].sort((a, b) => b.listenedMs - a.listenedMs).map((r) => {
          const b = byId.get(r.bookId);
          return el("tr", { onclick: () => b && openBook(b.id) },
            el("td", { class: "c-cover" }, b ? cover(b) : null),
            el("td", {}, b?.title || span("muted", "A book no longer in your library")),
            el("td", { class: "c-num" }, fmtDuration(r.listenedMs)),
            el("td", { class: "hide-sm muted" }, relative(r.lastListenedAt)),
            el("td", { class: "hide-sm muted" }, fmtDate(r.finishedAt) || "—"));
        }))) : el("p", { class: "muted", style: "padding:14px" }, "Nothing yet. Listening time appears here as the apps sync."),
      el("p", { class: "tiny muted", style: "padding:10px 14px" }, "Worked out from the apps' progress syncs, so these figures are an estimate and lean low.")));
}

// ------------------------------------------------------------------ devices

function deviceIcon(name = "") {
  const n = name.toLowerCase();
  if (n.startsWith("web")) return "web";
  if (/ipad|tab|fire/.test(n)) return "tablet";
  if (/mac|book|pc|desktop|laptop|windows|linux/.test(n)) return "computer";
  return "phone";
}

async function viewDevices(tools, who = state.me.name) {
  const q = who === state.me.name ? "" : "?user=" + encodeURIComponent(who);
  const { devices } = await api("/api/devices" + q);
  const out = [];
  if (isAdmin()) {
    const { users } = await api("/api/users");
    if (users.length > 1) {
      out.push(el("div", { class: "toolbar" }, el("select", { "aria-label": "User", style: "width:auto", onchange: (e) => refreshMain((t) => viewDevices(t, e.target.value)) },
        users.map((u) => el("option", { value: u.name, selected: u.name === who ? true : null }, "User: " + u.name)))));
    }
  }
  const revoke = async (d) => {
    if (!confirm(d.current ? "Sign out this browser?" : `Sign out “${d.name || "this device"}”? It will ask for the password again.`)) return;
    try {
      await api(`/api/devices/${encodeURIComponent(d.id)}${q}`, { method: "DELETE" });
      if (d.current) { forgetToken(); renderSignIn(); return; }
      toast("Signed out");
      refreshMain((t) => viewDevices(t, who));
    } catch (e) { toast(e.message); }
  };
  out.push(el("div", { class: "table-wrap" }, devices.length ? el("table", { class: "grid" },
    el("thead", {}, el("tr", {}, el("th", { style: "width:44px" }), el("th", {}, "Device"), el("th", { class: "hide-sm" }, "ID"), el("th", {}, "Signed in"), el("th", {}, "Last seen"), el("th", {}))),
    el("tbody", {}, devices.map((d) => el("tr", { style: "cursor:default" },
      el("td", { class: "muted" }, icon(deviceIcon(d.name))),
      el("td", {}, d.name || span("muted", "Unnamed device"), d.current ? el("span", { class: "badge" }, "This browser") : null),
      el("td", { class: "hide-sm mono muted" }, d.id),
      el("td", { class: "muted" }, fmtDate(d.createdAt)),
      el("td", { class: "muted" }, relative(d.lastSeenAt)),
      el("td", { class: "c-num" }, el("button", { class: "btn danger small", type: "button", onclick: () => revoke(d) }, "Sign out")))))) :
    el("p", { class: "muted", style: "padding:24px;text-align:center" }, "No devices signed in.")));
  out.push(el("p", { class: "tiny muted", style: "margin-top:10px" }, "Signing a device out makes it ask for the password again. Changing a password signs out every device. Last-seen times update about once an hour."));
  return out;
}

// ------------------------------------------------------------------ libraries (admin)

async function viewLibraries(tools) {
  const { libraries: libs } = await api("/api/libraries");
  tools.append(el("button", { class: "btn small", type: "button", onclick: () => addLibrarySheet() }, icon("add"), "Add library"));
  const remove = async (l) => {
    if (!confirm(`Remove ${l.name}? Its books disappear from the server and every device. The files stay where they are, and adding the folder back brings the books and everyone's places back.`)) return;
    try {
      await api("/api/libraries/" + encodeURIComponent(l.name), { method: "DELETE" });
      toast(`Removed ${l.name}`);
      render();
    } catch (e) { toast(e.message); }
  };
  return [
    el("div", { class: "table-wrap" }, libs.length ? el("table", { class: "grid" },
      el("thead", {}, el("tr", {}, el("th", {}, "Name"), el("th", {}, "Status"), el("th", { class: "hide-sm" }, "Path"), el("th", { class: "c-num" }, "Books"), el("th", { class: "c-num" }, "Length"), el("th", { class: "hide-sm" }, "Source"), el("th", {}))),
      el("tbody", {}, libs.map((l) => el("tr", { style: "cursor:default" },
        el("td", {}, el("span", { class: "row", style: "flex-wrap:nowrap" }, el("span", { class: "gold" }, icon("folder")), span("strong", l.name))),
        el("td", {}, l.error ? span("status err", "Unreadable") : span("status ok", "Online"), l.error ? el("div", { class: "tiny err" }, l.error) : null),
        el("td", { class: "hide-sm mono muted" }, l.path),
        el("td", { class: "c-num" }, l.books),
        el("td", { class: "c-num" }, fmtDuration(l.durationMs)),
        el("td", { class: "hide-sm muted" }, l.fixed ? "Command line" : "Added here"),
        el("td", { class: "c-num" }, l.fixed ? null : el("button", { class: "btn danger small", type: "button", onclick: () => remove(l) }, "Remove")))))) :
      el("div", { style: "padding:24px;text-align:center" }, el("p", { class: "muted" }, "No libraries yet."),
        el("div", { style: "margin-top:12px" }, el("button", { class: "btn", type: "button", onclick: () => addLibrarySheet() }, icon("add"), "Add library")))),
    el("p", { class: "tiny muted", style: "margin-top:10px" },
      "A library is a folder of audiobooks the server can see. Each book is a folder of audio files or a single file; any nesting above that is fine. Running in Docker? Only folders mounted into the container can be added. On the command line: kithara-server library add NAME DIR."),
  ];
}

function addLibrarySheet() {
  const box = el("div");
  openSheet("Add Library", [box]);
  showPicker(box, "");
}

// A folder picker over the server's own disk (admins only; it lists folder names).
async function showPicker(box, path) {
  let here;
  try { here = await api("/api/fs" + (path ? "?path=" + encodeURIComponent(path) : "")); }
  catch (e) { toast(e.message); return; }
  const name = el("input", { type: "text", value: here.path.split(here.separator).filter(Boolean).pop() || "", placeholder: "Library name" });
  const typed = el("input", { type: "text", class: "mono", value: here.path, "aria-label": "Folder path",
    onkeydown: (e) => { if (e.key === "Enter") { e.preventDefault(); showPicker(box, typed.value.trim()); } } });
  const add = el("button", { class: "btn", type: "button", onclick: async () => {
    add.disabled = true;
    try {
      await api("/api/libraries", { method: "POST", body: { name: name.value.trim(), path: here.path } });
      toast(`Added ${name.value.trim() || here.path}`);
      dialog.close();
      render();
    } catch (e) { toast(e.message); add.disabled = false; }
  } }, icon("add"), "Add this folder");
  box.replaceChildren(el("div", { class: "stack" },
    el("div", { class: "picker" },
      el("div", { class: "here" },
        el("button", { class: "icon-btn", type: "button", title: "Up one folder", "aria-label": "Up one folder", disabled: here.parent ? null : true, onclick: () => showPicker(box, here.parent) }, icon("up")),
        el("div", { class: "grow" }, typed)),
      el("div", { class: "list" },
        here.dirs.length ? here.dirs.map((d) => el("button", { class: "dir", type: "button", onclick: () => showPicker(box, d.path) },
          icon("folder"), span("grow clip", d.name))) :
          el("p", { class: "muted", style: "padding:12px" }, "No folders in here."))),
    el("p", { class: "small muted" }, here.audio ? `${plural(here.audio, "audio file")} directly in this folder. If this is one book, pick the folder above it.` : "Pick the folder that holds your audiobooks."),
    el("label", { class: "field" }, el("span", {}, "Name"), name),
    el("div", { class: "row end" }, el("button", { class: "btn secondary", type: "button", onclick: () => dialog.close() }, "Cancel"), add)));
}

// ------------------------------------------------------------------ users (admin)

async function viewUsers(tools) {
  const [{ users }, { libraries }] = await Promise.all([api("/api/users"), api("/api/libraries")]);
  tools.append(el("button", { class: "btn small", type: "button", onclick: () => userSheet(null, libraries) }, icon("add"), "Add user"));
  return [
    el("div", { class: "table-wrap" }, el("table", { class: "grid" },
      el("thead", {}, el("tr", {}, el("th", { style: "width:52px" }), el("th", {}, "Username"), el("th", {}, "Role"), el("th", { class: "hide-sm" }, "Libraries"), el("th", { class: "c-num" }, "Devices"), el("th", {}))),
      el("tbody", {}, users.map((u) => el("tr", { onclick: () => userSheet(u, libraries) },
        el("td", {}, el("span", { class: "avatar", style: "width:30px;height:30px;font-size:13px" }, u.name.slice(0, 1))),
        el("td", {}, span("strong", u.name), u.name === state.me.name ? el("span", { class: "badge plain" }, "You") : null),
        el("td", {}, u.role === "admin" ? span("gold", "Administrator") : "Listener"),
        el("td", { class: "hide-sm muted" }, u.role === "admin" ? "All" : u.libraries?.length ? u.libraries.join(", ") : "All"),
        el("td", { class: "c-num" }, u.devices),
        el("td", { class: "c-num" }, el("button", { class: "btn secondary small", type: "button" }, icon("edit"), "Edit"))))))),
    el("p", { class: "tiny muted", style: "margin-top:10px" }, "Listeners see only the libraries chosen for them, on every device. Administrators can add libraries, rescan, edit book details and manage users."),
  ];
}

// Add or edit a user, in a sheet.
function userSheet(u, libraries) {
  const editing = !!u;
  const chosen = new Set((u?.libraries || []).map((s) => s.toLowerCase()));
  let role = u?.role || "user";
  const libChips = el("div", { class: "chips" });
  const drawLibs = () => libChips.replaceChildren(
    chip("All libraries", !chosen.size, () => { chosen.clear(); drawLibs(); }),
    ...libraries.map((l) => {
      const key = l.name.toLowerCase();
      return chip(l.name, chosen.has(key), () => { chosen.has(key) ? chosen.delete(key) : chosen.add(key); drawLibs(); });
    }));
  drawLibs();
  const libsRow = el("div", { class: "wide", hidden: role === "admin" || libraries.length < 2 ? true : null }, el("div", { class: "small muted", style: "margin-bottom:6px" }, "Can see"), libChips);
  const roleBox = el("div", { class: "segmented" });
  const drawRole = () => roleBox.replaceChildren(...[["user", "Listener"], ["admin", "Administrator"]].map(([id, label]) =>
    chip(label, role === id, () => { role = id; drawRole(); libsRow.hidden = role === "admin" || libraries.length < 2; })));
  drawRole();
  const name = el("input", { type: "text", required: !editing, autocomplete: "off", value: u?.name || "", disabled: editing ? true : null, pattern: "[A-Za-z0-9._@\\-]{1,64}" });
  const pass = el("input", { type: "password", required: !editing, minlength: 8, autocomplete: "new-password", placeholder: editing ? "Leave empty to keep" : "" });
  const libsValue = () => libraries.filter((l) => chosen.has(l.name.toLowerCase())).map((l) => l.name);

  const form = el("form", { class: "form", onsubmit: async (ev) => {
    ev.preventDefault();
    try {
      if (editing) {
        const body = { role, libraries: libsValue() };
        if (pass.value) body.password = pass.value;
        await api("/api/users/" + encodeURIComponent(u.name), { method: "PATCH", body });
        if (u.name === state.me.name) state.me = await api("/api/me");
      } else {
        await api("/api/users", { method: "POST", body: { username: name.value, password: pass.value, role, libraries: libsValue() } });
      }
      toast(editing ? "Saved " + u.name : "Added " + name.value);
      dialog.close();
      render();
    } catch (e) { toast(e.message); }
  } },
  el("label", { class: "field" }, el("span", {}, "Username"), name),
  el("label", { class: "field" }, el("span", {}, editing ? "New password (signs out their devices)" : "Password (8 or more characters)"), pass),
  el("div", { class: "wide" }, el("div", { class: "small muted", style: "margin-bottom:6px" }, "Role"), roleBox),
  libsRow,
  el("div", { class: "row wide" },
    editing && u.name !== state.me.name ? el("button", { class: "btn danger", type: "button", onclick: async () => {
      if (!confirm(`Remove ${u.name}? Their devices are signed out; their listening history is kept aside on the server.`)) return;
      try { await api("/api/users/" + encodeURIComponent(u.name), { method: "DELETE" }); toast("Removed " + u.name); dialog.close(); render(); }
      catch (e) { toast(e.message); }
    } }, icon("remove"), "Remove user") : null,
    el("span", { class: "grow" }),
    el("button", { class: "btn secondary", type: "button", onclick: () => dialog.close() }, "Cancel"),
    el("button", { class: "btn", type: "submit" }, editing ? "Save" : "Add user")));
  openSheet(editing ? "Edit User · " + u.name : "Add User", [form]);
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
  applyTheme();
  try { state.server = await api("/api/health"); } catch { /* shown as "Kithara" */ }
  document.title = state.server?.name ? `${state.server.name} · Kithara` : "Kithara";
  state.token = loadToken();
  if (!state.token) return renderSignIn();
  try { await start(); } catch (e) { if (e.status !== 401) renderSignIn(e.message); }
})();
