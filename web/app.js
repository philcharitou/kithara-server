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
  prefs: { layout: "table", sort: "title", filter: "all", library: "", theme: "dark", group: "books" },
  loadedAt: 0, // when the catalogue was last fetched
  gen: 0, // bumped when the user changes; answers for an older one are dropped
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
  down: "M20 12l-1.41-1.41L13 16.17V4h-2v12.17l-5.58-5.59L4 12l8 8 8-8z",
  notes: "M14 17H4v2h10v-2zm6-8H4v2h16V9zM4 15h16v-2H4v2zM4 5v2h16V5H4z",
  queue: "M3 10h11v2H3zm0-4h11v2H3zm0 8h7v2H3zm13-1v8l6-4z",
  up: "M4 12l1.41 1.41L11 7.83V20h2V7.83l5.58 5.59L20 12l-8-8-8 8z",
  phone: "M17 1.01L7 1c-1.1 0-2 .9-2 2v18c0 1.1.9 2 2 2h10c1.1 0 2-.9 2-2V3c0-1.1-.9-1.99-2-1.99zM17 19H7V5h10v14z",
  tablet: "M21 4H3c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h18c1.1 0 1.99-.9 1.99-2L23 6c0-1.1-.9-2-2-2zm-2 14H5V6h14v12z",
  computer: "M20 18c1.1 0 1.99-.9 1.99-2L22 6c0-1.1-.9-2-2-2H4c-1.1 0-2 .9-2 2v10c0 1.1.9 2 2 2H0v2h24v-2h-4zM4 6h16v10H4V6z",
  web: "M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-1 17.93c-3.95-.49-7-3.85-7-7.93 0-.62.08-1.21.21-1.79L9 15v1c0 1.1.9 2 2 2v1.93zm6.9-2.54c-.26-.81-1-1.39-1.9-1.39h-1v-3c0-.55-.45-1-1-1H8v-2h2c.55 0 1-.45 1-1V7h2c1.1 0 2-.9 2-2v-.41c2.93 1.19 5 4.06 5 7.41 0 2.08-.8 3.97-2.1 5.39z",
  play: "M8 5v14l11-7z",
  pause: "M6 19h4V5H6v14zm8-14v14h4V5h-4z",
  replay: "M11.99 5V1l-5 5 5 5V7c3.31 0 6 2.69 6 6s-2.69 6-6 6-6-2.69-6-6h-2c0 4.42 3.58 8 8 8s8-3.58 8-8-3.58-8-8-8z",
  forward: "M18 13c0 3.31-2.69 6-6 6s-6-2.69-6-6 2.69-6 6-6v4l5-5-5-5v4c-4.42 0-8 3.58-8 8s3.58 8 8 8 8-3.58 8-8h-2z",
  prev: "M6 6h2v12H6zm3.5 6l8.5 6V6z",
  next: "M6 18l8.5-6L6 6v12zM16 6v12h2V6h-2z",
  bookmark: "M17 3H7c-1.1 0-1.99.9-1.99 2L5 21l7-3 7 3V5c0-1.1-.9-2-2-2z",
  timer: "M15 1H9v2h6V1zm-4 13h2V8h-2v6zm8.03-6.61l1.42-1.42c-.43-.51-.9-.99-1.41-1.41l-1.42 1.42A8.962 8.962 0 0012 4c-4.97 0-9 4.03-9 9s4.02 9 9 9 9-4.03 9-9c0-2.12-.74-4.07-1.97-5.61zM12 20c-3.87 0-7-3.13-7-7s3.13-7 7-7 7 3.13 7 7-3.13 7-7 7z",
  person: "M12 12c2.21 0 4-1.79 4-4s-1.79-4-4-4-4 1.79-4 4 1.79 4 4 4zm0 2c-2.67 0-8 1.34-8 4v2h16v-2c0-2.66-5.33-4-8-4z",
  build: "M22.7 19l-9.1-9.1c.9-2.3.4-5-1.5-6.9-2-2-5-2.4-7.4-1.3L9 6 6 9 1.6 4.7C.4 7.1.9 10.1 2.9 12.1c1.9 1.9 4.6 2.4 6.9 1.5l9.1 9.1c.4.4 1 .4 1.4 0l2.3-2.3c.5-.4.5-1.1.1-1.4z",
  history: "M13 3a9 9 0 00-9 9H1l3.89 3.89.07.14L9 12H6c0-3.87 3.13-7 7-7s7 3.13 7 7-3.13 7-7 7c-1.93 0-3.68-.79-4.94-2.06l-1.42 1.42A8.954 8.954 0 0013 21a9 9 0 000-18zm-1 5v5l4.28 2.54.72-1.21-3.5-2.08V8H12z",
  settings: "M19.14 12.94c.04-.3.06-.61.06-.94 0-.32-.02-.64-.07-.94l2.03-1.58a.49.49 0 00.12-.61l-1.92-3.32a.488.488 0 00-.59-.22l-2.39.96c-.5-.38-1.03-.7-1.62-.94l-.36-2.54a.484.484 0 00-.48-.41h-3.84c-.24 0-.43.17-.47.41l-.36 2.54c-.59.24-1.13.57-1.62.94l-2.39-.96c-.22-.08-.47 0-.59.22L2.74 8.87c-.12.21-.08.47.12.61l2.03 1.58c-.05.3-.09.63-.09.94s.02.64.07.94l-2.03 1.58a.49.49 0 00-.12.61l1.92 3.32c.12.22.37.29.59.22l2.39-.96c.5.38 1.03.7 1.62.94l.36 2.54c.05.24.24.41.48.41h3.84c.24 0 .44-.17.47-.41l.36-2.54c.59-.24 1.13-.56 1.62-.94l2.39.96c.22.08.47 0 .59-.22l1.92-3.32c.12-.22.07-.47-.12-.61l-2.01-1.58zM12 15.6c-1.98 0-3.6-1.62-3.6-3.6s1.62-3.6 3.6-3.6 3.6 1.62 3.6 3.6-1.62 3.6-3.6 3.6z",
  upload: "M9 16h6v-6h4l-7-7-7 7h4zm-4 2h14v2H5z",
  image: "M21 19V5c0-1.1-.9-2-2-2H5c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2zM8.5 13.5l2.5 3.01L14.5 12l4.5 6H5l3.5-4.5z",
  warning: "M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z",
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

// Held on to: an open sheet borrows it (see below), and reopening a sheet would
// otherwise take it out of the page.
const toastEl = document.getElementById("toast");
function toast(msg) {
  const t = toastEl;
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

// resetCaches forgets what was loaded for the signed-in user, so the next one (after a
// sign-out, or signing in as someone else) never sees it.
function resetCaches() {
  state.gen++;
  state.books = [];
  state.libraries = [];
  state.progress = new Map();
  state.loadedAt = 0;
  bookmarkCache = null;
  for (const url of coverCache.values()) URL.revokeObjectURL(url);
  coverCache.clear();
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
  resetCaches();
  try { sessionStorage.removeItem(TOKEN_KEY); storage()?.removeItem(TOKEN_KEY); } catch { /* nothing kept */ }
}

class ApiError extends Error {
  constructor(status, message) { super(message); this.status = status; }
}

// api calls the server with the token. body is sent as JSON, blob (a file) as it is.
async function api(path, { method = "GET", body, blob, raw = false } = {}) {
  const headers = {};
  if (state.token) headers.Authorization = "Bearer " + state.token;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(path, { method, headers, body: blob ?? (body === undefined ? undefined : JSON.stringify(body)) });
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
const seriesLabel = (x) => (x.series ? x.series + (x.seriesIndex != null ? " #" + x.seriesIndex : "") : "");
const firstAuthor = (x) => (x.author || "").split(",")[0].trim();
const emptyNote = (msg) => el("p", { class: "muted empty-note" }, msg);
const PROVIDERS = [["audible", "Audible"], ["openlibrary", "Open Library"], ["google", "Google Books"]];

// clickDownload saves what href points at, as the browser's own download.
function clickDownload(href, name = "") {
  const a = el("a", { href, download: name });
  document.body.append(a); a.click(); a.remove();
}
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

const COVERS_KEPT = 300; // decoded covers held at once; the oldest are let go

async function fillCover(box) {
  const url = box.dataset.src;
  try {
    let blobURL = coverCache.get(url);
    if (blobURL) {
      coverCache.delete(url); // most recently used goes last
    } else {
      const res = await api(url, { raw: true });
      if (!res.ok) return;
      blobURL = URL.createObjectURL(await res.blob());
    }
    coverCache.set(url, blobURL);
    while (coverCache.size > COVERS_KEPT) {
      const [oldest, old] = coverCache.entries().next().value;
      coverCache.delete(oldest);
      URL.revokeObjectURL(old); // pictures already on screen stay drawn
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
        resetCaches();
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
  const sso = el("div");
  app.replaceChildren(el("div", { class: "signin" }, el("div", { class: "box" },
    el("div", { class: "box-head" }, el("img", { src: "logo.svg", alt: "" }),
      el("div", {}, el("div", { class: "strong", style: "font-size:16px" }, "Kithara"), el("div", { class: "small muted" }, state.server?.name || "Audiobook server"))),
    form)));
  form.prepend(sso);
  api("/api/oidc").then((o) => {
    if (!o.enabled) return;
    sso.append(
      el("button", { class: "btn secondary wide-btn", type: "button", onclick: () => ssoStart(remember.checked).catch((e) => { err.textContent = e.message; }) }, icon("person"), o.buttonText),
      el("div", { class: "or tiny muted" }, "or with a password"));
  }).catch(() => { /* no button */ });
}

// Single sign-on: the server sends the browser to the identity provider and back here
// with a one-time code in the fragment, which is traded for a token along with the
// PKCE verifier kept for this tab.
const SSO_KEY = "kithara.sso";
const b64url = (bytes) => btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

async function ssoStart(remember, link = "") {
  if (!crypto.subtle) throw new Error("Single sign-on needs this page opened over https.");
  const verifier = b64url(crypto.getRandomValues(new Uint8Array(32)));
  const challenge = b64url(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier))));
  sessionStorage.setItem(SSO_KEY, JSON.stringify({ verifier, remember }));
  const q = new URLSearchParams({ challenge, device: browserName() });
  if (link) q.set("link", link);
  location.href = "/api/oidc/start?" + q;
}

// ssoReturn finishes a sign-in the provider sent back, if this load is one. It returns
// whether it was.
async function ssoReturn() {
  const h = new URLSearchParams(location.hash.slice(1));
  if (!h.has("code") && !h.has("error")) return false;
  history.replaceState(null, "", location.pathname);
  let saved = null;
  try { saved = JSON.parse(sessionStorage.getItem(SSO_KEY)); sessionStorage.removeItem(SSO_KEY); } catch { /* none */ }
  if (h.has("error") || !saved) {
    renderSignIn(h.get("error") || "That sign-in was started in another tab. Try again.");
    return true;
  }
  try {
    const out = await api("/api/oidc/exchange", { method: "POST", body: { code: h.get("code"), verifier: saved.verifier, device: browserName() } });
    resetCaches();
    saveToken(out.token, saved.remember);
    await start();
  } catch (e) {
    if (e.status !== 401 || !state.token) renderSignIn(e.status === 401 ? "That sign-in expired. Try again." : e.message);
  }
  return true;
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
  { id: "lists", label: "Collections", icon: "queue" },
  { id: "stats", label: "Listening", icon: "stats" },
  { id: "devices", label: "Devices", icon: "devices" },
  { id: "account", label: "Account", icon: "person" },
  { id: "libraries", label: "Libraries", icon: "folder", admin: true },
  { id: "maintenance", label: "Maintenance", icon: "build", admin: true },
  { id: "activity", label: "Activity", icon: "history", admin: true },
  { id: "users", label: "Users", icon: "users", admin: true },
  { id: "settings", label: "Settings", icon: "settings", admin: true },
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
    txChip(),
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
  stopPlayer();
  clearTimeout(txq.timer);
  txq.st = null;
  try { await api("/api/auth/logout", { method: "POST" }); } catch { /* signed out regardless */ }
  forgetToken();
  renderSignIn();
}

function go(view) {
  if (view !== state.view) pickEnd();
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
    const view = {
      dashboard: viewDashboard, library: viewLibrary, lists: viewLists, stats: viewStats, devices: viewDevices, account: viewAccount,
      libraries: viewLibraries, maintenance: viewMaintenance, activity: viewActivity, users: viewUsers, settings: viewSettings,
    }[current.id];
    main.replaceChildren(...[await view(tools)].flat().filter(Boolean));
  } catch (e) {
    if (e.status !== 401) main.replaceChildren(el("p", { class: "err" }, e.message));
  }
}

// rescanNow asks the server to rescan; the catalogue is fetched again afterwards.
async function rescanNow() {
  try {
    return await api("/api/rescan", { method: "POST" });
  } finally {
    state.loadedAt = 0; // even after an error: the scan may still have published
  }
}

function rescanButton() {
  const btn = el("button", { class: "btn secondary small", type: "button", onclick: async () => {
    btn.disabled = true;
    try { const r = await rescanNow(); toast(`Rescanned: ${plural(r.books, "book")}`); render(); }
    catch (e) { toast(e.message); btn.disabled = false; }
  } }, icon("refresh"), "Rescan");
  return btn;
}

async function refreshMain(view) {
  const main = document.getElementById("main");
  const tools = document.getElementById("tools");
  if (!main) return;
  tools.replaceChildren(); // the view adds its own buttons again
  main.replaceChildren(...[await view(tools)].flat().filter(Boolean));
}

