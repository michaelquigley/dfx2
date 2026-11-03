"use strict";

// The same script runs in both modes. Everything is fetched by relative URL;
// the mode is read from the origin only to say which limit result to expect.
const INPROCESS_ORIGIN = "https://dfx2-spike.invalid";
const mode = location.origin === INPROCESS_ORIGIN ? "inprocess" : "loopback";
const UTF8_TEXT = "héllo — 日本語 ✓ 🙂";
const LIMIT = 1 << 20;

const $ = (id) => document.getElementById(id);
const logEl = $("log");

function log(kind, text) {
  const li = document.createElement("li");
  li.className = kind;
  li.textContent = `${new Date().toISOString().slice(11, 23)} ${text}`;
  logEl.appendChild(li);
  logEl.scrollTop = logEl.scrollHeight;
}

// report sends a line to the handler, which logs it on the host: page-side
// evidence lands in the same log as the request lifetimes.
async function report(text) {
  try {
    await fetch("api/report", { method: "POST", body: `[${mode}] ${text}`, headers: { "Content-Type": "text/plain; charset=utf-8" } });
  } catch (e) {
    log("fail", `report failed: ${e}`);
  }
}

async function sha256hex(bytes) {
  const d = await crypto.subtle.digest("SHA-256", bytes);
  return [...new Uint8Array(d)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function expectedBinary() {
  const b = [];
  for (let i = 0; i < 256; i++) b.push(i);
  b.push(0x00, 0x0d, 0x0a, 0xff, 0xfe, 0xc3, 0x28, 0xef, 0xbb, 0xbf, 0x00);
  return new Uint8Array(b);
}

function sameBytes(a, b) {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Each check resolves to { ok: true|false|null, detail }. null is
// informational: recorded, not judged.
const checks = {
  async assets() {
    const css = getComputedStyle(document.documentElement).getPropertyValue("--css-loaded").trim() === "1";
    return { ok: css, detail: `app.css applied: ${css}; app.js running: true` };
  },

  async query() {
    const q = new URLSearchParams([["text", UTF8_TEXT], ["n", "1"], ["n", "2"], ["empty", ""]]);
    const r = await fetch(`api/query?${q}`, { headers: { "X-Spike-Request": "query-check" } });
    const j = await r.json();
    const ok = r.status === 200 && j.method === "GET" && j.path === "/api/query" &&
      j.query.text?.[0] === UTF8_TEXT && j.query.n?.join(",") === "1,2" && j.query.empty?.[0] === "" &&
      j.custom?.[0] === "query-check" && r.headers.get("content-type") === "application/json; charset=utf-8";
    return { ok, detail: `status ${r.status}, text ${JSON.stringify(j.query.text?.[0])}, n=${j.query.n}, host ${j.host}, custom header ${j.custom}` };
  },

  async post() {
    const body = `${UTF8_TEXT}\r\nline two\n`;
    const bytes = new TextEncoder().encode(body);
    const r = await fetch("api/echo", { method: "POST", body, headers: { "Content-Type": "text/plain; charset=utf-8", "X-Spike-Request": "post-check" } });
    const j = await r.json();
    const ok = r.status === 200 && j.text === body && j.length === bytes.length && j.sha256 === (await sha256hex(bytes)) &&
      j.contentType === "text/plain; charset=utf-8" && j.contentLength === bytes.length;
    return { ok, detail: `status ${r.status}, ${j.length} B, Content-Length ${j.contentLength}, sha256 match ${j.sha256 === (await sha256hex(bytes))}` };
  },

  async "post-binary"() {
    const bytes = new Uint8Array(64 * 1024);
    crypto.getRandomValues(bytes);
    bytes.set(expectedBinary(), 0);
    const r = await fetch("api/echo", { method: "POST", body: bytes, headers: { "Content-Type": "application/octet-stream" } });
    const j = await r.json();
    const want = await sha256hex(bytes);
    const ok = r.status === 200 && j.length === bytes.length && j.sha256 === want;
    return { ok, detail: `status ${r.status}, sent ${bytes.length} B, handler got ${j.length} B, sha256 match ${j.sha256 === want}` };
  },

  async binary() {
    const r = await fetch("api/binary");
    const got = new Uint8Array(await r.arrayBuffer());
    const want = expectedBinary();
    const ok = r.status === 200 && sameBytes(got, want) && r.headers.get("content-type") === "application/octet-stream";
    return { ok, detail: `status ${r.status}, ${got.length}/${want.length} B, bytes identical ${sameBytes(got, want)}, type ${r.headers.get("content-type")}` };
  },

  async headers() {
    const r = await fetch("api/headers");
    await r.json();
    const repeat = r.headers.get("x-spike-repeat");
    const cc = r.headers.get("cache-control");
    const ok = r.status === 200 && repeat === "one, two" && cc === "no-store";
    // JavaScript sees repeated headers joined; whether they arrived as two
    // lines is visible only in devtools (Network) or the host log.
    return { ok, detail: `x-spike-repeat ${JSON.stringify(repeat)} (fetch joins repeats), cache-control ${JSON.stringify(cc)}` };
  },

  async "header-utf8"() {
    const r = await fetch("api/headers");
    await r.json();
    const v = r.headers.get("x-spike-unicode");
    return { ok: null, detail: `handler sent "café" as UTF-8 bytes; page sees ${JSON.stringify(v)}` };
  },

  async s404() {
    const r = await fetch("api/status/404");
    const j = await r.json();
    return { ok: r.status === 404 && j.from === "handler", detail: `status ${r.status} ${r.statusText}, body ${JSON.stringify(j)}` };
  },

  async s500() {
    const r = await fetch("api/status/500");
    const j = await r.json();
    return { ok: r.status === 500 && j.from === "handler", detail: `status ${r.status} ${r.statusText}, body ${JSON.stringify(j)}` };
  },

  async unknown() {
    const r = await fetch("no/such/route");
    const t = await r.text();
    return { ok: r.status === 404 && t === "404 page not found\n", detail: `status ${r.status}, body ${JSON.stringify(t)}, type ${r.headers.get("content-type")}` };
  },

  async race() {
    const order = [];
    const t0 = performance.now();
    const slow = fetch("api/slow?tag=race").then((r) => r.json()).then((j) => order.push(`slow(${j.slow})@${Math.round(performance.now() - t0)}ms`));
    await sleep(100);
    const fast = fetch("api/query?fast=1").then((r) => r.json()).then(() => order.push(`fast@${Math.round(performance.now() - t0)}ms`));
    await Promise.all([slow, fast]);
    return { ok: order[0].startsWith("fast") && order[1].startsWith("slow(finished)"), detail: order.join(" then ") };
  },

  async abort() {
    const c = new AbortController();
    const t0 = performance.now();
    const p = fetch("api/slow?tag=abort", { signal: c.signal });
    setTimeout(() => c.abort(), 300);
    let outcome;
    try {
      await p;
      outcome = "completed (not aborted?)";
    } catch (e) {
      outcome = `${e.name} after ${Math.round(performance.now() - t0)}ms`;
    }
    // Whether the Go handler saw cancellation is in the host log
    // ("fixture: /api/slow tag=\"abort\" saw cancellation"), not here.
    return { ok: null, detail: `browser side: ${outcome}; Go side: see host log for tag=abort` };
  },

  async "big-post"() {
    const bytes = new Uint8Array(LIMIT + LIMIT / 2);
    const r = await fetch("api/echo", { method: "POST", body: bytes, headers: { "Content-Type": "application/octet-stream" } });
    const adapterErr = r.headers.get("x-spike-adapter-error");
    const text = await r.text();
    const ok = mode === "inprocess" ? r.status === 413 && adapterErr === "request-too-large" : r.status === 200;
    const after = await checks.query();
    return { ok: ok && after.ok, detail: `status ${r.status}, adapter error ${adapterErr}, body ${JSON.stringify(text.slice(0, 80))}; next request ok: ${after.ok}` };
  },

  async "big-get"() {
    const r = await fetch("api/big");
    const adapterErr = r.headers.get("x-spike-adapter-error");
    const n = (await r.arrayBuffer()).byteLength;
    const ok = mode === "inprocess" ? r.status === 502 && adapterErr === "response-too-large" : r.status === 200 && n === 2 * LIMIT;
    const after = await checks.query();
    return { ok: ok && after.ok, detail: `status ${r.status}, adapter error ${adapterErr}, ${n} B; next request ok: ${after.ok}` };
  },

  async "open-outside"() {
    const w = window.open("https://example.com/");
    return { ok: w === null, detail: `window.open returned ${w === null ? "null" : "a window"}; host log shows the refusal` };
  },

  async "open-same"() {
    const w = window.open("./");
    return { ok: w === null, detail: `window.open("./") returned ${w === null ? "null" : "a window"} (all new windows are refused)` };
  },
};

const allChecks = ["assets", "query", "post", "post-binary", "binary", "headers", "header-utf8", "s404", "s500", "unknown",
  "race", "abort", "big-post", "big-get", "open-outside", "open-same"];

async function runCheck(name) {
  let res;
  try {
    res = await checks[name]();
  } catch (e) {
    res = { ok: false, detail: `threw ${e}` };
  }
  const word = res.ok === null ? "INFO" : res.ok ? "PASS" : "FAIL";
  log(res.ok === null ? "info" : res.ok ? "pass" : "fail", `${word} ${name}: ${res.detail}`);
  return { name, word, detail: res.detail };
}

async function runAll() {
  const results = [];
  for (const name of allChecks) results.push(await runCheck(name));
  const judged = results.filter((r) => r.word !== "INFO");
  const passed = judged.filter((r) => r.word === "PASS").length;
  const summary = `checks ${passed === judged.length ? "PASS" : "FAIL"} ${passed}/${judged.length}`;
  log(passed === judged.length ? "pass" : "fail", summary);
  for (const r of results) await report(`${r.word} ${r.name}: ${r.detail}`);
  await report(summary);
  return summary;
}

async function slow(tag) {
  const t0 = performance.now();
  log("info", `slow ${tag}: started`);
  try {
    const r = await fetch(`api/slow?tag=${encodeURIComponent(tag)}`);
    const j = await r.json();
    log("info", `slow ${tag}: ${r.status} ${j.slow} after ${Math.round(performance.now() - t0)}ms`);
  } catch (e) {
    log("fail", `slow ${tag}: ${e} after ${Math.round(performance.now() - t0)}ms`);
  }
}

async function fast() {
  const t0 = performance.now();
  const r = await fetch("api/query?fast=1");
  await r.json();
  log("info", `fast: ${r.status} after ${Math.round(performance.now() - t0)}ms`);
}

function reloadDuringSlow() {
  sessionStorage.setItem("reloadedDuringSlow", String(Date.now()));
  slow("reload");
  report("reload: slow request tag=reload started; reloading in 500ms");
  setTimeout(() => location.reload(), 500);
}

function navOutside() {
  log("info", "navigating to https://example.com/ (the host should refuse)");
  location.href = "https://example.com/";
  setTimeout(() => {
    log("pass", "still here 1.5 s after outside navigation: refused");
    report("origin: still on the fixture 1.5 s after location.href = https://example.com/");
  }, 1500);
}

function wire() {
  $("origin").textContent = location.origin;
  $("mode").textContent = mode;
  const css = getComputedStyle(document.documentElement).getPropertyValue("--css-loaded").trim() === "1";
  $("assets").textContent = css ? "app.css ✓ app.js ✓" : "app.css ✗";
  document.title = `dfx2 WebView2 spike fixture (${mode})`;

  $("run-all").onclick = runAll;
  for (const id of ["query", "post", "post-binary", "binary", "headers", "s404", "s500", "unknown", "race", "abort", "big-post", "big-get", "open-outside", "open-same"]) {
    $(id).onclick = () => runCheck(id);
  }
  $("headers").onclick = async () => { await runCheck("headers"); await runCheck("header-utf8"); };
  $("slow").onclick = () => slow("button");
  $("fast").onclick = fast;
  $("reload-slow").onclick = reloadDuringSlow;
  $("nav-outside").onclick = navOutside;
  $("clear").onclick = () => { logEl.textContent = ""; };
}

async function autorun() {
  const what = new URLSearchParams(location.search).get("autorun");
  const reloaded = sessionStorage.getItem("reloadedDuringSlow");
  if (reloaded) {
    sessionStorage.removeItem("reloadedDuringSlow");
    log("info", `reloaded ${Date.now() - Number(reloaded)}ms after starting a slow request`);
    await report(`reload: page reloaded ${Date.now() - Number(reloaded)}ms after starting slow tag=reload`);
    return;
  }
  await report(`loaded ${location.pathname}${location.search} (assets ${$("assets").textContent})`);
  switch (what) {
    case "checks":
      await runAll();
      navOutside();
      break;
    case "slow":
      slow("busy");
      await report("slow: started tag=busy");
      break;
    case "reload":
      reloadDuringSlow();
      break;
  }
}

wire();
autorun();
