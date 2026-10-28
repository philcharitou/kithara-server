// The kithara-server web page. Plain modules, no build step, no dependencies. It signs
// in like an app and uses the same API; every bit of text from the server is set with
// textContent, never parsed as HTML. Its components follow the Android app's Compose
// screens: BrandTopBar, FilterChip rows, the library list and grid, the Sources screen,
// StatsScreen's cards, and a NavigationBar (a rail on wide screens).

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
  prefs: { layout: "list", sort: "recent", filter: "all", library: "", theme: "system" },
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

// Material icons (the same set the app uses), drawn into inline SVG.
const ICONS = {
  library: "M4 6H2v14c0 1.1.9 2 2 2h14v-2H4V6zm16-4H8c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h12c1.1 0 2-.9 2-2V4c0-1.1-.9-2-2-2zm-1 9H9V9h10v2zm-4 4H9v-2h6v2zm4-8H9V5h10v2z",
  stats: "M3.5 18.49l6-6.01 4 4L22 6.92l-1.41-1.41-7.09 7.97-4-4L2 16.99z",
  devices: "M4 6h18V4H4c-1.1 0-2 .9-2 2v11H0v3h14v-3H4V6zm19 2h-6c-.55 0-1 .45-1 1v10c0 .55.45 1 1 1h6c.55 0 1-.45 1-1V9c0-.55-.45-1-1-1zm-1 9h-4v-7h4v7z",
  users: "M16 11c1.66 0 2.99-1.34 2.99-3S17.66 5 16 5c-1.66 0-3 1.34-3 3s1.34 3 3 3zm-8 0c1.66 0 2.99-1.34 2.99-3S9.66 5 8 5C6.34 5 5 6.34 5 8s1.34 3 3 3zm0 2c-2.33 0-7 1.17-7 3.5V19h14v-2.5c0-2.33-4.67-3.5-7-3.5zm8 0c-.29 0-.62.02-.97.05 1.16.84 1.97 1.97 1.97 3.45V19h6v-2.5c0-2.33-4.67-3.5-7-3.5z",
  search: "M15.5 14h-.79l-.28-.27C15.41 12.59 16 11.11 16 9.5 16 5.91 13.09 3 9.5 3S3 5.91 3 9.5 5.91 16 9.5 16c1.61 0 3.09-.59 4.23-1.57l.27.28v.79l5 4.99L20.49 19l-4.99-5zm-6 0C7.01 14 5 11.99 5 9.5S7.01 5 9.5 5 14 7.01 14 9.5 11.99 14 9.5 14z",
  check: "M9 16.17L4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41z",
  sort: "M16 17.01V10h-2v7.01h-3L15 21l4-3.99h-3zM9 3L5 6.99h3V14h2V6.99h3L9 3z",
  grid: "M3 3v8h8V3H3zm6 6H5V5h4v4zm-6 4v8h8v-8H3zm6 6H5v-4h4v4zm4-16v8h8V3h-8zm6 6h-4V5h4v4zm-6 4v8h8v-8h-8zm6 6h-4v-4h4v4z",
  list: "M3 13h2v-2H3v2zm0 4h2v-2H3v2zm0-8h2V7H3v2zm4 4h14v-2H7v2zm0 4h14v-2H7v2zM7 7v2h14V7H7z",
  down: "M16.59 8.59L12 13.17 7.41 8.59 6 10l6 6 6-6z",
  back: "M20 11H7.83l5.59-5.59L12 4l-8 8 8 8 1.41-1.41L7.83 13H20v-2z",
  close: "M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z",
  download: "M19 9h-4V3H9v6H5l7 7 7-7zM5 18v2h14v-2H5z",
  refresh: "M17.65 6.35C16.2 4.9 14.21 4 12 4c-4.42 0-7.99 3.58-7.99 8s3.57 8 7.99 8c3.73 0 6.84-2.55 7.73-6h-2.08c-.82 2.33-3.04 4-5.65 4-3.31 0-6-2.69-6-6s2.69-6 6-6c1.66 0 3.14.69 4.22 1.78L13 11h7V4l-2.35 2.35z",
  logout: "M17 7l-1.41 1.41L18.17 11H8v2h10.17l-2.58 2.58L17 17l5-5zM4 5h8V3H4c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h8v-2H4V5z",
  edit: "M3 17.25V21h3.75L17.81 9.94l-3.75-3.75L3 17.25zM20.71 7.04a1 1 0 000-1.41l-2.34-2.34a1 1 0 00-1.41 0l-1.83 1.83 3.75 3.75 1.83-1.83z",
  magic: "M7.5 5.6L10 7 8.6 4.5 10 2 7.5 3.4 5 2l1.4 2.5L5 7zm12 9.8L17 14l1.4 2.5L17 19l2.5-1.4L22 19l-1.4-2.5L22 14zM22 2l-2.5 1.4L17 2l1.4 2.5L17 7l2.5-1.4L22 7l-1.4-2.5zm-7.63 5.29a.996.996 0 00-1.41 0L1.29 18.96a.996.996 0 000 1.41l2.34 2.34c.39.39 1.02.39 1.41 0L16.7 11.05a.996.996 0 000-1.41l-2.33-2.35zm-1.03 5.49l-2.12-2.12 2.44-2.44 2.12 2.12-2.44 2.44z",
  chapters: "M3 13h2v-2H3v2zm0 4h2v-2H3v2zm0-8h2V7H3v2zm4 4h14v-2H7v2zm0 4h14v-2H7v2zM7 7v2h14V7H7z",
  files: "M14 2H6c-1.1 0-1.99.9-1.99 2L4 20c0 1.1.89 2 1.99 2H18c1.1 0 2-.9 2-2V8l-6-6zm2 16H8v-2h8v2zm0-4H8v-2h8v2zm-3-5V3.5L18.5 9H13z",
  folder: "M10 4H4c-1.1 0-1.99.9-1.99 2L2 18c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2h-8l-2-2z",
  remove: "M6 19c0 1.1.9 2 2 2h8c1.1 0 2-.9 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z",
  add: "M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6v2z",
  up: "M4 12l1.41 1.41L11 7.83V20h2V7.83l5.58 5.59L20 12l-8-8-8 8z",
  phone: "M17 1.01L7 1c-1.1 0-2 .9-2 2v18c0 1.1.9 2 2 2h10c1.1 0 2-.9 2-2V3c0-1.1-.9-1.99-2-1.99zM17 19H7V5h10v14z",
  tablet: "M21 4H3c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h18c1.1 0 1.99-.9 1.99-2L23 6c0-1.1-.9-2-2-2zm-2 14H5V6h14v12z",
  computer: "M20 18c1.1 0 1.99-.9 1.99-2L22 6c0-1.1-.9-2-2-2H4c-1.1 0-2 .9-2 2v10c0 1.1.9 2 2 2H0v2h24v-2h-4zM4 6h16v10H4V6z",
  web: "M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-1 17.93c-3.95-.49-7-3.85-7-7.93 0-.62.08-1.21.21-1.79L9 15v1c0 1.1.9 2 2 2v1.93zm6.9-2.54c-.26-.81-1-1.39-1.9-1.39h-1v-3c0-.55-.45-1-1-1H8v-2h2c.55 0 1-.45 1-1V7h2c1.1 0 2-.9 2-2v-.41c2.93 1.19 5 4.06 5 7.41 0 2.08-.8 3.97-2.1 5.39z",
  dark: "M12 3a9 9 0 109 9c0-.46-.04-.92-.1-1.36a5.389 5.389 0 01-4.4 2.26 5.403 5.403 0 01-3.14-9.8c-.44-.06-.9-.1-1.36-.1z",
  light: "M12 7c-2.76 0-5 2.24-5 5s2.24 5 5 5 5-2.24 5-5-2.24-5-5-5zM2 13h2c.55 0 1-.45 1-1s-.45-1-1-1H2c-.55 0-1 .45-1 1s.45 1 1 1zm18 0h2c.55 0 1-.45 1-1s-.45-1-1-1h-2c-.55 0-1 .45-1 1s.45 1 1 1zM11 2v2c0 .55.45 1 1 1s1-.45 1-1V2c0-.55-.45-1-1-1s-1 .45-1 1zm0 18v2c0 .55.45 1 1 1s1-.45 1-1v-2c0-.55-.45-1-1-1s-1 .45-1 1zM5.99 4.58a.996.996 0 00-1.41 0 .996.996 0 000 1.41l1.06 1.06c.39.39 1.03.39 1.41 0s.39-1.03 0-1.41L5.99 4.58zm12.37 12.37a.996.996 0 00-1.41 0 .996.996 0 000 1.41l1.06 1.06c.39.39 1.03.39 1.41 0a.996.996 0 000-1.41l-1.06-1.06zm1.06-10.96a.996.996 0 000-1.41.996.996 0 00-1.41 0l-1.06 1.06c-.39.39-.39 1.03 0 1.41s1.03.39 1.41 0l1.06-1.06zM7.05 18.36a.996.996 0 000-1.41.996.996 0 00-1.41 0l-1.06 1.06c-.39.39-.39 1.03 0 1.41s1.03.39 1.41 0l1.06-1.06z",
  auto: "M10.85 12.65h2.3L12 9l-1.15 3.65zM20 8.69V4h-4.69L12 .69 8.69 4H4v4.69L.69 12 4 15.31V20h4.69L12 23.31 15.31 20H20v-4.69L23.31 12 20 8.69zM14.3 16l-.7-2h-3.2l-.7 2H7.8L11 7h2l3.2 9h-1.9z",
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

const text = (cls, ...children) => el("span", { class: cls }, ...children);

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
}