// A dashboard widget: a header with actions, then a visual and an overview list, or a
// full-width body.
function widget(title, { actions = [], visual, overview, body, wide } = {}) {
  return el("section", { class: "widget" + (wide ? " wide" : "") },
    el("div", { class: "w-head" }, el("h2", { class: "clip" }, title), actions),
    visual || overview ? el("div", { class: "w-body" },
      el("div", { class: "w-visual" }, visual),
      el("div", { class: "overview" }, el("div", { class: "label" }, "Overview"), overview)) :
      el("div", { class: "w-body full" }, body));
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

// loadLibrary fetches the progress, and the catalogue when it has not been fetched in
// the last minute or something changed it (state.loadedAt = 0).
async function loadLibrary() {
  const fresh = Date.now() - state.loadedAt < 60000;
  const gen = state.gen;
  const [cat, prog, libs] = await Promise.all([fresh ? null : api("/api/library"), api("/api/progress"), fresh ? null : api("/api/libraries")]);
  if (gen !== state.gen) return; // signed out meanwhile
  if (cat) {
    state.books = cat.books;
    state.libraries = libs.libraries;
    state.loadedAt = Date.now();
  }
  state.progress = new Map(prog.progress.map((p) => [p.bookId, p]));
  if (state.prefs.library && !state.libraries.some((l) => l.name === state.prefs.library)) state.prefs.library = "";
}

function progressOf(b) {
  const p = state.progress.get(b.id);
  if (!p) return null;
  return { ...p, fraction: b.durationMs ? Math.min(1, p.positionMs / b.durationMs) : 0 };
}

function statusOf(b) {
  const p = state.progress.get(b.id);
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
  const [status, stats, { devices }] = await Promise.all([api("/api/status"), api("/api/stats?tzOffset=" + tz), api("/api/devices"), loadLibrary()]);
  const out = [];
  const byId = new Map(state.books.map((b) => [b.id, b]));
  const totalMs = state.books.reduce((n, b) => n + b.durationMs, 0);

  if (isAdmin()) {
    tools.append(rescanButton());
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

// matches reports whether a book has every word of the search (already lowercased).
// Each book's searchable text is worked out once and kept with the book object.
const hayCache = new WeakMap();
function matches(b, words) {
  if (!words.length) return true;
  let hay = hayCache.get(b);
  if (hay === undefined) {
    hay = [b.title, b.subtitle, b.author, b.narrator, b.series, ...(b.genres || [])].join(" ").toLowerCase();
    hayCache.set(b, hay);
  }
  return words.every((w) => hay.includes(w));
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });
const byTitle = (a, b) => collator.compare(a.title, b.title);
const SORTS = {
  title: ["Title", byTitle],
  author: ["Author", (a, b) => collator.compare(a.author || "~", b.author || "~") || byTitle(a, b)],
  series: ["Series", (a, b) => collator.compare(a.series || "~", b.series || "~") || (a.seriesIndex ?? 1e9) - (b.seriesIndex ?? 1e9) || byTitle(a, b)],
  recent: ["Recently played", (a, b) => (state.progress.get(b.id)?.updatedAt || 0) - (state.progress.get(a.id)?.updatedAt || 0) || byTitle(a, b)],
  length: ["Length", (a, b) => b.durationMs - a.durationMs],
  updated: ["Recently changed", (a, b) => b.updatedAt - a.updatedAt],
};

const PAGE = 300; // books drawn at a time; more on request
let searchTimer = 0;

async function viewLibrary(tools) {
  await loadLibrary();
  pick.on = true;
  pick.listeners = [];
  const prefs = state.prefs;
  tools.append(el("button", { class: "btn secondary small", type: "button", onclick: searchTranscriptsSheet }, icon("search"), "Search transcripts"));
  if (isAdmin()) {
    tools.append(rescanButton());
  }
  if (!state.libraries.length) {
    return el("div", { class: "widget" }, el("div", { class: "w-body full" },
      el("p", { class: "muted" }, isAdmin() ? "No libraries yet. Add a folder of audiobooks and they will show up here and in the apps." : "Whoever runs this server hasn't added a library yet."),
      isAdmin() ? el("div", { style: "margin-top:12px" }, el("button", { class: "btn", type: "button", onclick: () => go("libraries") }, icon("add"), "Add a library")) : null));
  }

  const shelf = el("div");
  const count = span("small muted");
  let limit = PAGE;
  let fill = () => {
    const words = state.query.toLowerCase().split(/\s+/).filter(Boolean);
    shelf.querySelectorAll(".cover[data-src]").forEach((box) => coverObserver.unobserve(box)); // the old rows are going
    const shown = state.books
      .filter((b) => !prefs.library || b.library === prefs.library)
      .filter((b) => prefs.filter === "all" || statusOf(b) === prefs.filter)
      .filter((b) => matches(b, words))
      .sort((SORTS[prefs.sort] || SORTS.title)[1]);
    pick.shown = shown;
    count.textContent = shown.length === state.books.length ? plural(shown.length, "book") : `${shown.length} of ${state.books.length} books`;
    if (!shown.length) {
      shelf.replaceChildren(el("div", { class: "table-wrap" }, emptyNote("Nothing matches.")));
      return;
    }
    if (prefs.group === "series" || prefs.group === "authors") return shelf.replaceChildren(groupedShelf(shown, prefs.group));
    const page = shown.slice(0, limit);
    shelf.replaceChildren(...[prefs.layout === "grid" ? el("div", { class: "cover-grid" }, page.map(bookTile)) : bookTable(page),
      shown.length > limit ? el("div", { class: "row", style: "justify-content:center;margin-top:12px" },
        el("button", { class: "btn secondary", type: "button", onclick: () => { limit += PAGE; fill(); } }, `Show ${Math.min(PAGE, shown.length - limit)} more of ${shown.length - limit}`)) : null].filter(Boolean));
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

  const groupBox = el("div", { class: "segmented", role: "group", "aria-label": "Browse by" });
  const drawGroup = () => groupBox.replaceChildren(...[["books", "Books"], ["series", "Series"], ["authors", "Authors"]].map(([id, label]) =>
    chip(label, prefs.group === id, () => { prefs.group = id; savePrefs(); drawGroup(); layoutBtns.forEach((b) => { b.hidden = id !== "books"; }); fill(); })));
  drawGroup();
  layoutBtns.forEach((b) => { b.hidden = prefs.group !== "books"; });

  pick.refill = fill;
  // The table has its own select-all box in its header; covers and groups use this one.
  const allLabel = el("label", { class: "check small" }, pickAllBox(fill), "Select all");
  const fillAll = fill;
  fill = () => { fillAll(); allLabel.hidden = prefs.layout !== "grid" && prefs.group === "books"; };
  pick.refill = fill;
  fill();
  return [
    el("div", { class: "toolbar" },
      groupBox,
      el("div", { class: "search" }, icon("search"), el("input", { type: "search", placeholder: "Search title, author, narrator, series", "aria-label": "Search", value: state.query,
        oninput: (e) => { state.query = e.target.value; limit = PAGE; clearTimeout(searchTimer); searchTimer = setTimeout(fill, 150); } })),
      libSelect, sortSelect, layoutBtns),
    txBanner(),
    el("div", { class: "toolbar" }, filters, el("span", { class: "grow" }), allLabel, count),
    bulkBar(fill),
    shelf,
  ].filter(Boolean);
}

function bookTable(books) {
  const multi = state.libraries.length > 1;
  return el("div", { class: "table-wrap" }, el("table", { class: "grid" },
    el("thead", {}, el("tr", {},
      pick.on ? el("th", { class: "c-pick" }, pickAllBox(pick.refill)) : null, el("th", { class: "c-cover" }), el("th", {}, "Title"), el("th", { class: "hide-sm" }, "Author"), el("th", { class: "hide-sm" }, "Narrator"),
      el("th", { class: "hide-sm" }, "Series"), multi ? el("th", { class: "hide-sm" }, "Library") : null,
      el("th", { class: "c-num" }, "Length"), el("th", { class: "c-prog" }, "Progress"))),
    el("tbody", {}, books.map((b) => {
      const p = progressOf(b);
      const row = el("tr", { tabindex: 0, class: pick.on && pick.ids.has(b.id) ? "picked" : null, onclick: () => openBook(b.id),
        onkeydown: (e) => {
          if (e.key === "Enter") openBook(b.id);
          else if (pick.on && e.key === " ") { e.preventDefault(); pickToggle(b.id, row); }
        } },
        pick.on ? el("td", { class: "c-pick", onclick: (e) => { e.stopPropagation(); if (e.target.tagName !== "INPUT") pickToggle(b.id, row); } },
          el("input", { type: "checkbox", tabindex: -1, "aria-label": "Select " + b.title, checked: pick.ids.has(b.id) ? true : null,
            onchange: () => pickToggle(b.id, row, true) })) : null,
        el("td", { class: "c-cover" }, cover(b)),
        el("td", {}, el("div", { class: "strong clip2" }, b.title), b.subtitle ? el("div", { class: "small muted clip" }, b.subtitle) : null, txMark(b.id)),
        el("td", { class: "hide-sm" }, b.author || span("muted", "—")),
        el("td", { class: "hide-sm" }, b.narrator || span("muted", "—")),
        el("td", { class: "hide-sm" }, seriesLabel(b) || span("muted", "—")),
        multi ? el("td", { class: "hide-sm muted" }, b.library) : null,
        el("td", { class: "c-num" }, fmtDuration(b.durationMs)),
        el("td", { class: "c-prog" }, p && !p.finished && p.positionMs > 0 ? gradientProgress(p.fraction) : null, statusText(b)));
      return row;
    }))));
}

function bookTile(b) {
  const p = progressOf(b);
  const tile = el("button", { class: "tile", type: "button", onclick: () => openBook(b.id) },
    cover(b),
    p && !p.finished && p.positionMs > 0 ? gradientProgress(p.fraction) : null,
    el("div", { class: "strong clip2" }, b.title),
    el("div", { class: "small muted clip" }, b.author || "Unknown author"),
    statusText(b),
    txMark(b.id));
  if (!pick.on) return tile;
  const wrap = el("div", { class: "tile-wrap" + (pick.ids.has(b.id) ? " picked" : "") }, tile,
    el("input", { type: "checkbox", class: "tile-pick", "aria-label": "Select " + b.title, checked: pick.ids.has(b.id) ? true : null,
      onchange: () => pickToggle(b.id, wrap, true) }));
  return wrap;
}

// ------------------------------------------------------------------ picking books

// Picking books in the library: every row and cover has a checkbox, and once any is
// ticked a bar offers what can be done to all of them at once. listeners are what to
// update when the selection changes (the bar, the select-all boxes).
const pick = { on: false, ids: new Set(), shown: [], listeners: [], refill: () => {} };
// A listener answers false once its element has gone, and is dropped.
const pickChanged = () => { pick.listeners = pick.listeners.filter((f) => f() !== false); };

function pickEnd() {
  pick.on = false;
  pick.ids.clear();
}

// pickToggle flips a book; fromBox when its own checkbox already shows the new state.
function pickToggle(id, node, fromBox = false) {
  if (pick.ids.has(id)) pick.ids.delete(id); else pick.ids.add(id);
  const on = pick.ids.has(id);
  node?.classList.toggle("picked", on);
  const box = node?.querySelector("input[type=checkbox]");
  if (box && !fromBox) box.checked = on;
  pickChanged();
}

// pickAllBox ticks every book shown (after filters and search), or none; it shows a
// dash when only some are.
function pickAllBox(refill) {
  let attached = false;
  const box = el("input", { type: "checkbox", "aria-label": "Select all shown", onchange: () => {
    if (box.checked) pick.shown.forEach((b) => pick.ids.add(b.id));
    else pick.shown.forEach((b) => pick.ids.delete(b.id));
    refill();
    pickChanged();
  } });
  const sync = () => {
    if (attached && !box.isConnected) return false;
    attached = box.isConnected;
    const n = pick.shown.filter((b) => pick.ids.has(b.id)).length;
    box.checked = n > 0 && n === pick.shown.length;
    box.indeterminate = n > 0 && n < pick.shown.length;
  };
  pick.listeners.push(sync);
  sync();
  return box;
}

const pickedBooks = () => state.books.filter((b) => pick.ids.has(b.id));

function bulkBar(refill) {
  const count = span("strong", "");
  const actions = el("div", { class: "row" });
  const run = (label, fn) => el("button", { class: "btn secondary small", type: "button", onclick: async (ev) => {
    const btn = ev.currentTarget;
    const books = pickedBooks();
    if (!books.length) return toast("Select some books first");
    btn.disabled = true;
    try { await fn(books); } catch (e) { toast(e.message); }
    btn.disabled = false;
  } }, label);
  const progressAll = (finished) => async (books) => {
    for (const b of books) {
      const p = await api(`/api/progress/${encodeURIComponent(b.id)}`, { method: "PUT", body: { positionMs: finished ? b.durationMs : 0, updatedAt: Date.now(), finished } });
      state.progress.set(b.id, p);
    }
    toast(finished ? `Marked ${plural(books.length, "book")} finished` : `${plural(books.length, "book")} back to the start`);
    refill();
  };
  actions.append(
    run([icon("queue"), "Add to a list"], (books) => addManyToList(books)),
    run([icon("check"), "Mark finished"], progressAll(true)),
    run([icon("replay"), "Mark not started"], progressAll(false)));
  api("/api/transcription").then((st) => {
    if (!st.available) return;
    actions.append(run([icon("notes"), "Transcribe"], async (books) => {
      let n = 0;
      for (const b of books) n += (await api(`/api/books/${encodeURIComponent(b.id)}/transcribe`, { method: "POST", body: {} })).queued;
      toast(n ? `Queued ${plural(n, "chapter")}` : "Nothing left to transcribe");
      txWatch();
    }));
  }).catch(() => {});
  if (isAdmin()) actions.prepend(run([icon("magic"), "Find details"], (books) => openSheet(`Find details · ${plural(books.length, "book")}`, bulkMatch(books), { wide: true })));
  const bar = el("div", { class: "toolbar bulk-bar" },
    count,
    el("button", { class: "btn ghost small", type: "button", onclick: () => { pick.ids.clear(); refill(); pickChanged(); } }, "Clear"),
    el("span", { class: "grow" }),
    actions);
  const draw = () => { count.textContent = `${pick.ids.size} selected`; bar.hidden = !pick.ids.size; };
  pick.listeners.push(draw);
  draw();
  return bar;
}

// addManyToList adds books to one of the user's playlists (or, for an admin, a
// collection), or to a new playlist.
async function addManyToList(books) {
  const lists = await loadLists();
  const add = async (kind, l) => {
    const bookIds = [...l.bookIds, ...books.map((b) => b.id).filter((id) => !l.bookIds.includes(id))];
    await api(`/api/${kind}/${encodeURIComponent(l.id)}`, { method: "PATCH", body: { bookIds } });
    toast(`Added to ${l.name}`);
    dialog.close();
  };
  const rows = (kind) => lists[kind].length ? el("div", { class: "lines" }, lists[kind].map((l) => el("div", {},
    span("t clip", l.name), span("small muted", plural(l.bookIds.length, "book")),
    el("button", { class: "btn secondary small", type: "button", onclick: () => add(kind, l).catch((e) => toast(e.message)) }, "Add")))) : emptyNote(`No ${kind} yet.`);
  const fresh = el("input", { type: "text", placeholder: "New playlist", maxlength: 100, "aria-label": "New playlist name" });
  const make = el("form", { class: "row", onsubmit: async (ev) => {
    ev.preventDefault();
    if (!fresh.value.trim()) return;
    try { await api("/api/playlists", { method: "POST", body: { name: fresh.value, bookIds: books.map((b) => b.id) } }); toast("Playlist made"); dialog.close(); }
    catch (e) { toast(e.message); }
  } }, el("div", { class: "grow" }, fresh), el("button", { class: "btn secondary", type: "submit" }, icon("add"), "Make"));
  openSheet(`Add ${plural(books.length, "book")} to a list`, [
    el("h3", { class: "section-title" }, "My playlists"), rows("playlists"), make,
    isAdmin() ? el("h3", { class: "section-title" }, "Collections (everyone sees these)") : null,
    isAdmin() ? rows("collections") : null,
  ]);
}

// ------------------------------------------------------------------ sheets

function openSheet(title, parts, { onBack, wide } = {}) {
  delete dialog.dataset.sheet;
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
  if (mode === "chapters") return openSheet("Chapters · " + b.title, chaptersSheet(b), { onBack: () => openBook(id) });
  if (mode === "chapter-edit") return openSheet("Edit chapters · " + b.title, chapterEditor(b), { onBack: () => openBook(id, "chapters"), wide: true });
  openSheet(b.title, bookDetail(b), { wide: true });
}

function currentChapter(b, p) {
  return p && !p.finished ? currentChapterAt(b, p.positionMs) : -1;
}

function bookDetail(b) {
  const p = progressOf(b);
  const size = b.files.reduce((n, f) => n + (f.sizeBytes || 0), 0);
  const playing = player.book?.id === b.id;
  const resumeAt = p && !p.finished && p.positionMs > 0;
  const live = playing && !player.done; // loaded in the player and not run out
  const play = el("button", { class: "btn", type: "button", onclick: () => live ? (player.audio.paused ? player.audio.play() : player.audio.pause()) : playBook(b.id, p?.finished ? 0 : undefined) },
    icon(live && !player.audio.paused ? "pause" : "play"), live ? (player.audio.paused ? "Resume" : "Pause") : resumeAt ? `Resume at ${fmtClock(p.positionMs)}` : p?.finished ? "Play again" : "Play");
  const download = el("button", { class: "btn secondary", type: "button", onclick: async () => {
    download.disabled = true;
    try {
      const link = await api(`/api/books/${encodeURIComponent(b.id)}/download-link`, { method: "POST" });
      clickDownload(link.url);
    } catch (e) { toast(e.message); }
    download.disabled = false;
  } }, icon("download"), `Download (${fmtBytes(size)})`);
  const actions = el("div", { class: "row", style: "margin-top:14px" }, play, download,
    p?.finished ? el("button", { class: "btn secondary", type: "button", onclick: () => setProgress(b, false) }, icon("replay"), "Start over") :
      el("button", { class: "btn secondary", type: "button", onclick: () => setProgress(b, true) }, icon("check"), "Mark finished"),
    resumeAt ? el("button", { class: "btn ghost", type: "button", onclick: () => setProgress(b, false) }, "Start over") : null,
    el("button", { class: "btn ghost", type: "button", onclick: () => addToList(b) }, icon("queue"), "Add to a list"));
  if (isAdmin()) {
    actions.append(el("div", { class: "row", style: "flex-basis:100%;margin-top:2px" },
      el("button", { class: "btn secondary small", type: "button", onclick: () => openBook(b.id, "match") }, icon("magic"), "Find details"),
      el("button", { class: "btn secondary small", type: "button", onclick: () => openBook(b.id, "edit") }, icon("edit"), "Edit details"),
      el("button", { class: "btn secondary small", type: "button", onclick: () => pickCover(b) }, icon("image"), "Change cover"),
      el("button", { class: "btn secondary small", type: "button", onclick: () => openBook(b.id, "chapter-edit") }, icon("chapters"), "Edit chapters")));
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
        ["Series", seriesLabel(b)],
        ["Length", fmtDuration(b.durationMs)], ["Published", [b.publishedYear, b.publisher].filter(Boolean).join(", ")],
        ["Language", b.language], ["ISBN", b.isbn], ["ASIN", b.asin],
        ["Library", state.libraries.length > 1 ? b.library : ""],
      ])),
      (b.genres || []).length ? el("div", { class: "tags" }, b.genres.slice(0, 6).map((g) => el("span", { class: "tag" }, g))) : null,
      actions)),
    progress,
    b.description ? el("p", { class: "desc" }, b.description) : null,
    bookmarksPanel(b),
    transcriptPanel(b),
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
  let provider = "audible";
  const region = el("select", { "aria-label": "Audible region", style: "width:auto", onchange: () => search() },
    ["us", "uk", "ca", "au", "de", "fr", "it", "es", "in", "jp"].map((r) => el("option", { value: r }, "Region: " + r.toUpperCase())));
  const providerChips = el("div", { class: "segmented", role: "group", "aria-label": "Provider" });
  const drawProviders = () => providerChips.replaceChildren(...PROVIDERS.map(([id, label]) =>
    chip(label, provider === id, () => { provider = id; region.hidden = id !== "audible"; drawProviders(); search(); })));
  drawProviders();
  const title = el("input", { type: "text", value: b.title, "aria-label": "Title", placeholder: "Title" });
  const author = el("input", { type: "text", value: firstAuthor(b), "aria-label": "Author", placeholder: "Author" });
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
  const series = c.series ? " · " + seriesLabel(c) : "";
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
          return el("tr", { class: b ? "link" : null, onclick: () => b && openBook(b.id) },
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
    el("tbody", {}, devices.map((d) => el("tr", {},
      el("td", { class: "muted" }, icon(deviceIcon(d.name))),
      el("td", {}, d.name || span("muted", "Unnamed device"), d.current ? el("span", { class: "badge" }, "This browser") : null),
      el("td", { class: "hide-sm mono muted" }, d.id),
      el("td", { class: "muted" }, fmtDate(d.createdAt)),
      el("td", { class: "muted" }, relative(d.lastSeenAt)),
      el("td", { class: "c-num" }, el("button", { class: "btn danger small", type: "button", onclick: () => revoke(d) }, "Sign out")))))) :
    emptyNote("No devices signed in.")));
  out.push(el("p", { class: "tiny muted", style: "margin-top:10px" }, "Signing a device out makes it ask for the password again. Changing a password signs out every device. Last-seen times update about once an hour."));
  return out;
}

