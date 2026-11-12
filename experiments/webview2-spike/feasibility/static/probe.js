// Probe page for the webview integration feasibility spike. A host may run
// these functions through its native JavaScript call, with results coming
// back as values. A host that evaluates no JavaScript natively loads the page
// as ?run=NAME instead: the page runs it and posts the result to its own
// handler's /api/report, as ordinary same-origin application traffic, then
// performs any ?then= action. Cross-origin attempts are judged by the
// collector's own counters, not by what the page sees.
"use strict";
const probe = {};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

probe.assets = () => ({
  origin: location.origin,
  css: getComputedStyle(document.body).backgroundColor === "rgb(30, 31, 36)",
  img: document.getElementById("logo").complete && document.getElementById("logo").naturalWidth === 48,
  script: true,
  cookieVisible: document.cookie.includes("dfx2-endpoint"),
});

probe.checks = async () => {
  const q = await (await fetch("api/query?x=" + encodeURIComponent("é&ü"))).json();
  const body = "edit=" + "x".repeat(1000);
  const e = await (await fetch("api/echo", { method: "POST", body })).json();
  return { ...probe.assets(), query: q.x, echoLength: e.length, echoSha: e.sha256 };
};

// Streaming: the handler sends chunk 1, then waits until the page acknowledges
// it before sending chunk 2. Seeing both proves chunk 1 was visible early.
probe.stream = async () => {
  const r = await fetch("api/stream");
  const reader = r.body.getReader();
  const decoder = new TextDecoder();
  const chunks = [];
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    const text = decoder.decode(value);
    chunks.push({ text, at: performance.now() });
    if (text.includes("chunk-1")) await fetch("api/stream-ack", { method: "POST" });
  }
  return chunks;
};

// Storage: an application cookie, local and session storage, and a cached
// resource whose server count shows whether the browser cache answered.
probe.storageSet = (tag) => {
  document.cookie = "vis=" + tag + "; path=/";
  localStorage.setItem("k", tag);
  sessionStorage.setItem("k", tag);
  return probe.storageRead();
};
probe.storageRead = () => ({ cookie: document.cookie, local: localStorage.getItem("k"), session: sessionStorage.getItem("k") });
probe.cached = async () => {
  const r = await fetch("api/cached");
  return { count: r.headers.get("X-Count"), body: await r.text() };
};
// The same cacheable resource as an image, twice in this document: the second
// can be answered by the session's memory cache without a request.
probe.cachedImage = () => new Promise((ok) => {
  let n = 0;
  const load = () => { const i = new Image(); i.onload = i.onerror = () => { if (++n === 2) ok("loaded twice"); else load(); }; i.src = "api/cached?as=image"; };
  load();
});