function savePrefs() {
  try { storage()?.setItem(PREFS_KEY, JSON.stringify(state.prefs)); } catch { /* not kept */ }
}

// The theme, as the app's setting: follow the system, or always light, or always dark.
const THEMES = { system: ["Follow system", "auto"], light: ["Light", "light"], dark: ["Dark", "dark"] };
function applyTheme() {
  const t = state.prefs.theme;
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
  else delete document.documentElement.dataset.theme;
  const dark = t === "dark" || (t !== "light" && matchMedia("(prefers-color-scheme: dark)").matches);
  document.querySelector('meta[name="theme-color"]')?.setAttribute("content", dark ? "#141218" : "#fdf8ff");
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
  const body2 = await res.text();
  const data = body2 ? JSON.parse(body2) : null;
  if (!res.ok) throw new ApiError(res.status, data?.error || res.statusText);
  return data;
}

// Format.kt's shapes: "4h 1m", "52m".
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
    box.replaceWith(el("img", { class: "cover", src: blobURL, alt: "" }));
  } catch { /* keep the stand-in */ }
}

// The app's stand-in for a book without artwork: surface-variant, the title's first two
// letters. A finished book wears the FinishedBadge in the corner.
function cover(book) {
  const box = el("div", { class: "cover blank", "aria-hidden": "true" }, (book.title || "").slice(0, 2));
  if (book.coverUrl) {
    // updatedAt in the address, so a changed cover is fetched again.
    box.dataset.src = book.coverUrl + "?v=" + book.updatedAt;
    coverObserver.observe(box);
  }
  const finished = book.id && progressOf(book)?.finished;
  return el("div", { class: "cover-box" }, box, finished ? el("span", { class: "finished", title: "Finished" }, icon("check")) : null);
}