// ------------------------------------------------------------------ libraries (admin)

async function viewLibraries(tools) {
  const { libraries: libs } = await api("/api/libraries");
  state.libraries = libs;
  if (libs.length) tools.append(el("button", { class: "btn secondary small", type: "button", onclick: () => uploadSheet(libs[0].name) }, icon("upload"), "Upload"));
  tools.append(el("button", { class: "btn small", type: "button", onclick: () => addLibrarySheet() }, icon("add"), "Add library"));
  const remove = async (l) => {
    if (!confirm(`Remove ${l.name}? Its books disappear from the server and every device. The files stay where they are, and adding the folder back brings the books and everyone's places back.`)) return;
    try {
      await api("/api/libraries/" + encodeURIComponent(l.name), { method: "DELETE" });
      state.loadedAt = 0;
      toast(`Removed ${l.name}`);
      render();
    } catch (e) { toast(e.message); }
  };
  return [
    el("div", { class: "table-wrap" }, libs.length ? el("table", { class: "grid" },
      el("thead", {}, el("tr", {}, el("th", {}, "Name"), el("th", {}, "Status"), el("th", { class: "hide-sm" }, "Path"), el("th", { class: "c-num" }, "Books"), el("th", { class: "c-num" }, "Length"), el("th", { class: "hide-sm" }, "Source"), el("th", {}))),
      el("tbody", {}, libs.map((l) => el("tr", {},
        el("td", {}, el("span", { class: "row", style: "flex-wrap:nowrap" }, el("span", { class: "gold" }, icon("folder")), span("strong", l.name))),
        el("td", {}, l.error ? span("status err", "Unreadable") : span("status ok", "Online"), l.error ? el("div", { class: "tiny err" }, l.error) : null),
        el("td", { class: "hide-sm mono muted" }, l.path),
        el("td", { class: "c-num" }, l.books),
        el("td", { class: "c-num" }, fmtDuration(l.durationMs)),
        el("td", { class: "hide-sm muted" }, l.fixed ? "Command line" : "Added here"),
        el("td", { class: "c-num" }, el("div", { class: "row end", style: "flex-wrap:nowrap" },
          el("button", { class: "btn secondary small", type: "button", onclick: () => uploadSheet(l.name) }, icon("upload"), "Upload"),
          l.fixed ? null : el("button", { class: "btn danger small", type: "button", onclick: () => remove(l) }, "Remove"))))))) :
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
      state.loadedAt = 0;
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
      el("tbody", {}, users.map((u) => el("tr", { class: "link", onclick: () => userSheet(u, libraries) },
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

// ------------------------------------------------------------------ player

// One <audio> element plays the book's files in turn; positions are on the book's
// combined timeline, as everywhere else. Progress is saved the way the apps save it:
// on pause, when the tab is hidden, and every 30 seconds while playing.
const player = {
  audio: new Audio(),
  book: null,
  files: [], // { url, start, durationMs }
  index: -1,
  pendingSeek: null,
  saveAt: 0,
  sleepAt: 0, // epoch ms, or -1 for the end of the chapter
  done: false, // the book ran to its end: later saves keep it finished
  bar: null,
};
player.audio.preload = "auto";

function playerPosition() {
  const f = player.files[player.index];
  if (!f) return 0;
  // While another file loads, its currentTime is not the place yet: the pending seek is.
  return f.start + Math.round((player.pendingSeek ?? player.audio.currentTime) * 1000);
}

function fileAt(ms) {
  let i = player.files.findIndex((f) => ms < f.start + f.durationMs);
  if (i < 0) i = player.files.length - 1;
  return i;
}

function seekTo(ms, autoplay = !player.audio.paused) {
  if (!player.book) return;
  ms = Math.max(0, Math.min(ms, player.book.durationMs - 250));
  player.done = false;
  const i = fileAt(ms);
  const offset = (ms - player.files[i].start) / 1000;
  if (i !== player.index) {
    player.index = i;
    player.pendingSeek = offset;
    player.audio.src = player.files[i].url;
    player.audio.load();
  } else {
    player.audio.currentTime = offset;
  }
  if (autoplay) player.audio.play().catch(() => {});
  drawPlayer();
}

async function playBook(id, atMs, { keepSheet = false } = {}) {
  const b = state.books.find((x) => x.id === id);
  if (!b) return;
  try {
    if (player.book && player.book.id !== id) await saveProgress();
    const link = await api(`/api/books/${encodeURIComponent(id)}/stream-link`, { method: "POST" });
    let start = 0;
    player.files = link.files.map((f) => { const out = { url: f.url, start, durationMs: f.durationMs }; start += f.durationMs; return out; });
    player.book = b;
    player.index = -1;
    const p = progressOf(b);
    const from = atMs ?? (p && !p.finished ? p.positionMs : 0);
    setSpeed(Number(state.prefs.speed || 1));
    seekTo(from, true);
    mediaSession();
    if (!keepSheet && dialog.open) dialog.close();
  } catch (e) { toast(e.message); }
}

async function saveProgress({ finished = false, keepalive = false } = {}) {
  const b = player.book;
  if (!b) return;
  finished = finished || player.done;
  const pos = finished ? b.durationMs : playerPosition();
  const body = { positionMs: pos, updatedAt: Date.now(), finished };
  const speed = player.speed || 1;
  if (speed !== 1) body.speed = speed;
  player.saveAt = Date.now();
  try {
    const res = await fetch(`/api/progress/${encodeURIComponent(b.id)}`, {
      method: "PUT", keepalive,
      headers: { Authorization: "Bearer " + state.token, "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (res.ok || res.status === 409) state.progress.set(b.id, await res.json());
  } catch { /* the next save catches up */ }
}

function currentChapterAt(b, ms) {
  return b.chapters.findIndex((c) => ms >= c.startMs && ms < c.endMs);
}

function stopPlayer() {
  if (!player.book) return;
  saveProgress();
  player.audio.pause();
  player.audio.removeAttribute("src");
  player.audio.load();
  player.book = null;
  player.index = -1;
  player.sleepAt = 0;
  drawPlayer();
}

player.audio.addEventListener("loadedmetadata", () => {
  if (player.pendingSeek != null) { player.audio.currentTime = player.pendingSeek; player.pendingSeek = null; }
  setSpeed(player.speed);
  if (player.book) drawPlayer();
});
player.audio.addEventListener("ended", () => {
  if (player.index + 1 < player.files.length) {
    seekTo(player.files[player.index + 1].start, true);
  } else {
    player.done = true;
    saveProgress({ finished: true });
    toast(`Finished ${player.book.title}`);
    drawPlayer();
  }
});
// Loading the next file pauses the element for a moment; that is not the listener pausing.
player.audio.addEventListener("pause", () => { if (player.pendingSeek != null) return; if (!player.audio.ended) saveProgress(); drawPlayer(); });
player.audio.addEventListener("play", () => { drawPlayer(); requestAnimationFrame(playerFrame); });

// playerFrame moves the timeline every frame while playing; the audio element itself
// only reports its time a few times a second, which made the playhead hop.
function playerFrame() {
  if (!player.book || player.audio.paused) return;
  updatePlayerTime();
  requestAnimationFrame(playerFrame);
}
player.audio.addEventListener("error", () => {
  if (!player.book) return;
  player.pendingSeek = null;
  drawPlayer();
  toast("This file could not be played in the browser");
});
player.audio.addEventListener("timeupdate", () => {
  if (!player.book) return;
  const pos = playerPosition();
  if (!player.audio.paused && Date.now() - player.saveAt > 30000) saveProgress();
  if (player.sleepAt > 0 && Date.now() >= player.sleepAt) { player.sleepAt = 0; player.audio.pause(); toast("Sleep timer: paused"); }
  if (player.sleepAt === -1) {
    const c = player.book.chapters[currentChapterAt(player.book, pos)];
    if (c && pos >= c.endMs - 400) { player.sleepAt = 0; player.audio.pause(); toast("Sleep timer: end of chapter"); }
  }
  updatePlayerTime();
});
document.addEventListener("visibilitychange", () => { if (document.hidden && player.book) saveProgress({ keepalive: true }); });
window.addEventListener("pagehide", () => { if (player.book) saveProgress({ keepalive: true }); });

// setSpeed sets the playback rate where loading another file won't undo it: load()
// puts playbackRate back to defaultPlaybackRate.
function setSpeed(rate) {
  player.speed = rate || 1;
  player.audio.defaultPlaybackRate = player.speed;
  player.audio.playbackRate = player.speed;
}

function skip(sec) { seekTo(playerPosition() + sec * 1000); }
function chapterJump(dir) {
  const b = player.book;
  if (!b?.chapters.length) return skip(dir * 30);
  const pos = playerPosition();
  let i = currentChapterAt(b, pos);
  if (dir < 0 && i >= 0 && pos - b.chapters[i].startMs > 3000) return seekTo(b.chapters[i].startMs);
  i = Math.max(0, Math.min(b.chapters.length - 1, i + dir));
  seekTo(b.chapters[i].startMs);
}

function mediaSession() {
  if (!("mediaSession" in navigator) || !player.book) return;
  const b = player.book;
  const art = coverCache.get(b.coverUrl + "?v=" + b.updatedAt);
  navigator.mediaSession.metadata = new MediaMetadata({ title: b.title, artist: b.author || "", album: b.series || "", artwork: art ? [{ src: art }] : [] });
  const h = {
    play: () => player.audio.play(), pause: () => player.audio.pause(),
    seekbackward: () => skip(-30), seekforward: () => skip(30),
    previoustrack: () => chapterJump(-1), nexttrack: () => chapterJump(1),
    seekto: (d) => seekTo(d.seekTime * 1000),
  };
  for (const [k, f] of Object.entries(h)) { try { navigator.mediaSession.setActionHandler(k, f); } catch { /* not supported */ } }
}

const SPEEDS = [0.75, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5];

// drawPlayer builds the bar when the book changes; otherwise it only updates what
// moved, so seeking or pausing never rebuilds the cover or the slider under the pointer.
function drawPlayer() {
  if (!player.bar) {
    player.bar = el("div", { class: "player", id: "player" });
    document.body.append(player.bar);
  }
  document.body.classList.toggle("playing", !!player.book);
  const b = player.book;
  if (!b) { player.bar.replaceChildren(); player.drawn = null; return; }
  if (player.drawn === b) return updatePlayerControls();
  player.drawn = b;
  const scrub = el("input", { type: "range", class: "scrub", min: 0, max: b.durationMs, step: "any", value: playerPosition(), "aria-label": "Position",
    oninput: (e) => { scrub.dataset.dragging = "1"; times.firstChild.textContent = fmtClock(Number(e.target.value)); },
    onchange: (e) => { delete scrub.dataset.dragging; seekTo(Number(e.target.value)); } });
  const times = el("div", { class: "times tiny muted" }, span("", ""), span("", ""));
  const chapter = el("div", { class: "small muted clip", id: "player-chapter" });
  const speed = el("select", { "aria-label": "Speed", class: "mini-select", onchange: (e) => {
    setSpeed(Number(e.target.value)); state.prefs.speed = e.target.value; savePrefs();
  } }, SPEEDS.map((v) => el("option", { value: v, selected: player.speed === v ? true : null }, v + "×")));
  const sleep = el("select", { "aria-label": "Sleep timer", class: "mini-select", onchange: (e) => {
    const v = e.target.value;
    player.sleepAt = v === "chapter" ? -1 : v === "0" ? 0 : Date.now() + Number(v) * 60000;
    toast(v === "0" ? "Sleep timer off" : v === "chapter" ? "Pausing at the end of this chapter" : `Pausing in ${v} minutes`);
  } }, [["0", "Sleep: off"], ["15", "15 min"], ["30", "30 min"], ["45", "45 min"], ["60", "60 min"], ["chapter", "End of chapter"]].map(([v, l]) =>
    el("option", { value: v, selected: (player.sleepAt === -1 ? "chapter" : player.sleepAt > 0 ? null : "0") === v ? true : null }, l)));
  player.bar.replaceChildren(
    el("button", { class: "p-cover", type: "button", title: "Open book", onclick: () => openBook(b.id) }, cover(b)),
    el("div", { class: "p-info" }, el("div", { class: "strong clip" }, b.title), chapter),
    el("div", { class: "p-controls" },
      iconBtn("prev", "Previous chapter", () => chapterJump(-1)),
      iconBtn("replay", "Back 30 seconds", () => skip(-30)),
      el("button", { class: "p-play", type: "button", onclick: () => player.audio.paused ? player.audio.play() : player.audio.pause() }),
      iconBtn("forward", "Forward 30 seconds", () => skip(30)),
      iconBtn("next", "Next chapter", () => chapterJump(1))),
    el("div", { class: "p-track" }, scrub, times),
    el("div", { class: "p-extra" },
      speed, sleep,
      iconBtn("bookmark", "Add a bookmark here", () => addBookmark(b, playerPosition())),
      iconBtn("chapters", "Chapters", () => openBook(b.id, "chapters")),
      iconBtn("notes", "Transcript", () => openTranscript(b)),
      iconBtn("close", "Stop", stopPlayer)));
  player.scrub = scrub;
  player.times = times;
  player.chapter = chapter;
  player.sleep = sleep;
  updatePlayerControls();
}

function updatePlayerControls() {
  const btn = player.bar.querySelector(".p-play");
  const paused = player.audio.paused && player.pendingSeek == null;
  if (btn.dataset.state !== String(paused)) {
    btn.dataset.state = String(paused);
    btn.setAttribute("aria-label", paused ? "Play" : "Pause");
    btn.replaceChildren(icon(paused ? "play" : "pause"));
  }
  player.sleep.value = player.sleepAt === -1 ? "chapter" : player.sleepAt > 0 ? player.sleep.value : "0";
  updatePlayerTime();
}

function updatePlayerTime() {
  const b = player.book;
  if (!b || !player.scrub) return;
  const pos = playerPosition();
  if (!player.scrub.dataset.dragging) player.scrub.value = pos;
  player.scrub.style.setProperty("--p", (pos / b.durationMs).toFixed(5)); // the CSS puts the fill under the thumb
  player.times.firstChild.textContent = fmtClock(pos);
  player.times.lastChild.textContent = "-" + fmtClock(b.durationMs - pos);
  const i = currentChapterAt(b, pos);
  player.chapter.textContent = i >= 0 ? `${b.chapters[i].title} · ${i + 1} of ${b.chapters.length}` : `${fmtDuration(b.durationMs - pos)} left`;
  followTranscript(pos);
}

// ------------------------------------------------------------------ progress and bookmarks

async function setProgress(b, finished) {
  if (player.book?.id === b.id) {
    if (finished) { player.done = true; player.audio.pause(); } else seekTo(0, false);
  }
  try {
    const p = await api(`/api/progress/${encodeURIComponent(b.id)}`, { method: "PUT", body: { positionMs: finished ? b.durationMs : 0, updatedAt: Date.now(), finished } });
    state.progress.set(b.id, p);
    toast(finished ? "Marked finished" : "Back to the start");
    openBook(b.id);
    if (state.view === "library" || state.view === "dashboard") refreshMain(state.view === "library" ? viewLibrary : viewDashboard);
  } catch (e) { toast(e.message); }
}

let bookmarkCache = null;
async function bookmarksFor(bookId) {
  if (!bookmarkCache) {
    const gen = state.gen;
    const list = (await api("/api/bookmarks")).bookmarks;
    if (gen !== state.gen) return [];
    bookmarkCache = list;
  }
  return bookmarkCache.filter((m) => m.bookId === bookId).sort((a, c) => a.positionMs - c.positionMs);
}

async function addBookmark(b, positionMs) {
  const label = prompt(`Bookmark at ${fmtClock(positionMs)}: a note (optional)`, "");
  if (label === null) return;
  try {
    const m = await api(`/api/bookmarks/${encodeURIComponent(b.id)}`, { method: "POST", body: { positionMs, label: label.trim(), createdAt: Date.now() } });
    bookmarkCache?.push(m);
    toast("Bookmark added");
    if (dialog.open) openBook(b.id);
  } catch (e) { toast(e.message); }
}

async function deleteBookmark(b, m) {
  try {
    await api(`/api/bookmarks/${encodeURIComponent(b.id)}/${encodeURIComponent(m.id)}`, { method: "DELETE" });
    bookmarkCache = bookmarkCache?.filter((x) => x.id !== m.id) || null;
    openBook(b.id);
  } catch (e) { toast(e.message); }
}

function bookmarksPanel(b) {
  const box = el("div", { class: "lines" }, el("div", {}, span("muted", "Loading…")));
  bookmarksFor(b.id).then((list) => {
    box.replaceChildren(...(list.length ? list.map((m) => el("div", {},
      el("button", { class: "linkish", type: "button", title: "Play from here", onclick: () => playBook(b.id, m.positionMs) }, span("n mono", fmtClock(m.positionMs))),
      span("t", m.label || span("muted", "No note")),
      span("end", fmtDate(m.createdAt)),
      el("button", { class: "icon-btn sm", type: "button", title: "Delete bookmark", "aria-label": "Delete bookmark", onclick: () => deleteBookmark(b, m) }, icon("remove")))) :
      [el("div", {}, span("muted", "No bookmarks yet. Add one from the player, or in the apps."))]));
  }).catch((e) => box.replaceChildren(el("div", {}, span("err", e.message))));
  return el("details", { class: "panel", open: true },
    el("summary", {}, icon("bookmark"), span("strong grow", "Bookmarks")), box);
}

// ------------------------------------------------------------------ chapters (the player's list, and the admin editor)

function chaptersSheet(b) {
  const now = player.book?.id === b.id ? currentChapterAt(b, playerPosition()) : currentChapter(b, progressOf(b));
  return [
    b.chapters.length ? el("div", { class: "lines" }, b.chapters.map((c, i) => el("div", { class: i === now ? "now" : "" },
      el("button", { class: "linkish", type: "button", title: "Play from here", onclick: () => playBook(b.id, c.startMs) }, span("n", i + 1)),
      el("button", { class: "linkish t", type: "button", onclick: () => playBook(b.id, c.startMs) }, c.title),
      span("end", fmtClock(c.startMs))))) : el("p", { class: "muted" }, "This book has no chapters."),
    isAdmin() ? el("div", { class: "row end" }, el("button", { class: "btn secondary", type: "button", onclick: () => openBook(b.id, "chapter-edit") }, icon("edit"), "Edit chapters")) : null,
  ];
}

const parseClock = (s) => {
  const parts = String(s).trim().split(":").map(Number);
  if (!parts.length || parts.some((n) => !Number.isFinite(n) || n < 0)) return NaN;
  return Math.round(parts.reduce((t, n) => t * 60 + n, 0) * 1000);
};

function chapterEditor(b) {
  const rows = el("div", { class: "stack" });
  const add = (title, startMs) => {
    const t = el("input", { type: "text", value: title, "aria-label": "Title" });
    const at = el("input", { type: "text", class: "mono", value: fmtClock(startMs), "aria-label": "Start", style: "width:110px" });
    const row = el("div", { class: "row", style: "flex-wrap:nowrap" },
      el("div", { class: "grow" }, t), at,
      player.book?.id === b.id ? iconBtn("timer", "Use the player's position", () => { at.value = fmtClock(playerPosition()); }) : null,
      iconBtn("play", "Play from here", () => { const ms = parseClock(at.value); if (Number.isFinite(ms)) playBook(b.id, ms); }),
      iconBtn("remove", "Remove", () => row.remove()));
    row._get = () => ({ title: t.value, startMs: parseClock(at.value) });
    rows.append(row);
  };
  b.chapters.forEach((c) => add(c.title, c.startMs));
  if (!b.chapters.length) add("Chapter 1", 0);
  const save = el("button", { class: "btn", type: "button", onclick: async () => {
    const list = [...rows.children].map((r) => r._get());
    const bad = list.findIndex((c) => !Number.isFinite(c.startMs));
    if (bad >= 0) { toast(`Chapter ${bad + 1}: write the start as h:mm:ss`); return; }
    save.disabled = true;
    try {
      const updated = await api(`/api/books/${encodeURIComponent(b.id)}/chapters`, { method: "PUT", body: { chapters: list } });
      replaceBook(updated); toast("Chapters saved"); openBook(b.id, "chapters");
    } catch (e) { toast(e.message); save.disabled = false; }
  } }, "Save chapters");
  return [
    el("p", { class: "small muted" }, "Titles and start times (h:mm:ss, on the whole book's timeline). Each chapter ends where the next begins. Saved with the book's details on the server; apps get them on their next sync."),
    rows,
    el("div", { class: "row" },
      el("button", { class: "btn secondary", type: "button", onclick: () => {
        const last = [...rows.children].pop()?._get();
        add(`Chapter ${rows.children.length + 1}`, player.book?.id === b.id ? playerPosition() : Math.min(b.durationMs - 1000, (last?.startMs || 0) + 600000));
      } }, icon("add"), "Add chapter"),
      el("button", { class: "btn danger", type: "button", onclick: async () => {
        if (!confirm("Go back to the chapters read from the files?")) return;
        try { const updated = await api(`/api/books/${encodeURIComponent(b.id)}/chapters`, { method: "PUT", body: { chapters: [] } }); replaceBook(updated); toast("Back to the files' chapters"); openBook(b.id, "chapters"); }
        catch (e) { toast(e.message); }
      } }, "Use the files' chapters"),
      el("span", { class: "grow" }),
      el("button", { class: "btn secondary", type: "button", onclick: () => openBook(b.id, "chapters") }, "Cancel"),
      save),
  ];
}

// ------------------------------------------------------------------ covers (admin)

function pickCover(b) {
  const input = el("input", { type: "file", accept: "image/jpeg,image/png,image/webp", style: "display:none", onchange: async () => {
    const file = input.files[0];
    input.remove();
    if (!file) return;
    try {
      const out = await api(`/api/books/${encodeURIComponent(b.id)}/cover`, { method: "PUT", blob: file });
      replaceBook(out); toast("Cover changed"); openBook(b.id);
    } catch (e) { toast(e.message); }
  } });
  document.body.append(input);
  input.click();
}

// ------------------------------------------------------------------ series and authors

function groupedShelf(books, by) {
  const groups = new Map();
  for (const b of books) {
    const keys = by === "series" ? (b.series ? [b.series] : []) : (b.author ? b.author.split(",").map((a) => a.trim()).filter(Boolean) : ["Unknown author"]);
    for (const k of keys) {
      if (!groups.has(k)) groups.set(k, []);
      groups.get(k).push(b);
    }
  }
  const names = [...groups.keys()].sort(collator.compare);
  if (!names.length) return el("div", { class: "table-wrap" }, emptyNote(by === "series" ? "No series yet. Series come from tags, folder names (Author/Series/Book) or saved details." : "No authors."));
  return el("div", { class: "groups" }, names.map((name) => {
    const list = groups.get(name).sort(by === "series" ? (a, c) => (a.seriesIndex ?? 1e9) - (c.seriesIndex ?? 1e9) || byTitle(a, c) : byTitle);
    const total = list.reduce((n, b) => n + b.durationMs, 0);
    const done = list.filter((b) => statusOf(b) === "finished").length;
    const grid = el("div", { class: "cover-grid" });
    const sec = el("details", { class: "group-sec", open: names.length <= 12 ? true : null,
      ontoggle: () => { if (sec.open && !grid.childElementCount) grid.append(...list.map(bookTile)); } },
      el("summary", { class: "group-head" }, el("h2", {}, name), span("small muted", `${plural(list.length, "book")} · ${fmtDuration(total)}${done ? ` · ${done} finished` : ""}`)),
      grid);
    if (sec.open) grid.append(...list.map(bookTile));
    return sec;
  }));
}

// ------------------------------------------------------------------ account

async function viewAccount() {
  const me = await api("/api/me");
  const cur = el("input", { type: "password", autocomplete: "current-password", required: true });
  const nw = el("input", { type: "password", autocomplete: "new-password", minlength: 8, required: true });
  const again = el("input", { type: "password", autocomplete: "new-password", minlength: 8, required: true });
  const pw = el("form", { class: "form", onsubmit: async (ev) => {
    ev.preventDefault();
    if (nw.value !== again.value) { toast("The new passwords don't match"); return; }
    try {
      const out = await api("/api/me/password", { method: "PUT", body: { current: cur.value, new: nw.value } });
      const remembered = !!storage()?.getItem(TOKEN_KEY);
      saveToken(out.token, remembered);
      toast("Password changed. Your other devices will ask for it again.");
      pw.reset();
    } catch (e) { toast(e.status === 429 ? "Too many wrong passwords. Try again later." : e.message); }
  } },
  el("label", { class: "field" }, el("span", {}, "Current password"), cur),
  el("label", { class: "field" }, el("span", {}, "New password (8 or more characters)"), nw),
  el("label", { class: "field" }, el("span", {}, "New password again"), again),
  el("div", { class: "wide row end" }, el("button", { class: "btn", type: "submit" }, "Change password")));

  const everywhere = el("button", { class: "btn danger", type: "button", onclick: async () => {
    if (!confirm("Sign out every other device? They will ask for the password again.")) return;
    try {
      const { devices } = await api("/api/devices");
      for (const d of devices.filter((x) => !x.current)) await api("/api/devices/" + encodeURIComponent(d.id), { method: "DELETE" });
      toast("Other devices signed out");
    } catch (e) { toast(e.message); }
  } }, "Sign out other devices");

  return el("div", { class: "widgets" },
    widget("Profile", {
      visual: [el("span", { class: "avatar", style: "width:64px;height:64px;font-size:26px" }, me.name.slice(0, 1)), span("strong", me.name)],
      overview: kv([["Username", me.name], ["Role", me.role === "admin" ? "Administrator" : "Listener"], ["Libraries", me.role === "admin" || !me.libraries?.length ? "All" : me.libraries.join(", ")], ["Devices", me.devices]]),
    }),
    widget("Appearance", { body: [
      el("div", { class: "segmented" }, [["dark", "Dark"], ["light", "Light"]].map(([id, label]) =>
        chip(label, state.prefs.theme === id, () => { state.prefs.theme = id; savePrefs(); applyTheme(); render(); }))),
      el("p", { class: "small muted", style: "margin-top:10px" }, "Kept in this browser."),
    ] }),
    widget("Password", { wide: true, body: [pw, el("p", { class: "tiny muted", style: "margin-top:8px" }, "Changing it signs out your other devices; this browser stays signed in.")] }),
    widget("Devices", { body: [el("p", { class: "small muted" }, "Lost a phone? Sign it out here, or pick it on the Devices page."), el("div", { style: "margin-top:12px" }, everywhere)] }),
    await ssoWidget(me));
}

// ssoWidget links this account to the identity provider, when the server has one: the
// owner signs in there once from here, and from then on that sign-in reaches this account.
async function ssoWidget(me) {
  const o = await api("/api/oidc").catch(() => null);
  if (!o?.enabled) return null;
  const link = el("button", { class: "btn secondary", type: "button", onclick: async () => {
    try {
      const { code } = await api("/api/oidc/link", { method: "POST" });
      await ssoStart(!!storage()?.getItem(TOKEN_KEY), code);
    } catch (e) { toast(e.message); }
  } }, icon("person"), "Link single sign-on");
  const unlink = el("button", { class: "btn ghost", type: "button", onclick: async () => {
    if (!confirm("Unlink this account from single sign-on? You can still sign in with your password.")) return;
    try { await api("/api/me/oidc", { method: "DELETE" }); toast("Unlinked"); render(); } catch (e) { toast(e.message); }
  } }, "Unlink");
  return widget("Single sign-on", { body: [
    el("p", { class: "small" }, me.sso ? "This account is linked: “" + o.buttonText + "” signs you in to it." : "Link this account so that “" + o.buttonText + "” signs you in to it. You sign in at the provider once to do it."),
    el("div", { class: "row", style: "margin-top:12px" }, me.sso ? unlink : link),
  ] });
}

// ------------------------------------------------------------------ upload (admin)

function uploadSheet(libName) {
  const lib = el("select", {}, state.libraries.map((l) => el("option", { value: l.name, selected: l.name === libName ? true : null }, l.name)));
  const folder = el("input", { type: "text", placeholder: "Author/Title (leave empty to keep the folders you pick)" });
  const list = el("div", { class: "uploads" });
  let queue = [];
  const addFiles = (items) => {
    for (const { file, rel } of items) {
      const row = el("div", { class: "up-row" }, span("clip grow small", rel), span("tiny muted", fmtBytes(file.size)), el("div", { class: "progress" }, el("i", { style: "width:0%" })));
      queue.push({ file, rel, row });
      list.append(row);
    }
    go.disabled = !queue.length;
    go.lastChild.textContent = `Upload ${plural(queue.length, "file")}`;
  };
  const filesIn = el("input", { type: "file", multiple: true, style: "display:none", onchange: () => addFiles([...filesIn.files].map((f) => ({ file: f, rel: f.name }))) });
  const dirIn = el("input", { type: "file", multiple: true, webkitdirectory: true, style: "display:none", onchange: () => addFiles([...dirIn.files].map((f) => ({ file: f, rel: f.webkitRelativePath || f.name }))) });
  const drop = el("div", { class: "drop", tabindex: 0,
    ondragover: (e) => { e.preventDefault(); drop.classList.add("over"); },
    ondragleave: () => drop.classList.remove("over"),
    ondrop: async (e) => { e.preventDefault(); drop.classList.remove("over"); addFiles(await droppedFiles(e.dataTransfer)); } },
    icon("upload"), el("div", { class: "strong" }, "Drop audiobook folders or files here"),
    el("div", { class: "row", style: "justify-content:center" },
      el("button", { class: "btn secondary small", type: "button", onclick: () => dirIn.click() }, "Choose a folder"),
      el("button", { class: "btn secondary small", type: "button", onclick: () => filesIn.click() }, "Choose files")),
    filesIn, dirIn);
  const go = el("button", { class: "btn", type: "button", disabled: true, onclick: async () => {
    go.disabled = true;
    const prefix = folder.value.trim().replace(/^\/+|\/+$/g, "");
    let ok = 0, failed = 0;
    for (const item of queue) {
      const dest = (prefix ? prefix + "/" : "") + item.rel;
      try { await uploadOne(lib.value, dest, item.file, item.row.querySelector(".progress > i")); ok++; item.row.classList.add("ok"); }
      catch (e) { failed++; item.row.append(span("tiny err", e.message)); }
    }
    queue = [];
    try { await rescanNow(); } catch { /* the periodic scan will */ }
    toast(`${plural(ok, "file")} uploaded${failed ? `, ${failed} failed` : ""}`);
    if (!failed) { dialog.close(); render(); }
  } }, icon("upload"), span("", "Upload"));
  openSheet("Upload audiobooks", [
    el("div", { class: "form" },
      el("label", { class: "field" }, el("span", {}, "Library"), lib),
      el("label", { class: "field" }, el("span", {}, "Into folder"), folder)),
    drop, list,
    el("p", { class: "tiny muted" }, "Audio, cover pictures and metadata files (.opf, metadata.json, desc.txt) are accepted. Existing files are not overwritten. The library is rescanned when the upload is done."),
    el("div", { class: "row end" }, el("button", { class: "btn secondary", type: "button", onclick: () => dialog.close() }, "Close"), go),
  ]);
}

// droppedFiles walks dropped folders (DataTransfer entries) into files with their paths.
async function droppedFiles(dt) {
  const out = [];
  const walk = (entry, prefix) => new Promise((resolve) => {
    if (entry.isFile) entry.file((f) => { out.push({ file: f, rel: prefix + f.name }); resolve(); }, resolve);
    else if (entry.isDirectory) {
      const reader = entry.createReader();
      const all = [];
      const read = () => reader.readEntries(async (batch) => {
        if (batch.length) { all.push(...batch); read(); }
        else { for (const e of all) await walk(e, prefix + entry.name + "/"); resolve(); }
      }, resolve);
      read();
    } else resolve();
  });
  const entries = [...dt.items].map((i) => i.webkitGetAsEntry?.()).filter(Boolean);
  if (!entries.length) return [...dt.files].map((f) => ({ file: f, rel: f.name }));
  for (const e of entries) await walk(e, "");
  return out;
}

// uploadOne sends a file with progress (fetch cannot report upload progress).
function uploadOne(library, rel, file, bar) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("PUT", `/api/libraries/${encodeURIComponent(library)}/files?path=${encodeURIComponent(rel)}`);
    xhr.setRequestHeader("Authorization", "Bearer " + state.token);
    xhr.upload.onprogress = (e) => { if (e.lengthComputable && bar) bar.style.width = ((e.loaded / e.total) * 100).toFixed(1) + "%"; };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) { if (bar) bar.style.width = "100%"; resolve(); }
      else { let msg = xhr.statusText; try { msg = JSON.parse(xhr.responseText).error || msg; } catch { /* plain */ } reject(new Error(msg)); }
    };
    xhr.onerror = () => reject(new Error("network error"));
    xhr.send(file);
  });
}

// ------------------------------------------------------------------ maintenance (admin)

async function viewMaintenance(tools) {
  const [{ problems }] = await Promise.all([api("/api/problems"), loadLibrary()]);
  const gaps = (b) => [!b.coverUrl && "cover", !b.author && "author", !b.description && "description", !b.narrator && "narrator"].filter(Boolean);
  const lacking = state.books.filter((b) => gaps(b).length).sort(byTitle);

  return [
    el("section", { class: "widget", style: "margin-bottom:12px" },
      el("div", { class: "w-head" }, el("h2", {}, `Scan problems (${problems.length})`)),
      problems.length ? el("table", { class: "grid" },
        el("thead", {}, el("tr", {}, el("th", {}, "Library"), el("th", {}, "Path"), el("th", {}, "Problem"))),
        el("tbody", {}, problems.map((p) => el("tr", {},
          el("td", {}, p.library), el("td", { class: "mono small" }, p.path), el("td", { class: "err small" }, p.error))))) :
        el("p", { class: "muted", style: "padding:14px" }, "The last scan used everything it found.")),
    el("section", { class: "widget" },
      el("div", { class: "w-head" }, el("h2", {}, `Books missing details (${lacking.length})`)),
      lacking.length ? bulkMatch(lacking, gaps) : el("p", { class: "muted", style: "padding:14px" }, "Every book has a cover, an author, a narrator and a description.")),
  ];
}

// bulkMatch looks books up with a provider in one go, and saves the matches ticked:
// for Maintenance's books missing details, and for books picked in the library.
// gaps, when given, says what each book lacks.
function bulkMatch(books, gaps) {
  const provider = el("select", { "aria-label": "Provider", style: "width:auto" },
    PROVIDERS.map(([v, l]) => el("option", { value: v }, "Source: " + l)));
  const rows = new Map();
  const tbody = el("tbody", {}, books.map((b) => {
    const pick = el("input", { type: "checkbox", checked: true, "aria-label": "Include " + b.title });
    const found = el("td", { class: "muted small" }, "—");
    const use = el("input", { type: "checkbox", disabled: true, "aria-label": "Use this match" });
    rows.set(b.id, { b, pick, found, use, match: null });
    return el("tr", {},
      el("td", { style: "width:32px" }, pick),
      el("td", { class: "c-cover" }, cover(b)),
      el("td", {}, el("div", { class: "strong clip2" }, b.title), el("div", { class: "small muted" }, b.author || "Unknown author")),
      gaps ? el("td", { class: "hide-sm" }, gaps(b).map((g) => el("span", { class: "tag", style: "margin:0 4px 4px 0" }, "no " + g))) : el("td", { class: "hide-sm small muted" }, b.author || ""),
      found, el("td", { style: "width:40px" }, use));
  }));
  const status = span("small muted grow", "");
  const find = el("button", { class: "btn", type: "button", onclick: async () => {
    find.disabled = true;
    const todo = [...rows.values()].filter((r) => r.pick.checked);
    let n = 0;
    for (const r of todo) {
      status.textContent = `Searching ${++n} of ${todo.length}…`;
      r.found.textContent = "Searching…";
      try {
        const q = new URLSearchParams({ provider: provider.value, title: r.b.title, author: firstAuthor(r.b) });
        const { results } = await api(`/api/books/${encodeURIComponent(r.b.id)}/match?` + q);
        const best = results[0];
        r.match = best || null;
        r.found.replaceChildren(best ? el("div", { class: "row", style: "flex-wrap:nowrap" },
          best.coverUrl ? el("img", { src: best.coverUrl, alt: "", referrerpolicy: "no-referrer", style: "width:36px;height:36px;border-radius:3px;object-fit:cover" }) : null,
          el("div", { style: "min-width:0" }, el("div", { class: "clip" }, best.title), el("div", { class: "tiny muted clip" }, (best.authors || []).join(", "), best.narrators?.length ? " · " + best.narrators.join(", ") : ""))) :
          span("muted", "Nothing found"));
        r.use.disabled = !best;
        r.use.checked = !!best && similar(best.title, r.b.title);
      } catch (e) { r.found.replaceChildren(span("err small", e.message)); }
      await new Promise((ok) => setTimeout(ok, 350)); // gentle on the provider
    }
    status.textContent = "Tick the matches to keep, then apply.";
    find.disabled = false;
    apply.disabled = false;
  } }, icon("magic"), "Find matches");
  const apply = el("button", { class: "btn secondary", type: "button", disabled: true, onclick: async () => {
    const chosen = [...rows.values()].filter((r) => r.use.checked && r.match);
    if (!chosen.length) { toast("Nothing ticked"); return; }
    apply.disabled = true;
    let n = 0, failed = 0;
    for (const r of chosen) {
      status.textContent = `Saving ${++n} of ${chosen.length}…`;
      try { await api(`/api/books/${encodeURIComponent(r.b.id)}/metadata?rescan=0`, { method: "PUT", body: r.match }); }
      catch { failed++; }
    }
    await rescanNow().catch(() => {});
    toast(`Saved ${chosen.length - failed} of ${chosen.length}`);
    if (dialog.open) dialog.close();
    render();
  } }, icon("check"), "Apply ticked");

  return [
    el("div", { class: "toolbar", style: "padding:12px 14px 0" }, provider, find, apply, status),
    el("table", { class: "grid" },
      el("thead", {}, el("tr", {}, el("th", {}), el("th", { class: "c-cover" }), el("th", {}, "Book"), el("th", { class: "hide-sm" }, gaps ? "Missing" : "Author"), el("th", {}, "Best match"), el("th", {}, "Use"))),
      tbody),
  ];
}

// similar: a match whose title shares most words with the book's is probably right.
function similar(a, b) {
  const words = (s) => new Set(String(s).toLowerCase().replace(/[^\p{L}\p{N} ]/gu, " ").split(/\s+/).filter((w) => w.length > 2));
  const x = words(a), y = words(b);
  if (!x.size || !y.size) return false;
  let both = 0;
  for (const w of y) if (x.has(w)) both++;
  return both / y.size >= 0.6;
}

// ------------------------------------------------------------------ settings (admin)

async function viewSettings() {
  const st = await api("/api/settings");
  const name = el("input", { type: "text", value: st.saved.name || "", placeholder: st.defaults.name, maxlength: 80 });
  const every = el("input", { type: "number", min: 0, max: 10080, value: st.saved.rescanMinutes ?? "", placeholder: String(st.defaults.rescanMinutes) });
  const form = el("form", { class: "form", onsubmit: async (ev) => {
    ev.preventDefault();
    const body = {};
    if (name.value.trim()) body.name = name.value.trim();
    if (every.value !== "") body.rescanMinutes = Number(every.value);
    try {
      await api("/api/settings", { method: "PUT", body });
      state.server = await api("/api/health");
      document.title = `${state.server.name} · Kithara`;
      toast("Settings saved");
      render();
    } catch (e) { toast(e.message); }
  } },
  el("label", { class: "field" }, el("span", {}, "Server name (shown in the apps)"), name),
  el("label", { class: "field" }, el("span", {}, "Rescan every (minutes, 0 = never)"), every),
  el("p", { class: "wide tiny muted" }, `Empty fields use the server's command line: “${st.defaults.name}”, every ${st.defaults.rescanMinutes} minutes. Changes take effect at once.`),
  el("div", { class: "wide row end" }, el("button", { class: "btn", type: "submit" }, "Save")));

  const dl = el("button", { class: "btn", type: "button", onclick: async () => {
    dl.disabled = true;
    try {
      const res = await api("/api/backup", { raw: true });
      if (!res.ok) throw new Error((await res.json()).error || res.statusText);
      const url = URL.createObjectURL(await res.blob());
      clickDownload(url, `kithara-backup-${new Date().toISOString().slice(0, 10)}.zip`);
      setTimeout(() => URL.revokeObjectURL(url), 10000);
    } catch (e) { toast(e.message); }
    dl.disabled = false;
  } }, icon("download"), "Download backup");

  return el("div", { class: "widgets" },
    widget("General", { wide: true, body: form }),
    widget("Transcription", { wide: true, body: transcriptionSettings() }),
    widget("Single sign-on", { wide: true, body: await oidcForm() }),
    widget("Backup", { body: [
      el("p", { class: "small" }, "Everything that can't be rebuilt from the audiobook folders: accounts, everyone's progress, bookmarks, playlists and listening history, collections, added libraries, saved details and covers, and these settings."),
      el("p", { class: "tiny muted", style: "margin:8px 0 12px" }, "It holds password hashes: keep it somewhere safe. To restore, unzip it into the data folder of a stopped server. Devices sign in again afterwards."),
      dl] }));
}

// oidcForm sets up sign-in through an identity provider (Authelia, Authentik, Keycloak
// and the like).
async function oidcForm() {
  const st = await api("/api/settings/oidc");
  const c = st.config;
  const input = (value, attrs = {}) => el("input", { type: "text", value: value || "", ...attrs });
  const issuer = input(c.issuer, { placeholder: "https://auth.example.com" });
  const clientId = input(c.clientId);
  const secret = el("input", { type: "password", autocomplete: "new-password", placeholder: st.hasSecret ? "Saved. Leave empty to keep it" : "" });
  const publicUrl = input(c.publicUrl, { placeholder: location.origin });
  const claim = input(c.usernameClaim, { placeholder: "preferred_username" });
  const button = input(c.buttonText, { placeholder: "Sign in with single sign-on", maxlength: 40 });
  const auto = el("input", { type: "checkbox", checked: c.autoCreate ? true : null });
  const byName = el("input", { type: "checkbox", checked: c.matchByName ? true : null });
  const callback = el("code", { class: "mono small" });
  const showCallback = () => { callback.textContent = (publicUrl.value.trim() || location.origin).replace(/\/+$/, "") + "/api/oidc/callback"; };
  publicUrl.addEventListener("input", showCallback);
  showCallback();
  const save = async (body) => {
    try { await api("/api/settings/oidc", { method: "PUT", body }); toast(body.issuer ? "Single sign-on saved" : "Single sign-on turned off"); render(); }
    catch (e) { toast(e.message); }
  };
  return el("form", { class: "form", onsubmit: (ev) => {
    ev.preventDefault();
    save({ issuer: issuer.value, clientId: clientId.value, clientSecret: secret.value, publicUrl: publicUrl.value || location.origin,
      usernameClaim: claim.value, buttonText: button.value, autoCreate: auto.checked, matchByName: byName.checked });
  } },
  el("p", { class: "wide small" }, "Let people sign in with an identity provider as well as a password. Register Kithara with the provider as a web app using this redirect address: ", callback),
  el("label", { class: "field" }, el("span", {}, "Issuer"), issuer),
  el("label", { class: "field" }, el("span", {}, "This server's public address"), publicUrl),
  el("label", { class: "field" }, el("span", {}, "Client ID"), clientId),
  el("label", { class: "field" }, el("span", {}, "Client secret"), secret),
  el("label", { class: "field" }, el("span", {}, "Claim holding the user name"), claim),
  el("label", { class: "field" }, el("span", {}, "Button text"), button),
  el("label", { class: "check wide small" }, auto, "Make an account for someone the provider knows but this server doesn't (as a listener who sees every library)"),
  el("label", { class: "check wide small" }, byName, "Link listener accounts by user name the first time their owner signs in (never admins)"),
  el("p", { class: "wide tiny muted" }, "Existing accounts are linked by their owners, from their Account page. Once linked, people are matched by the provider's own id for them, even if their name changes there. Only link by name if nobody can choose their own user name at the provider."),
  el("div", { class: "wide row end" },
    c.issuer ? el("button", { class: "btn ghost", type: "button", onclick: () => save({ issuer: "" }) }, "Turn off") : null,
    el("button", { class: "btn", type: "submit" }, "Save")));
}

// ------------------------------------------------------------------ activity (admin)

async function viewActivity(tools, days = 30) {
  const [act] = await Promise.all([api("/api/activity?days=" + days), state.books.length ? null : loadLibrary()]);
  const title = (id) => state.books.find((b) => b.id === id)?.title || "A removed book";
  tools.append(el("select", { "aria-label": "Period", style: "width:auto", onchange: (e) => refreshMain((t) => viewActivity(t, Number(e.target.value))) },
    [[7, "Last 7 days"], [30, "Last 30 days"], [90, "Last 90 days"], [365, "Last year"]].map(([v, l]) => el("option", { value: v, selected: v === days ? true : null }, l))));
  return [
    el("div", { class: "table-wrap", style: "margin-bottom:12px" }, el("table", { class: "grid" },
      el("thead", {}, el("tr", {}, el("th", {}, "User"), el("th", { class: "c-num" }, "Listened"), el("th", { class: "c-num hide-sm" }, "Books"), el("th", { class: "c-num hide-sm" }, "Sessions"), el("th", {}, "Last listened"))),
      el("tbody", {}, act.users.map((u) => el("tr", {},
        el("td", {}, el("span", { class: "row", style: "flex-wrap:nowrap" }, el("span", { class: "avatar", style: "width:26px;height:26px;font-size:12px" }, u.name.slice(0, 1)), span("strong", u.name))),
        el("td", { class: "c-num" }, fmtDuration(u.listenedMs)),
        el("td", { class: "c-num hide-sm" }, u.books),
        el("td", { class: "c-num hide-sm" }, u.sessions),
        el("td", { class: "muted" }, u.lastAt ? `${title(u.lastBookId)} · ${relative(u.lastAt)}` : "—")))))),
    el("section", { class: "widget" },
      el("div", { class: "w-head" }, el("h2", {}, "Recent sessions")),
      act.recent.length ? el("table", { class: "grid" },
        el("thead", {}, el("tr", {}, el("th", {}, "When"), el("th", {}, "User"), el("th", {}, "Book"), el("th", { class: "hide-sm" }, "Device"), el("th", { class: "c-num" }, "Listened"), el("th", { class: "hide-sm" }, "From – to"))),
        el("tbody", {}, act.recent.map((r) => el("tr", {},
          el("td", { class: "muted small" }, fmtDateTime(r.endedAt)),
          el("td", {}, r.user),
          el("td", {}, title(r.bookId)),
          el("td", { class: "hide-sm muted" }, r.device || "—"),
          el("td", { class: "c-num" }, fmtDuration(r.listenedMs)),
          el("td", { class: "hide-sm mono small muted" }, `${fmtClock(r.startPositionMs)} – ${fmtClock(r.endPositionMs)}`))))) :
        el("p", { class: "muted", style: "padding:14px" }, "No listening in this period.")),
    el("p", { class: "tiny muted", style: "margin-top:10px" }, "Apps that keep their own listening log send it; for the others it is worked out from progress syncs, so it is an estimate and leans low."),
  ];
}

// ------------------------------------------------------------------ transcripts

// Transcripts are made on the server by whisper.cpp, or on a phone and sent up. The
// page shows where a book stands, reads along with the player, and searches them.

async function loadTranscript(bookId) {
  return api(`/api/books/${encodeURIComponent(bookId)}/transcript`);
}

function transcriptPanel(b) {
  const box = el("div", { class: "panel stack" }, span("small muted", "Transcript: loading…"));
  const draw = async () => {
    const [tx, st] = await Promise.all([loadTranscript(b.id), api("/api/transcription")]).catch(() => [null, null]);
    if (!tx) return box.replaceChildren(span("small muted", "Transcript: could not load"));
    const total = chapterCount(b);
    const done = tx.chapters.filter((c) => c.status === "DONE").length;
    const mine = st.queue.filter((j) => j.bookId === b.id).length;
    const running = st.running?.bookId === b.id ? st.running : null;
    const busy = running || mine > 0;
    const transcribe = async (again) => {
      try {
        const r = await api(`/api/books/${encodeURIComponent(b.id)}/transcribe`, { method: "POST", body: again ? { again: true } : {} });
        toast(r.queued ? `Queued ${plural(r.queued, "chapter")}` : "Nothing left to transcribe");
        txWatch();
        draw();
      } catch (e) { toast(e.message); }
    };
    const stop = async () => {
      try { await api(`/api/books/${encodeURIComponent(b.id)}/transcribe`, { method: "DELETE" }); txWatch(); draw(); } catch (e) { toast(e.message); }
    };
    const actions = el("div", { class: "row" },
      tx.passages.length ? el("button", { class: "btn secondary small", type: "button", onclick: () => openTranscript(b, tx) }, icon("notes"), "Read along") : null,
      st.available && !busy && done < total ? el("button", { class: "btn secondary small", type: "button", onclick: () => transcribe(false) }, icon("magic"), done ? "Transcribe the rest" : "Transcribe on the server") : null,
      st.available && !busy && done === total && isAdmin() ? el("button", { class: "btn ghost small", type: "button", onclick: () => transcribe(true) }, "Transcribe again") : null,
      busy ? el("button", { class: "btn ghost small", type: "button", onclick: stop }, "Stop") : null);
    const models = [...new Set(tx.chapters.map((c) => c.model))].join(", ");
    box.replaceChildren(...[
      el("div", { class: "row" },
        icon("notes"),
        span("strong grow", done === total ? "Transcribed" : done ? `${done} of ${plural(total, "chapter")} transcribed` : "Not transcribed"),
        models ? span("small muted", models) : null),
      running ? el("div", { class: "stack" },
        span("small", `Transcribing chapter ${running.chapter + 1}${mine ? ` · ${plural(mine, "more chapter")} waiting` : ""}`),
        gradientProgress(running.progress)) :
        mine ? span("small muted", `${plural(mine, "chapter")} waiting their turn`) : null,
      !st.available && done < total ? span("tiny muted", "This server can't transcribe: whisper.cpp isn't installed.") : null,
      actions.children.length ? actions : null].filter(Boolean));
    if (busy) setTimeout(() => box.isConnected && draw(), 3000);
  };
  draw();
  return box;
}

const chapterCount = (b) => Math.max(1, b.chapters.length);

// The read-along sheet: the transcript by chapter, the passage being spoken lit and
// kept in view, a tap to play from any passage, and a filter.
const tx = { bookId: null, lines: [], now: -1, userScrolledAt: 0, filter: "" };
for (const ev of ["wheel", "touchmove"]) dialog.addEventListener(ev, () => { tx.userScrolledAt = Date.now(); }, { passive: true });

async function openTranscript(b, loaded) {
  let data = loaded;
  if (!data) {
    try { data = await loadTranscript(b.id); } catch (e) { return toast(e.message); }
  }
  if (!data.passages.length) {
    return openSheet("Transcript · " + b.title, [emptyNote("No transcript yet."), transcriptPanel(b)], { onBack: () => openBook(b.id) });
  }
  const list = el("div", { class: "tx" });
  tx.bookId = b.id;
  tx.lines = [];
  tx.now = -1;
  tx.filter = "";
  let chapter = -1;
  const frag = document.createDocumentFragment();
  for (const p of data.passages) {
    if (p.chapterIndex !== chapter) {
      chapter = p.chapterIndex;
      frag.append(el("h3", { class: "tx-head" }, b.chapters[chapter]?.title || `Part ${chapter + 1}`));
    }
    const line = el("button", { type: "button", class: "tx-line", onclick: () => playFrom(b, p.startMs) },
      span("tx-time", fmtClock(p.startMs)), span("tx-text", p.text));
    tx.lines.push({ el: line, start: p.startMs, text: p.text.toLowerCase() });
    frag.append(line);
  }
  list.append(frag);
  const count = span("small muted", "");
  const search = el("input", { type: "search", placeholder: "Find in this book", "aria-label": "Find in this book", oninput: () => {
    tx.filter = search.value.trim().toLowerCase();
    let n = 0;
    for (const l of tx.lines) { const show = !tx.filter || l.text.includes(tx.filter); l.el.hidden = !show; n += show; }
    list.querySelectorAll(".tx-head").forEach((h) => { h.hidden = !!tx.filter; });
    count.textContent = tx.filter ? plural(n, "passage") : "";
  } });
  const playing = () => player.book?.id === b.id;
  const playBtn = iconBtn(playing() && !player.audio.paused ? "pause" : "play", "Play or pause", () => {
    if (!playing()) return playBook(b.id, undefined, { keepSheet: true });
    player.audio.paused ? player.audio.play() : player.audio.pause();
  });
  openSheet("Transcript · " + b.title, [
    el("div", { class: "toolbar tx-bar" },
      playBtn, iconBtn("replay", "Back 30 seconds", () => playing() && skip(-30)),
      el("div", { class: "search grow" }, icon("search"), search), count),
    list,
  ], { onBack: () => openBook(b.id), wide: true });
  tx.playBtn = playBtn;
  if (playing()) followTranscript(playerPosition(), true);
}

function playFrom(b, ms) {
  if (player.book?.id === b.id) seekTo(ms, true);
  else playBook(b.id, ms, { keepSheet: true });
}

// followTranscript lights the passage at pos, and scrolls to it unless the reader has
// scrolled away in the last few seconds or is filtering.
function followTranscript(pos, jump = false) {
  if (!dialog.open || tx.bookId !== player.book?.id || !tx.lines.length || !tx.lines[0].el.isConnected) return;
  const icon_ = player.audio.paused ? "play" : "pause";
  if (tx.playBtn && tx.playBtn.dataset.icon !== icon_) { tx.playBtn.dataset.icon = icon_; tx.playBtn.replaceChildren(icon(icon_)); }
  let lo = 0, hi = tx.lines.length - 1, at = -1;
  while (lo <= hi) { const mid = (lo + hi) >> 1; if (tx.lines[mid].start <= pos) { at = mid; lo = mid + 1; } else hi = mid - 1; }
  if (at === tx.now) return;
  tx.lines[tx.now]?.el.classList.remove("now");
  tx.now = at;
  const line = tx.lines[at]?.el;
  if (!line) return;
  line.classList.add("now");
  if (!tx.filter && (jump || Date.now() - tx.userScrolledAt > 5000)) line.scrollIntoView({ block: "center", behavior: jump ? "auto" : "smooth" });
}

// Search transcripts: what is said in every transcribed book the user may see.
function searchTranscriptsSheet() {
  const results = el("div", { class: "lines" });
  const input = el("input", { type: "search", placeholder: "A word or a phrase", "aria-label": "Search transcripts", autofocus: true });
  let timer;
  const run = async () => {
    const q = input.value.trim();
    if (q.length < 2) return results.replaceChildren();
    try {
      const r = await api("/api/transcripts/search?q=" + encodeURIComponent(q));
      if (input.value.trim() !== q) return;
      results.replaceChildren(...(r.hits.length ? r.hits.map((h) => {
        const b = state.books.find((x) => x.id === h.bookId);
        if (!b) return null;
        return el("div", { class: "hit", role: "button", tabindex: 0, onclick: () => { openTranscript(b).then(() => playFrom(b, h.startMs)); } },
          span("t", el("div", { class: "strong clip" }, b.title), el("div", { class: "small" }, h.text)),
          span("end", fmtClock(h.startMs)));
      }).filter(Boolean) : [el("div", {}, span("muted", "Nothing found."))]),
      r.more ? el("div", {}, span("tiny muted", "Showing the first 200.")) : "");
    } catch (e) { toast(e.message); }
  };
  input.addEventListener("input", () => { clearTimeout(timer); timer = setTimeout(run, 250); });
  openSheet("Search transcripts", [
    el("p", { class: "small muted" }, "Finds words spoken in the books that have been transcribed, and plays from where they're said. Transcribe a book from its page, or tick some books and choose Transcribe."),
    el("div", { class: "search" }, icon("search"), input), results]);
}

// transcriptionSettings: the engine, the model and the queue, kept up to date while a
// job runs or a model downloads.
function transcriptionSettings() {
  const box = el("div", {}, el("p", { class: "muted" }, "Loading…"));
  const draw = async () => {
    let st;
    try { st = await api("/api/transcription"); } catch (e) { box.replaceChildren(el("p", { class: "err" }, e.message)); return false; }
    await (state.books.length ? null : loadLibrary());
    if (!st.available) {
      box.replaceChildren(
        el("p", { class: "small" }, "Transcribe books on this machine with whisper.cpp, usually far faster than on a phone. The Docker image includes it; otherwise install whisper.cpp so that whisper-cli is on the PATH (or point KITHARA_WHISPER at it), and this section comes alive within a minute."));
      return false;
    }
    const s = st.settings;
    const model = el("select", {}, st.models.map((m) => el("option", { value: m.id, selected: m.id === s.model ? true : null },
      `${m.label}, ${fmtBytes(m.sizeBytes)}${m.installed ? " · downloaded" : ""}`)));
    const threads = el("input", { type: "number", min: 1, max: 64, value: s.threads });
    const lang = el("input", { type: "text", value: s.language, maxlength: 4, placeholder: "auto" });
    const english = () => st.models.find((m) => m.id === model.value)?.englishOnly;
    const syncLang = () => { lang.disabled = english(); if (english()) lang.value = "en"; };
    model.addEventListener("change", syncLang);
    syncLang();
    const cur = st.models.find((m) => m.id === s.model);
    const dl = st.downloads?.[s.model];
    const title = (id) => state.books.find((b) => b.id === id)?.title || "A removed book";
    const form = el("form", { class: "form", onsubmit: async (ev) => {
      ev.preventDefault();
      try {
        await api("/api/settings/transcription", { method: "PUT", body: { model: model.value, threads: Number(threads.value), language: english() ? "" : lang.value.trim() } });
        toast("Transcription settings saved");
        refresh();
      } catch (e) { toast(e.message); }
    } },
    el("label", { class: "field" }, el("span", {}, "Model"), model),
    el("label", { class: "field" }, el("span", {}, "Processor threads"), threads),
    el("label", { class: "field" }, el("span", {}, "Language (a code such as de, or auto)"), lang),
    el("p", { class: "wide tiny muted" }, "Bigger models are slower and more accurate; the English-only ones are the better choice for English books. The phone app uses tiny, base or small."),
    el("div", { class: "wide row end" }, el("button", { class: "btn", type: "submit" }, "Save")));

    const model_ = el("div", { class: "row", style: "margin:4px 0 12px" },
      dl ? (dl.error ? span("small err grow", "Download failed: " + dl.error) : el("div", { class: "grow stack" }, span("small", `Downloading ${cur.label}: ${fmtBytes(dl.got)} of ${fmtBytes(dl.total)}`), gradientProgress(dl.total ? dl.got / dl.total : 0))) :
        span("small grow " + (st.ready ? "" : "err"), st.ready ? `${cur.label} is downloaded and ready.` : `${cur.label} isn't downloaded yet; nothing is transcribed until it is.`),
      !st.ready && (!dl || dl.error) ? el("button", { class: "btn secondary small", type: "button", onclick: async () => {
        try { await api(`/api/transcription/models/${encodeURIComponent(s.model)}`, { method: "POST" }); refresh(); } catch (e) { toast(e.message); }
      } }, icon("download"), "Download " + fmtBytes(cur.sizeBytes)) : null);

    const whole = el("button", { class: "btn secondary small", type: "button", onclick: async () => {
      if (!confirm("Transcribe every chapter of every book not transcribed yet? On a big library this runs for days; it can be stopped at any time.")) return;
      try { const r = await api("/api/transcription/library", { method: "POST" }); toast(`Queued ${plural(r.queued, "chapter")}`); txWatch(); refresh(); } catch (e) { toast(e.message); }
    } }, icon("magic"), "Transcribe the whole library");
    const waiting = st.waiting.reduce((n, w) => n + w.chapters, 0);
    box.replaceChildren(
      model_, form,
      el("div", { class: "panel row", style: "margin-top:12px" },
        span("small grow", st.running ? `Transcribing ${title(st.running.bookId)}` + (waiting ? `, ${plural(waiting, "more chapter")} waiting` : "") :
          waiting ? `${plural(waiting, "chapter")} waiting` : "Nothing being transcribed."),
        st.running || waiting || st.lastError ? el("button", { class: "btn ghost small", type: "button", onclick: openQueue }, "Open the queue") : null,
        whole),
      el("p", { class: "tiny muted", style: "margin-top:8px" }, st.vad ? "Speech is found with whisper.cpp's voice-activity detector, so passages begin where speech does." : "This whisper.cpp has no voice-activity detector; passages follow Whisper's own timing."));
    return !!(dl && !dl.error);
  };
  // Poll while something is happening; an action starts polling again.
  let polling = false;
  const loop = async () => {
    if (await draw() && box.isConnected) setTimeout(loop, 2000);
    else polling = false;
  };
  const refresh = () => { if (!polling) { polling = true; loop(); } else draw(); };
  refresh();
  return box;
}

// ------------------------------------------------------------------ transcription status

// One poller for everything that shows transcription in progress: the chip in the top
// bar, the queue sheet, and the markers on books. Every few seconds while something is
// queued, once a minute otherwise.
const txq = { st: null, timer: 0, chip: null };

function txWatch(delay = 0) {
  clearTimeout(txq.timer);
  txq.timer = setTimeout(txPoll, delay);
}

async function txPoll() {
  if (!state.token) return;
  const gen = state.gen;
  let st = null;
  try { st = await api("/api/transcription"); } catch { /* look again later */ }
  if (gen !== state.gen) return;
  txq.st = st;
  txDraw();
  txq.timer = setTimeout(txPoll, txBusy() ? 4000 : 60000);
}

const txBusy = () => !!(txq.st?.available && (txq.st.running || txq.st.waiting.length));
const txWaitingChapters = () => (txq.st?.waiting || []).reduce((n, w) => n + w.chapters, 0);

function txChip() {
  if (!txq.chip) {
    txq.chip = el("button", { class: "tx-chip", type: "button", title: "Transcription queue", onclick: openQueue },
      el("span", { class: "tx-ring", "aria-hidden": "true" }), span("tx-chip-text", ""));
    txq.chip.hidden = true;
  }
  return txq.chip;
}

function txDraw() {
  const st = txq.st;
  const chip = txChip();
  chip.hidden = !txBusy();
  if (txBusy()) {
    const waiting = txWaitingChapters();
    const p = st.running ? st.running.progress : 0;
    chip.style.setProperty("--p", p.toFixed(3));
    chip.classList.toggle("idle", !st.running);
    chip.querySelector(".tx-chip-text").textContent = (st.running ? `Transcribing ${Math.floor(p * 100)}%` : "Transcription waiting") +
      (waiting ? ` · ${waiting} waiting` : "");
  }
  document.querySelectorAll(".tx-mark").forEach(txMarkDraw);
  document.querySelectorAll(".tx-banner").forEach(txBannerDraw);
  if (dialog.open && dialog.dataset.sheet === "queue") drawQueue();
}

// txMark is a book's marker: shown while it is being transcribed or waiting to be.
function txMark(bookId) {
  const m = el("span", { class: "tx-mark", "data-book": bookId });
  txMarkDraw(m);
  return m;
}

function txMarkDraw(m) {
  const st = txq.st, id = m.dataset.book;
  const running = st?.running?.bookId === id;
  const waiting = !running && st?.waiting.some((w) => w.bookId === id);
  m.hidden = !running && !waiting;
  m.classList.toggle("now", running);
  m.replaceChildren(...(running || waiting ? [icon("notes"), running ? "Transcribing" : "Waiting to transcribe"] : []));
}

// txBanner is the library's line about transcription in progress, above the filters.
function txBanner() {
  const b = el("div", { class: "tx-banner panel row" });
  txBannerDraw(b);
  return b;
}

function txBannerDraw(node) {
  node.hidden = !txBusy();
  if (node.hidden) return;
  const st = txq.st, r = st.running, waiting = txWaitingChapters();
  const book = r && state.books.find((x) => x.id === r.bookId);
  const { left } = txTimes();
  node.replaceChildren(
    icon("notes"),
    el("div", { class: "grow stack" },
      span("small", r ? `Transcribing ${book?.title || "a book"}, chapter ${r.chapter + 1} · ${Math.floor(r.progress * 100)}%` +
        (left ? ` · about ${fmtLeft(left)} left` : "") + (waiting ? ` · ${waiting} more waiting` : "") :
        `${plural(waiting, "chapter")} waiting to be transcribed`),
      r ? gradientProgress(r.progress) : null),
    el("button", { class: "btn ghost small", type: "button", onclick: openQueue }, "Open the queue"));
}

// txTimes is the running chapter's speed so far (audio per wall-clock ms) and time left.
function txTimes() {
  const r = txq.st?.running;
  if (!r) return { speed: 0, left: 0 };
  const len = chapterMs(state.books.find((b) => b.id === r.bookId), r.chapter), spent = Date.now() - r.startedAt;
  const speed = r.progress > 0.02 && spent > 5000 ? (len * r.progress) / spent : 0;
  return { speed, left: speed ? (len * (1 - r.progress)) / speed : 0 };
}

const chapterMs = (b, i) => {
  const c = b?.chapters[i];
  return c ? c.endMs - c.startMs : b?.durationMs || 0;
};

const fmtLeft = (ms) => ms < 60000 ? "under a minute" : fmtDuration(ms);

function openQueue() {
  openSheet("Transcription queue", [el("div", { id: "txq-body", class: "stack" }, el("p", { class: "muted" }, "Loading…"))]);
  dialog.dataset.sheet = "queue";
  drawQueue();
  txWatch();
}

// drawQueue fills the queue sheet from the last status: the chapter being transcribed,
// with time left, and the books waiting, each with Stop where the reader may stop it.
function drawQueue() {
  const body = document.getElementById("txq-body");
  const st = txq.st;
  if (!body || !st) return;
  const book = (id) => state.books.find((b) => b.id === id);
  const stop = (id, label = "Stop") => el("button", { class: "btn ghost small", type: "button", onclick: async () => {
    try { await api(`/api/books/${encodeURIComponent(id)}/transcribe`, { method: "DELETE" }); txWatch(); } catch (e) { toast(e.message); }
  } }, label);
  const parts = [];
  const { speed, left: leftNow } = txTimes();
  if (st.running) {
    const r = st.running, b = book(r.bookId);
    const len = chapterMs(b, r.chapter), left = leftNow;
    parts.push(el("div", { class: "panel row tx-now" },
      b ? cover(b) : null,
      el("div", { class: "grow stack" },
        el("div", { class: "strong clip" }, b?.title || "A book you can't see"),
        span("small muted", `Chapter ${r.chapter + 1}` + (b?.chapters[r.chapter] ? `: ${b.chapters[r.chapter].title}` : "") + ` · ${fmtDuration(len)} of audio`),
        gradientProgress(r.progress),
        span("small", `${Math.floor(r.progress * 100)}%` + (left ? ` · about ${fmtLeft(left)} left` : ""))),
      isAdmin() || r.mine ? stop(r.bookId) : null));
  }
  if (st.waiting.length) {
    const total = txWaitingChapters();
    // An estimate for everything, from the chapters listed (the first 500).
    let rest = 0;
    if (speed && total <= st.queue.length) for (const j of st.queue) rest += chapterMs(book(j.bookId), j.chapter);
    parts.push(el("div", { class: "row" },
      el("h3", { class: "section-title grow", style: "margin:6px 0 0" }, `Waiting · ${plural(total, "chapter")}`),
      rest ? span("small muted", `about ${fmtLeft(leftNow + rest / speed)} for all of it`) : null));
    parts.push(el("div", { class: "lines" }, st.waiting.map((w) => {
      const b = book(w.bookId);
      return el("div", {},
        span("t clip", b?.title || "A book you can't see"),
        span("small muted", plural(w.chapters, "chapter") + (!isAdmin() && w.mine && w.mine < w.chapters ? ` (${w.mine} yours)` : "")),
        isAdmin() || w.mine ? stop(w.bookId) : null);
    })));
    if (!st.running) parts.push(el("p", { class: "small err" }, !st.ready ?
      "Nothing runs until the transcription model is downloaded" + (isAdmin() ? ": Settings, Transcription." : "; ask the server's admin.") :
      "Starting soon."));
  }
  if (!st.running && !st.waiting.length) parts.push(emptyNote("Nothing being transcribed. Transcribe a book from its page, or tick some books in the library and choose Transcribe."));
  if (isAdmin() && st.lastError) parts.push(el("p", { class: "small err" }, "Last error: " + st.lastError));
  if (isAdmin()) parts.push(el("div", { class: "row end" },
    el("button", { class: "btn ghost small", type: "button", onclick: () => { dialog.close(); go("settings"); } }, icon("settings"), "Transcription settings"),
    st.running || st.waiting.length ? el("button", { class: "btn ghost small", type: "button", onclick: async () => {
      if (!confirm("Stop transcribing and empty the whole queue?")) return;
      try { await api("/api/transcription/queue", { method: "DELETE" }); txWatch(); } catch (e) { toast(e.message); }
    } }, "Stop everything") : null));
  body.replaceChildren(...parts);
}

// ------------------------------------------------------------------ collections and playlists

// Collections are shared (admins make them, everyone sees the books in them they may
// see); playlists are each person's own.
const LIST_NOUN = { collections: "collection", playlists: "playlist" };

async function loadLists() {
  const [c, p] = await Promise.all([api("/api/collections"), api("/api/playlists")]);
  return { collections: c.collections, playlists: p.playlists };
}

async function viewLists(tools) {
  const [lists] = await Promise.all([loadLists(), loadLibrary()]);
  tools.append(el("button", { class: "btn secondary small", type: "button", onclick: () => listSheet("playlists") }, icon("add"), "New playlist"));
  if (isAdmin()) tools.append(el("button", { class: "btn secondary small", type: "button", onclick: () => listSheet("collections") }, icon("add"), "New collection"));
  const section = (kind, title, note, editable) => [
    el("h2", { class: "section-title" }, title),
    lists[kind].length ? el("div", { class: "widgets" }, lists[kind].map((l) => listWidget(kind, l, editable))) : emptyNote(note),
  ];
  return [
    ...section("collections", "Collections", isAdmin() ? "No collections yet. Make one to group books for everyone, like a series in reading order." : "No collections yet.", isAdmin()),
    ...section("playlists", "My playlists", "No playlists yet. Add books to one from a book's page, or make one here. Only you see them.", true),
  ];
}

function listWidget(kind, l, editable) {
  const books = l.bookIds.map((id) => state.books.find((b) => b.id === id)).filter(Boolean);
  const total = books.reduce((n, b) => n + (b.durationMs || 0), 0);
  return widget(l.name, {
    wide: true,
    actions: editable ? [iconBtn("edit", "Edit " + LIST_NOUN[kind], () => listSheet(kind, l))] : [],
    body: [
      l.description ? el("p", { class: "small muted", style: "margin-bottom:10px" }, l.description) : null,
      books.length ? el("div", { class: "cover-grid" }, books.map(bookTile)) : emptyNote("Nothing in it yet."),
      el("p", { class: "tiny muted", style: "margin-top:8px" }, `${plural(books.length, "book")} · ${fmtDuration(total)}`),
    ],
  });
}

// hiddenIf keeps a button's place in its row while hiding it, so columns line up.
const hiddenIf = (hide, btn) => { if (hide) btn.style.visibility = "hidden"; return btn; };

// listSheet makes a list, or edits one: its name, description, and the order of books.
function listSheet(kind, l = null) {
  const noun = LIST_NOUN[kind];
  const name = el("input", { type: "text", value: l?.name || "", maxlength: 100, required: true, autofocus: true });
  const desc = el("textarea", { rows: 2, maxlength: 2000 });
  desc.value = l?.description || "";
  const ids = [...(l?.bookIds || [])];
  const rows = el("div", { class: "lines" });
  const draw = () => rows.replaceChildren(...ids.map((id, i) => {
    const b = state.books.find((x) => x.id === id);
    const move = (d) => { [ids[i], ids[i + d]] = [ids[i + d], ids[i]]; draw(); };
    return el("div", {},
      span("n", i + 1), span("t clip", b?.title || "A removed book"),
      el("span", { class: "end row", style: "flex-wrap:nowrap;gap:2px" },
        hiddenIf(i === 0, iconBtn("up", "Move up", () => move(-1))),
        hiddenIf(i === ids.length - 1, iconBtn("down", "Move down", () => move(1))),
        iconBtn("remove", "Take out", () => { ids.splice(i, 1); draw(); })));
  }));
  draw();
  const done = () => { dialog.close(); if (state.view === "lists") render(); };
  const form = el("form", { class: "form", onsubmit: async (ev) => {
    ev.preventDefault();
    const body = { name: name.value, description: desc.value, bookIds: ids };
    try {
      await api(l ? `/api/${kind}/${encodeURIComponent(l.id)}` : `/api/${kind}`, { method: l ? "PATCH" : "POST", body });
      toast(l ? "Saved" : `Made the ${noun}`);
      done();
    } catch (e) { toast(e.message); }
  } },
  el("label", { class: "field wide" }, el("span", {}, "Name"), name),
  el("label", { class: "field wide" }, el("span", {}, "Description"), desc),
  l ? el("div", { class: "wide" }, ids.length ? rows : emptyNote("Add books from their pages: “Add to a list”.")) : null,
  el("div", { class: "wide row end" },
    l ? el("button", { class: "btn ghost", type: "button", onclick: async () => {
      if (!confirm(`Delete the ${noun} “${l.name}”? The books stay in the library.`)) return;
      try { await api(`/api/${kind}/${encodeURIComponent(l.id)}`, { method: "DELETE" }); toast("Deleted"); done(); } catch (e) { toast(e.message); }
    } }, icon("remove"), "Delete") : null,
    el("button", { class: "btn", type: "submit" }, l ? "Save" : "Make " + noun)));
  openSheet(l ? `Edit ${noun} · ${l.name}` : `New ${noun}`, [form]);
}

// addToList puts a book in (or takes it out of) the user's playlists, and an admin's
// collections.
async function addToList(b) {
  let lists;
  try { lists = await loadLists(); } catch (e) { return toast(e.message); }
  const row = (kind, l) => {
    const box = el("input", { type: "checkbox", checked: l.bookIds.includes(b.id) ? true : null, onchange: async () => {
      box.disabled = true;
      const bookIds = box.checked ? [...l.bookIds, b.id] : l.bookIds.filter((x) => x !== b.id);
      try { Object.assign(l, await api(`/api/${kind}/${encodeURIComponent(l.id)}`, { method: "PATCH", body: { bookIds } })); }
      catch (e) { toast(e.message); box.checked = !box.checked; }
      box.disabled = false;
    } });
    return el("label", { class: "check" }, box, l.name);
  };
  const group = (kind, title) => [
    el("h3", { class: "section-title" }, title),
    lists[kind].length ? el("div", { class: "stack" }, lists[kind].map((l) => row(kind, l))) : emptyNote(`No ${kind} yet.`),
  ];
  const fresh = el("input", { type: "text", placeholder: "New playlist", maxlength: 100, "aria-label": "New playlist name" });
  const make = el("form", { class: "row", style: "margin-top:10px", onsubmit: async (ev) => {
    ev.preventDefault();
    if (!fresh.value.trim()) return;
    try { await api("/api/playlists", { method: "POST", body: { name: fresh.value, bookIds: [b.id] } }); addToList(b); }
    catch (e) { toast(e.message); }
  } }, el("div", { class: "grow" }, fresh), el("button", { class: "btn secondary", type: "submit" }, icon("add"), "Make"));
  openSheet("Add to a list · " + b.title, [
    ...group("playlists", "My playlists"), make,
    isAdmin() ? group("collections", "Collections (everyone sees these)") : null,
  ].flat(), { onBack: () => openBook(b.id) });
}

// ------------------------------------------------------------------ start

async function start() {
  state.me = await api("/api/me");
  drawPlayer();
  txWatch();
  const hash = location.hash.slice(1);
  if (VIEWS.some((v) => v.id === hash)) state.view = hash;
  render();
}

(async () => {
  loadPrefs();
  applyTheme();
  try { state.server = await api("/api/health"); } catch { /* shown as "Kithara" */ }
  document.title = state.server?.name ? `${state.server.name} · Kithara` : "Kithara";
  if (await ssoReturn()) return;
  state.token = loadToken();
  if (!state.token) return renderSignIn();
  try { await start(); } catch (e) { if (e.status !== 401) renderSignIn(e.message); }
})();