// Requests aimed at the collector (another port on the same host) and at
// another window's endpoint. Each is fire-and-forget; the page's view of
// success is recorded but proves nothing.
probe.attacks = async (collector, other) => {
  const seen = {};
  const tryit = async (name, f) => { try { seen[name] = String(await f()); } catch (e) { seen[name] = "threw " + e.name; } };
  const add = (tag, attrs) => { const el = document.createElement(tag); Object.assign(el, attrs); document.body.appendChild(el); return el; };
  await tryit("img", () => { add("img", { src: collector + "/img" }); return "added"; });
  await tryit("script", () => { add("script", { src: collector + "/script" }); return "added"; });
  await tryit("css", () => { add("link", { rel: "stylesheet", href: collector + "/css" }); return "added"; });
  await tryit("font", () => { const s = add("style", {}); s.textContent = `@font-face{font-family:p;src:url(${collector}/font)}body{font-family:p}`; return "added"; });
  await tryit("fetch", async () => (await fetch(collector + "/fetch", { credentials: "include", mode: "no-cors" })).type);
  await tryit("xhr", () => new Promise((ok) => { const x = new XMLHttpRequest(); x.open("GET", collector + "/xhr"); x.withCredentials = true; x.onloadend = () => ok(x.status); x.send(); }));
  await tryit("beacon", () => navigator.sendBeacon(collector + "/beacon", "x"));
  await tryit("websocket", () => new Promise((ok) => { const w = new WebSocket(collector.replace("http", "ws") + "/ws"); w.onopen = () => ok("open"); w.onerror = () => ok("error"); setTimeout(() => ok("timeout"), 1500); }));
  await tryit("eventsource", () => new Promise((ok) => { const s = new EventSource(collector + "/sse"); s.onerror = () => { s.close(); ok("error"); }; setTimeout(() => { s.close(); ok("timeout"); }, 1500); }));
  await tryit("prefetch", () => { add("link", { rel: "prefetch", href: collector + "/prefetch" }); return "added"; });
  await tryit("iframe-cross", () => { add("iframe", { src: collector + "/iframe" }); return "added"; });
  await tryit("iframe-same", () => new Promise((ok) => { const f = add("iframe", { src: "api/query?x=frame" }); f.onload = () => { try { ok("loaded " + f.contentDocument.body.textContent.length); } catch (e) { ok("loaded, unreadable"); } }; setTimeout(() => ok("no load"), 1500); }));
  await tryit("redirect-img", () => { add("img", { src: "api/redirect-out?to=" + encodeURIComponent(collector + "/redirect-img") }); return "added"; });
  await tryit("redirect-fetch", async () => (await fetch("api/redirect-out?to=" + encodeURIComponent(collector + "/redirect-fetch"))).status);
  await tryit("ping", () => { const a = add("a", { href: "#pinged", ping: collector + "/ping", textContent: "p" }); a.click(); return "clicked"; });
  if (other) await tryit("other-endpoint", async () => (await fetch(other + "/api/count", { credentials: "include" })).status);
  await sleep(2000);
  return seen;
};

// Navigation attempts end the page, so the host runs them one at a time.
probe.navigate = (how, collector) => {
  switch (how) {
    case "location": location.href = collector + "/nav"; break;
    case "redirect": location.href = "api/redirect-out?to=" + encodeURIComponent(collector + "/redirect-nav"); break;
    case "popup": return String(window.open(collector + "/popup"));
    case "form": { const f = document.createElement("form"); f.method = "post"; f.action = collector + "/form"; document.body.appendChild(f); f.submit(); break; }
  }
  return how;
};

// Page-driven runs: each takes the URL's parameters.
probe.runs = {
  checks: () => probe.checks(),
  stream: () => probe.stream(),
  storageSet: (q) => probe.storageSet(q.get("tag")),
  storageRead: () => probe.storageRead(),
  cached: () => probe.cached(),
  cachedImage: () => probe.cachedImage(),
  attacks: (q) => probe.attacks(q.get("collector"), q.get("other") || ""),
  popup: (q) => probe.navigate("popup", q.get("collector")),
  query: async (q) => (await fetch("api/query?x=" + encodeURIComponent(q.get("x") || ""))).json(),
  count: async (q) => (await fetch(q.get("url"))).json(),
  slow: () => { fetch("api/slow"); return "started"; },
  broken: async () => {
    const fetched = await fetch("/broken").then(() => "ok", (e) => "rejected: " + e.name);
    await new Promise((ok) => { const i = new Image(); i.onload = i.onerror = ok; i.src = "/broken"; document.body.appendChild(i); });
    return { fetched, path: location.pathname, form: !!document.getElementById("edit") };
  },
  armed: () => "armed",
};
// Actions after the report, which end or leave the page.
probe.then = {
  navigate: (q) => probe.navigate(q.get("how"), q.get("collector")),
  go: (q) => { location.href = q.get("to"); },
  submit: (q) => { const f = document.getElementById("edit"); if (q.get("action")) f.action = q.get("action"); f.submit(); },
};
probe.report = (body) => fetch("api/report", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
addEventListener("load", async () => {
  const q = new URLSearchParams(location.search);
  const run = q.get("run");
  if (!run) return;
  const report = { id: q.get("id") || run, run, href: location.href };
  try { report.result = await probe.runs[run](q); } catch (e) { report.error = String(e); }
  await probe.report(report);
  await sleep(Number(q.get("delay") || 0));
  if (q.get("then")) probe.then[q.get("then")](q);
});

addEventListener("DOMContentLoaded", () => {
  document.getElementById("origin").textContent = location.origin;
  const a = probe.assets();
  document.getElementById("assets").textContent = (a.css ? "css " : "") + "script";
});