// ------------------------------------------------------------------ sign in

function renderSignIn(message = "") {
  dialog.open && dialog.close();
  const err = el("p", { class: "body-medium error-text", role: "alert" }, message);
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
    el("label", { class: "field" }, el("span", {}, "User name"), user),
    el("label", { class: "field" }, el("span", {}, "Password"), pass),
    el("label", { class: "check body-medium" }, remember, "Keep me signed in on this browser"),
    err,
    submit,
  );
  app.replaceChildren(el("div", { class: "signin" },
    el("img", { class: "mark", src: "logo.svg", alt: "" }),
    el("div", {},
      el("h1", { class: "headline-small" }, state.server?.name || "Kithara"),
      el("p", { class: "body-medium variant", style: "margin-top:8px" }, "Sign in with your Kithara account. Accounts are made by whoever runs this server.")),
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
  { id: "stats", label: "Listening", icon: "stats", narrow: true },
  { id: "devices", label: "Devices", icon: "devices", narrow: true },
  { id: "users", label: "Users", icon: "users", narrow: true, admin: true },
];

function renderShell(current) {
  const nav = el("nav", { class: "bar", "aria-label": "Sections" },
    VIEWS.filter((v) => !v.admin || isAdmin()).map((v) =>
      el("button", { type: "button", "aria-current": state.view === v.id ? "page" : null, onclick: () => go(v.id) },
        el("span", { class: "pill" }, icon(v.icon)), v.label)));
  const [themeLabel, themeIcon] = THEMES[state.prefs.theme] || THEMES.system;
  const themeBtn = el("button", { class: "icon-btn", type: "button", title: `Theme: ${themeLabel}`, "aria-label": `Theme: ${themeLabel}`, onclick: () => {
    const order = Object.keys(THEMES);
    state.prefs.theme = order[(order.indexOf(state.prefs.theme) + 1) % order.length];
    savePrefs(); applyTheme();
    const [label, ic] = THEMES[state.prefs.theme];
    themeBtn.replaceChildren(icon(ic));
    themeBtn.title = `Theme: ${label}`;
    themeBtn.setAttribute("aria-label", `Theme: ${label}`);
    toast(`Theme: ${label}`);
  } }, icon(themeIcon));
  const top = el("header", { class: "topbar" },
    el("img", { class: "mark", src: "logo.svg", alt: "" }),
    el("h1", { class: "title-large clip" }, current.label),
    current.id === "library" ? el("span", { id: "top-actions", class: "row", style: "gap:0;flex-wrap:nowrap" }) : null,
    themeBtn,
    el("span", { class: "who", title: `${state.me.name}${isAdmin() ? " (admin)" : ""}` },
      el("span", { class: "avatar", "aria-hidden": "true" }, state.me.name.slice(0, 1)),
      text("label-large clip", state.me.name)),
    el("button", { class: "icon-btn", type: "button", title: "Sign out", "aria-label": "Sign out", onclick: signOut }, icon("logout")));
  const main = el("main", { id: "main", class: current.narrow ? "content narrow" : "content" });
  app.replaceChildren(nav, el("div", { class: "frame" }, top, main));
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
  main.append(el("p", { class: "body-medium variant", style: "padding:16px" }, "Loading…"));
  try {
    const view = { library: viewLibrary, stats: viewStats, devices: viewDevices, users: viewUsers }[current.id];
    main.replaceChildren(...[await view()].flat().filter(Boolean));
  } catch (e) {
    if (e.status !== 401) main.replaceChildren(el("p", { class: "body-medium error-text", style: "padding:16px" }, e.message));
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

// The app's statusLine: "43% · 4h 1m left", "Not started · 8h 52m", "Finished".
function statusLine(b) {
  const p = progressOf(b);
  const s = statusOf(b);
  if (s === "finished") return text("label-small status primary-text", "Finished");
  if (s === "progress") return text("label-small status variant", `${Math.round(p.fraction * 100)}% · ${fmtDuration(b.durationMs - p.positionMs)} left`);
  return text("label-small status variant", `Not started · ${fmtDuration(b.durationMs)}`);
}

function gradientProgress(fraction, thick = false) {
  return el("div", { class: "progress" + (thick ? " thick" : ""), role: "progressbar", "aria-valuenow": Math.round(fraction * 100), "aria-valuemin": 0, "aria-valuemax": 100 },
    el("i", { style: `width:${Math.max(1, fraction * 100).toFixed(1)}%` }));
}

function bookProgress(b) {
  const p = progressOf(b);
  return p && !p.finished && p.positionMs > 0 ? gradientProgress(p.fraction) : null;
}

function chip(label, on, onclick) {
  return el("button", { class: "chip", type: "button", "aria-pressed": on ? "true" : "false", onclick }, on ? icon("check") : null, label);
}

async function viewLibrary() {
  await loadLibrary();
  const prefs = state.prefs;
  const shelf = el("div");
  const count = text("label-large grow");

  if (!state.libraries.length) {
    return el("div", { class: "empty" },
      el("img", { class: "mark", src: "logo.svg", alt: "" }),
      el("h2", { class: "headline-small" }, "No libraries yet"),
      el("p", { class: "body-medium variant" }, isAdmin()
        ? "Point the server at a folder of audiobooks and they will show up here, and in the apps."
        : "Whoever runs this server hasn't added a library yet."),
      isAdmin() ? el("button", { class: "btn", type: "button", onclick: openLibraries }, icon("add"), "Add a library") : null);
  }

  const fill = () => {
    const shown = state.books
      .filter((b) => !prefs.library || b.library === prefs.library)
      .filter((b) => prefs.filter === "all" || statusOf(b) === prefs.filter)
      .filter((b) => matches(b, state.query))
      .sort((SORTS[prefs.sort] || SORTS.recent)[1]);
    const total = state.books.length;
    count.textContent = shown.length === total ? plural(total, "title") : `${shown.length} of ${total} titles`;
    if (!shown.length) {
      shelf.className = "";
      shelf.replaceChildren(el("div", { class: "empty" }, el("p", { class: "body-medium variant" },
        total ? "Nothing here matches." : "This library is empty. Add audiobook folders to it, then rescan.")));
      return;
    }
    shelf.className = prefs.layout === "grid" ? "card-grid" : "rows-list";
    shelf.replaceChildren(...shown.map(prefs.layout === "grid" ? bookCard : bookRow));
  };

  const filters = el("div", { class: "chips filters", role: "group", "aria-label": "Show" });
  const drawChips = () => {
    const chips = [["all", "All"], ["progress", "In progress"], ["new", "Not started"], ["finished", "Finished"]].map(([id, label]) =>
      chip(label, prefs.filter === id, () => { prefs.filter = id; savePrefs(); drawChips(); fill(); }));
    if (state.libraries.length > 1) {
      chips.push(el("span", { style: "width:1px;background:var(--outline-variant);margin:4px 4px;flex:none" }));
      for (const l of state.libraries) {
        chips.push(chip(l.name, prefs.library === l.name, () => { prefs.library = prefs.library === l.name ? "" : l.name; savePrefs(); drawChips(); fill(); }));
      }
    }
    filters.replaceChildren(...chips);
  };
  drawChips();

  const sortLabel = text("", (SORTS[prefs.sort] || SORTS.recent)[0]);
  const sortBtn = el("button", { class: "btn text", type: "button", title: "Sort by", onclick: () => {
    const keys = Object.keys(SORTS);
    prefs.sort = keys[(keys.indexOf(prefs.sort) + 1) % keys.length];
    sortLabel.textContent = SORTS[prefs.sort][0];
    savePrefs(); fill();
  } }, icon("sort"), sortLabel);
  const layoutBtns = [["list", "List"], ["grid", "Grid"]].map(([id, label], i) => el("button", {
    class: "icon-btn", type: "button", title: label, "aria-label": label, "aria-pressed": prefs.layout === id ? "true" : "false",
    onclick: () => { prefs.layout = id; savePrefs(); layoutBtns.forEach((b, j) => b.setAttribute("aria-pressed", i === j ? "true" : "false")); fill(); },
  }, icon(id)));

  // Admin actions sit in the top bar, as the app's library actions do.
  if (isAdmin()) {
    const rescan = el("button", { class: "icon-btn", type: "button", title: "Rescan libraries", "aria-label": "Rescan libraries", onclick: async () => {
      rescan.disabled = true;
      try { const r = await api("/api/rescan", { method: "POST" }); toast(`Rescanned: ${plural(r.books, "book")}`); render(); }
      catch (e) { toast(e.message); rescan.disabled = false; }
    } }, icon("refresh"));
    queueMicrotask(() => document.getElementById("top-actions")?.replaceChildren(
      el("button", { class: "icon-btn", type: "button", title: "Libraries", "aria-label": "Libraries", onclick: openLibraries }, icon("folder")),
      rescan));
  }

  const search = el("div", { class: "search" }, icon("search"),
    el("input", { type: "search", placeholder: "Search your library", "aria-label": "Search", value: state.query,
      oninput: (e) => { state.query = e.target.value; fill(); } }));

  fill();
  return [search, filters, el("div", { class: "countbar" }, count, sortBtn, layoutBtns), shelf];
}

function bookRow(b) {
  return el("button", { class: "book-row", type: "button", onclick: () => openBook(b.id) },
    cover(b),
    el("div", { class: "body" },
      text("title-small clip2", b.title),
      b.author ? text("body-small variant clip", b.author) : null,
      bookProgress(b),
      statusLine(b)));
}

function bookCard(b) {
  return el("button", { class: "book-card", type: "button", onclick: () => openBook(b.id) },
    cover(b),
    bookProgress(b),
    text("title-small clip2", b.title),
    b.author ? text("body-small variant clip", b.author) : null,
    statusLine(b));
}

// ------------------------------------------------------------------ sheets

function openSheet(title, parts, { onBack, wide } = {}) {
  const lead = onBack
    ? el("button", { class: "icon-btn", type: "button", "aria-label": "Back", title: "Back", onclick: onBack }, icon("back"))
    : el("button", { class: "icon-btn", type: "button", "aria-label": "Close", title: "Close", onclick: () => dialog.close() }, icon("close"));
  dialog.replaceChildren(el("div", { class: "sheet" },
    el("div", { class: "sheet-bar" }, lead, el("h2", { class: "title-large clip" }, title || "")),
    ...parts.filter(Boolean)));
  dialog.style.width = wide ? "min(880px, calc(100vw - 24px))" : "";
  if (!dialog.open) dialog.showModal();
  dialog.scrollTop = 0;
}

dialog.addEventListener("click", (e) => { if (e.target === dialog) dialog.close(); });

// ------------------------------------------------------------------ one book

function openBook(id, mode = "view") {
  const b = state.books.find((x) => x.id === id);
  if (!b) return;
  if (mode === "edit") return openSheet("Edit details", bookEditor(b), { onBack: () => openBook(id) });
  if (mode === "match") return openSheet("Find details", bookMatcher(b), { onBack: () => openBook(id) });
  openSheet("", bookDetail(b), { wide: true });
}

function currentChapter(b, p) {
  if (!p || p.finished || !b.chapters.length) return -1;
  return b.chapters.findIndex((c) => p.positionMs >= c.startMs && p.positionMs < c.endMs);
}

function bookDetail(b) {
  const p = progressOf(b);
  const byline = [b.author, b.narrator ? "read by " + b.narrator : ""].filter(Boolean).join(" · ");
  const tags = [
    b.series ? `${b.series}${b.seriesIndex != null ? " #" + b.seriesIndex : ""}` : "",
    fmtDuration(b.durationMs), b.publishedYear, ...(b.genres || []).slice(0, 4),
    state.libraries.length > 1 ? b.library : "",
  ].filter(Boolean);

  const size = b.files.reduce((n, f) => n + (f.sizeBytes || 0), 0);
  const download = el("button", { class: "btn", type: "button", onclick: async () => {
    download.disabled = true;
    try {
      const link = await api(`/api/books/${encodeURIComponent(b.id)}/download-link`, { method: "POST" });
      const a = el("a", { href: link.url, download: "" });
      document.body.append(a); a.click(); a.remove();
    } catch (e) { toast(e.message); }
    download.disabled = false;
  } }, icon("download"), `Download · ${fmtBytes(size)}`);
  const actions = el("div", { class: "row", style: "margin-top:16px" }, download);
  if (isAdmin()) {
    actions.append(
      el("button", { class: "btn tonal", type: "button", onclick: () => openBook(b.id, "match") }, icon("magic"), "Find details"),
      el("button", { class: "btn outlined", type: "button", onclick: () => openBook(b.id, "edit") }, icon("edit"), "Edit"));
  }

  const cur = currentChapter(b, p);
  const progress = p && (p.positionMs > 0 || p.finished) ? el("div", { class: "panel wash stack" },
    cur >= 0 ? el("div", {}, text("title-small", b.chapters[cur].title), el("div", { class: "body-small variant" }, `${cur + 1} of ${b.chapters.length}`)) : null,
    gradientProgress(p.finished ? 1 : p.fraction, true),
    el("div", { class: "row label-medium variant", style: "justify-content:space-between" },
      text("", p.finished ? "Finished" + (p.finishedAt ? " " + fmtDate(p.finishedAt) : "") : fmtClock(p.positionMs)),
      p.finished ? null : text("", `${fmtDuration(b.durationMs - p.positionMs)} left in book`),
      text("", p.startedAt ? "Started " + fmtDate(p.startedAt) : ""))) : null;

  const facts = el("dl", { class: "facts body-medium" });
  const fact = (k, v) => v && facts.append(el("dt", {}, k), el("dd", {}, v));
  fact("Publisher", b.publisher);
  fact("Language", b.language);
  fact("ISBN", b.isbn);
  fact("ASIN", b.asin);

  return [
    el("div", { class: "hero" }, cover(b), el("div", {},
      el("h2", { class: "headline-small" }, b.title),
      b.subtitle ? el("p", { class: "title-medium variant", style: "font-weight:400" }, b.subtitle) : null,
      el("p", { class: "body-medium variant", style: "margin-top:4px" }, byline || "Unknown author"),
      el("div", { class: "tags" }, tags.map((t) => el("span", { class: "tag" }, t))),
      actions)),
    progress,
    b.description ? el("p", { class: "desc" }, b.description) : null,
    facts.childElementCount ? el("div", { class: "panel" }, facts) : null,
    b.chapters.length ? el("details", { class: "panel" },
      el("summary", {}, icon("chapters"), text("title-small grow", plural(b.chapters.length, "chapter")), cur >= 0 ? text("label-medium variant", `on ${cur + 1}`) : null),
      el("div", { class: "lines body-medium" }, b.chapters.map((c, i) => el("div", { class: i === cur ? "now" : "" },
        text("n", i + 1), text("t", c.title), text("end", fmtClock(c.startMs)))))) : null,
    el("details", { class: "panel" },
      el("summary", {}, icon("files"), text("title-small grow", plural(b.files.length, "file")), text("label-medium variant", fmtBytes(size))),
      el("div", { class: "lines body-medium" }, b.files.map((f, i) => el("div", {},
        text("n", i + 1), text("t clip", f.name), text("end", fmtDuration(f.durationMs), " · ", fmtBytes(f.sizeBytes)))))),
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
  el("p", { class: "wide body-small variant" }, "Saved details are kept in the server's data folder and take priority over the files. Reset brings back what the library says."),
  el("div", { class: "row wide" },
    el("button", { class: "btn text danger", type: "button", onclick: () => resetMetadata(b) }, "Reset to library"),
    el("span", { class: "grow" }),
    el("button", { class: "btn text", type: "button", onclick: () => openBook(b.id) }, "Cancel"),
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
  if (state.view === "library") refreshMain(viewLibrary);
}

function bookMatcher(b) {
  const providers = [["audible", "Audible"], ["openlibrary", "Open Library"], ["google", "Google Books"]];
  let provider = "audible";
  const region = el("select", { "aria-label": "Audible region", style: "width:auto;height:40px", onchange: () => search() },
    ["us", "uk", "ca", "au", "de", "fr", "it", "es", "in", "jp"].map((r) => el("option", { value: r }, r.toUpperCase())));
  const providerChips = el("div", { class: "segmented", role: "group", "aria-label": "Provider" });
  const drawProviders = () => providerChips.replaceChildren(...providers.map(([id, label]) =>
    chip(label, provider === id, () => { provider = id; region.hidden = id !== "audible"; drawProviders(); search(); })));
  drawProviders();
  const title = el("input", { type: "text", value: b.title, "aria-label": "Title", placeholder: "Title" });
  const author = el("input", { type: "text", value: (b.author || "").split(",")[0], "aria-label": "Author", placeholder: "Author" });
  const results = el("div", { class: "stack" });
  async function search(ev) {
    ev?.preventDefault();
    results.replaceChildren(el("p", { class: "body-medium variant" }, "Searching…"));
    const q = new URLSearchParams({ provider, title: title.value, author: author.value });
    if (provider === "audible") q.set("region", region.value);
    try {
      const out = await api(`/api/books/${encodeURIComponent(b.id)}/match?` + q);
      results.replaceChildren(...(out.results.length ? out.results.map((c) => matchResult(b, c)) : [el("p", { class: "body-medium variant" }, "Nothing found. Try a shorter title or another provider.")]));
    } catch (e) {
      results.replaceChildren(el("p", { class: "body-medium error-text" }, e.message));
    }
  }
  const form = el("form", { class: "row", onsubmit: search },
    el("div", { class: "grow" }, title), el("div", { class: "grow" }, author),
    el("button", { class: "btn", type: "submit" }, icon("search"), "Search"));
  queueMicrotask(search);
  return [
    el("p", { class: "body-medium variant" }, "The server asks the provider only when you search. The result you pick, and its cover, replace what the library says until you reset it."),
    el("div", { class: "row" }, providerChips, region),
    form,
    results,
  ];
}

function matchResult(b, c) {
  const series = c.series ? ` · ${c.series}${c.seriesIndex != null ? " #" + c.seriesIndex : ""}` : "";
  const keepCover = el("input", { type: "checkbox", checked: !!c.coverUrl, disabled: !c.coverUrl });
  return el("div", { class: "result" },
    c.coverUrl ? el("img", { src: c.coverUrl, alt: "", loading: "lazy", referrerpolicy: "no-referrer" }) : el("div", { class: "cover blank" }, (c.title || "").slice(0, 2)),
    el("div", { style: "min-width:0" },
      el("div", { class: "title-small" }, c.title, c.subtitle ? text("variant", ": " + c.subtitle) : null),
      el("div", { class: "body-small" }, (c.authors || []).join(", "), c.narrators?.length ? " · read by " + c.narrators.join(", ") : ""),
      el("div", { class: "body-small variant" }, [c.publishedYear, c.publisher].filter(Boolean).join(", "), series, c.durationMs ? " · " + fmtDuration(c.durationMs) : "")),
    el("div", { class: "row" },
      c.coverUrl ? el("label", { class: "check label-medium" }, keepCover, "Cover") : null,
      el("button", { class: "btn small", type: "button", onclick: () => {
        const body = { ...c };
        if (!keepCover.checked) delete body.coverUrl;
        saveMetadata(b, body);
      } }, "Use")));
}

// ------------------------------------------------------------------ libraries (admin): the app's Sources screen

async function openLibraries() {
  let libs;
  try { ({ libraries: libs } = await api("/api/libraries")); } catch (e) { toast(e.message); return; }
  const remove = async (l) => {
    if (!confirm(`Remove ${l.name}? Its books disappear from the server and every device. The files stay where they are, and adding the folder back brings the books and everyone's places back.`)) return;
    try {
      await api("/api/libraries/" + encodeURIComponent(l.name), { method: "DELETE" });
      toast(`Removed ${l.name}`);
      afterLibraryChange();
    } catch (e) { toast(e.message); }
  };
  const cards = libs.map((l) => el("div", { class: "source" + (l.error ? " bad" : "") },
    el("span", { class: "ico" }, icon("folder")),
    el("div", { class: "grow" },
      el("div", { class: "title-medium" }, l.name),
      el("div", { class: "body-small variant clip", title: l.path }, l.error ? "Can't read the folder: " + l.error : `${plural(l.books, "title")} · ${fmtDuration(l.durationMs)}`),
      el("div", { class: "body-small variant clip", title: l.path }, l.path),
      l.fixed ? el("div", { class: "label-small variant", style: "margin-top:4px" }, "Set on the server's command line") : null),
    l.fixed ? null : el("button", { class: "icon-btn", type: "button", title: `Remove ${l.name}`, "aria-label": `Remove ${l.name}`, onclick: () => remove(l) }, icon("remove"))));

  const picker = el("div");
  const addFolder = el("button", { class: "add-option", type: "button", onclick: () => { addFolder.hidden = true; showPicker(picker, ""); } },
    icon("folder"),
    el("div", { class: "grow" }, el("div", { class: "title-small t" }, "Folder on this server"),
      el("div", { class: "body-small variant" }, "Audiobooks the server can already see: a disk, a share, a mounted volume")));

  openSheet("Libraries", [
    libs.length ? el("div", { class: "stack" }, cards) : el("p", { class: "body-medium variant" }, "No libraries yet."),
    el("h3", { class: "title-medium", style: "margin-top:8px" }, "Add a library"),
    addFolder,
    picker,
    el("p", { class: "body-small variant" }, "Running in Docker? Only folders mounted into the container can be added. On the command line: kithara-server library add NAME DIR."),
  ]);
}

async function afterLibraryChange() {
  await openLibraries();
  if (state.view === "library") refreshMain(viewLibrary);
}

// A folder picker over the server's own disk (admins only; it lists folder names).
async function showPicker(box, path) {
  let here;
  try { here = await api("/api/fs" + (path ? "?path=" + encodeURIComponent(path) : "")); }
  catch (e) { toast(e.message); return; }
  const name = el("input", { type: "text", value: here.path.split(here.separator).filter(Boolean).pop() || "", placeholder: "Library name" });
  const typed = el("input", { type: "text", value: here.path, "aria-label": "Folder path", style: "height:40px;border:0;padding:0 4px;font-size:14px",
    onkeydown: (e) => { if (e.key === "Enter") { e.preventDefault(); showPicker(box, typed.value.trim()); } } });
  const add = el("button", { class: "btn", type: "button", onclick: async () => {
    add.disabled = true;
    try {
      await api("/api/libraries", { method: "POST", body: { name: name.value.trim(), path: here.path } });
      toast(`Added ${name.value.trim() || here.path}`);
      afterLibraryChange();
    } catch (e) { toast(e.message); add.disabled = false; }
  } }, icon("add"), "Add this folder");
  box.replaceChildren(el("div", { class: "stack" },
    el("div", { class: "picker" },
      el("div", { class: "here" },
        el("button", { class: "icon-btn", type: "button", title: "Up", "aria-label": "Up one folder", disabled: here.parent ? null : true, onclick: () => showPicker(box, here.parent) }, icon("up")),
        el("div", { class: "grow" }, typed)),
      el("div", { class: "list" },
        here.dirs.length ? here.dirs.map((d) => el("button", { class: "dir", type: "button", onclick: () => showPicker(box, d.path) },
          el("span", { class: "ico" }, icon("folder")), text("body-medium grow clip", d.name))) :
          el("p", { class: "body-medium variant", style: "padding:12px 16px" }, "No folders in here."))),
    el("p", { class: "body-small variant" }, here.audio ? `${plural(here.audio, "audio file")} directly in this folder. Pick the folder above your books if this is one book.` : "Each book can be a folder of audio files, or a single file; any nesting above that is fine."),
    el("div", { class: "row" }, el("label", { class: "field grow" }, el("span", {}, "Name"), name)),
    el("div", { class: "row end" }, el("button", { class: "btn text", type: "button", onclick: () => openLibraries() }, "Cancel"), add)));
}

// ------------------------------------------------------------------ listening (StatsScreen)

async function viewStats() {
  const tz = -new Date().getTimezoneOffset();
  const [st] = await Promise.all([api("/api/stats?tzOffset=" + tz), state.books.length ? null : loadLibrary()]);
  const byId = new Map(state.books.map((b) => [b.id, b]));
  const tile = (k, v) => el("div", { class: "card variant-bg" }, text("label-medium", k), text("v", v));

  // The last 30 days, today included: listened days in primary-container, today in
  // primary, the rest surface-variant, as the app's bars.
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
    series.map(([d, ms, today]) => el("div", { class: today && ms ? "today" : ms ? "" : "zero", style: `height:${Math.max(2, (ms / peak) * 100)}%`, title: `${d}: ${fmtDuration(ms)}` })));

  const avg = st.daysListened ? st.totalListenedMs / st.daysListened : 0;
  return el("div", { class: "stats" },
    el("div", { class: "card hero-card" },
      text("label-large", "Last 30 days"),
      text("display-small", fmtDuration(month)),
      text("body-small primary-text", `${plural(activeDays, "day")} with listening · ${fmtDuration(st.totalListenedMs)} all time`)),
    el("div", { class: "stat-tiles" },
      tile("Streak", plural(st.currentStreakDays, "day")),
      tile("Longest streak", plural(st.longestStreakDays, "day")),
      tile("Days listened", st.daysListened),
      tile("Average per day", fmtDuration(avg)),
      tile("Books finished", plural(st.booksFinished, "book")),
      tile("Books started", plural(st.booksStarted, "book"))),
    el("h2", { class: "title-medium section-title" }, "By day"),
    el("div", {}, chart, el("div", { class: "axis label-small variant" }, text("", fmtDate(Date.parse(series[0][0]))), text("", "Today"))),
    el("h2", { class: "title-medium section-title" }, "Most time spent"),
    st.books.length ? el("div", {}, [...st.books].sort((a, b) => b.listenedMs - a.listenedMs).slice(0, 10).map((r) => {
      const b = byId.get(r.bookId);
      return el("div", { class: "most" },
        b ? cover(b) : el("div", { class: "cover-box" }, el("div", { class: "cover blank" }, "?")),
        el("div", { class: "grow" },
          el("div", { class: "body-medium clip" }, b?.title || "A book no longer in your library"),
          el("div", { class: "label-small variant" }, r.finishedAt ? `Finished ${fmtDate(r.finishedAt)}` : `Last listened ${relative(r.lastListenedAt)}`)),
        text("label-medium variant", fmtDuration(r.listenedMs)));
    })) : el("p", { class: "body-medium variant" }, "Nothing yet. Listening time appears here as the apps sync."),
    el("p", { class: "body-small variant", style: "margin-top:8px" }, "Worked out from the apps' progress syncs, so these figures are an estimate and lean low."));
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
      out.push(el("div", { class: "chips", style: "margin-bottom:8px", role: "group", "aria-label": "Whose devices" },
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
  out.push(devices.length ? el("div", { class: "stack" }, devices.map((d) => el("div", { class: "item-card" },
    el("span", { class: "ico" }, icon(deviceIcon(d.name))),
    el("div", { class: "body" },
      el("div", { class: "title-medium" }, d.name || "Unnamed device", d.current ? el("span", { class: "badge" }, "This browser") : null),
      el("div", { class: "body-small variant" }, `Signed in ${fmtDate(d.createdAt)} · last seen ${relative(d.lastSeenAt)}`)),
    el("button", { class: "btn text danger", type: "button", onclick: () => revoke(d) }, "Sign out")))) :
    el("p", { class: "body-medium variant" }, "No devices signed in."));
  out.push(el("p", { class: "body-small variant", style: "margin-top:12px" }, "Signing a device out makes it ask for the password again. Changing a password signs out every device."));
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
  const caption = (t) => el("div", { class: "label-medium variant", style: "margin-bottom:4px" }, t);

  const cards = users.map((u) => {
    const libs = libraryPicker(u.libraries || []);
    const libsRow = el("div", { hidden: u.role === "admin" || libraries.length < 2 ? true : null }, caption("Can see"), libs.node);
    const role = rolePicker(u.role, (v) => { libsRow.hidden = v === "admin" || libraries.length < 2; });
    const save = el("button", { class: "btn tonal small", type: "button", onclick: async () => {
      try {
        await api("/api/users/" + encodeURIComponent(u.name), { method: "PATCH", body: { role: role.value(), libraries: libs.value() } });
        toast("Saved " + u.name);
        if (u.name === state.me.name) { state.me = await api("/api/me"); render(); } else rerender();
      } catch (e) { toast(e.message); }
    } }, "Save");
    const password = el("button", { class: "btn text", type: "button", onclick: async () => {
      const pw = prompt(`New password for ${u.name} (8 or more characters). Their devices will be signed out.`);
      if (!pw) return;
      try { await api("/api/users/" + encodeURIComponent(u.name), { method: "PATCH", body: { password: pw } }); toast("Password changed"); rerender(); }
      catch (e) { toast(e.message); }
    } }, "Password");
    const remove = u.name === state.me.name ? null : el("button", { class: "btn text danger", type: "button", onclick: async () => {
      if (!confirm(`Remove ${u.name}? Their devices are signed out; their listening history is kept aside on the server.`)) return;
      try { await api("/api/users/" + encodeURIComponent(u.name), { method: "DELETE" }); toast("Removed " + u.name); rerender(); }
      catch (e) { toast(e.message); }
    } }, "Remove");
    const access = u.role === "admin" ? "Admin · sees everything" : u.libraries?.length ? "Sees " + u.libraries.join(", ") : "Sees every library";
    return el("div", { class: "item-card" },
      el("span", { class: "avatar", style: "width:40px;height:40px;font-size:16px" }, u.name.slice(0, 1)),
      el("div", { class: "body" },
        el("div", { class: "title-medium" }, u.name, u.name === state.me.name ? el("span", { class: "badge" }, "You") : null),
        el("div", { class: "body-small variant" }, `${access} · ${plural(u.devices, "device")}`)),
      el("div", { class: "row" }, password, remove),
      el("div", { class: "more" }, el("div", { class: "row" }, role.node, save), libsRow));
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
  el("label", { class: "field" }, el("span", {}, "User name"), name),
  el("label", { class: "field" }, el("span", {}, "Password (8 or more characters)"), pass),
  el("div", { class: "wide" }, caption("Role"), newRole.node),
  newLibsRow,
  el("div", { class: "wide row end" }, el("button", { class: "btn", type: "submit" }, icon("add"), "Add user")));

  return [
    el("div", { class: "stack" }, cards),
    el("p", { class: "body-small variant", style: "margin:12px 0 0" }, "Listeners see only the libraries chosen for them, on every device. Admins can add libraries, rescan, edit details and manage users."),
    el("h2", { class: "title-medium section-title" }, "Add a user"),
    el("div", { class: "card pad" }, add),
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
  applyTheme();
  matchMedia("(prefers-color-scheme: dark)").addEventListener?.("change", applyTheme);
  try { state.server = await api("/api/health"); } catch { /* shown as "Kithara" */ }
  document.title = state.server?.name ? `${state.server.name} · Kithara` : "Kithara";
  state.token = loadToken();
  if (!state.token) return renderSignIn();
  try { await start(); } catch (e) { if (e.status !== 401) renderSignIn(e.message); }
})();
