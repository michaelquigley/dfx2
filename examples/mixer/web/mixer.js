"use strict";
// One browser view of the mixer model. It polls one snapshot at a time,
// shows it, and submits a user's edits once each; showing a snapshot never
// submits anything, because assigning a control's value fires no event.

const params = new URLSearchParams(location.search);
let view = params.get("view") || "";
let audition = false;
const strips = new Map(); // channel id -> elements
const held = new Set();   // ids whose fader the pointer is holding
const sending = new Map(); // id -> edits queued or unanswered for that channel
let submissions = Promise.resolve(); // this page's edits, submitted one at a time
let lastReport = 0;

const api = (path) => view ? `${path}?view=${encodeURIComponent(view)}` : path;
const $ = (id) => document.getElementById(id);

async function poll() {
  try {
    const r = await fetch(api("api/state"), { cache: "no-store" });
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    const s = await r.json();
    view = s.view;
    audition = s.audition;
    render(s);
  } catch (e) {
    $("net").textContent = `state: ${e}`;
  }
  setTimeout(poll, 100); // the next poll starts only after this one ended
}

function strip(c) {
  let e = strips.get(c.id);
  if (e) return e;
  e = { root: document.createElement("div") };
  e.root.className = "strip";
  e.root.dataset.id = c.id;
  e.name = add(e.root, "div", "name");
  e.fader = add(e.root, "input");
  Object.assign(e.fader, { type: "range", min: "0", max: "1", step: "0.01" });
  e.fader.setAttribute("aria-label", `${c.name} gain`);
  const label = add(e.root, "label", "small");
  e.mute = add(label, "input");
  e.mute.type = "checkbox";
  label.append(" Mute");
  e.meter = add(add(e.root, "div", "meter"), "div");
  e.level = add(e.root, "div", "small");
  e.device = add(e.root, "div", "small");
  e.pending = add(e.root, "div", "small pending");
  e.fader.addEventListener("pointerdown", () => held.add(c.id));
  e.fader.addEventListener("change", () => edit(c.id, "gain", Number(e.fader.value)));
  e.mute.addEventListener("change", () => edit(c.id, "mute", e.mute.checked));
  strips.set(c.id, e);
  return e;
}

addEventListener("pointerup", () => held.clear());
addEventListener("pointercancel", () => held.clear());

function add(parent, tag, cls) {
  const el = document.createElement(tag);
  if (cls) el.className = cls;
  parent.append(el);
  return el;
}

function render(s) {
  $("title").textContent = `dfx2 synthetic mixer — view ${s.view} · tick ${s.tick} · feed ${s.running ? "running" : "paused"}`;
  const box = $("strips");
  const shown = new Set(s.channels.map((c) => c.id));
  for (const [id, e] of strips) {
    if (!shown.has(id)) { e.root.remove(); strips.delete(id); held.delete(id); }
  }
  // Reorder only when the order changed: moving an element between a press
  // and its release would cancel the click or the drag.
  const order = s.channels.map((c) => strip(c).root);
  if (order.length !== box.children.length || order.some((el, i) => box.children[i] !== el)) box.append(...order);
  for (const c of s.channels) {
    const e = strip(c);
    e.name.textContent = c.name;
    if (!held.has(c.id) && !sending.has(c.id)) e.fader.value = String(c.shownGain);
    if (!sending.has(c.id)) e.mute.checked = c.shownMute;
    e.meter.style.width = `${Math.round(c.level * 100)}%`;
    e.level.textContent = `${c.db.toFixed(1)} dB`;
    e.device.textContent = `device ${c.gain.toFixed(2)}${c.muted ? " muted" : ""}`;
    const pending = [];
    if (c.gainPending) pending.push(`gain ${c.pendingGain.toFixed(2)} #${c.gainSeq}`);
    if (c.mutePending) pending.push(`mute ${c.pendingMute ? "on" : "off"} #${c.muteSeq}`);
    e.pending.textContent = pending.length ? `pending ${pending.join(", ")}` : "";
  }
  const k = s.commands;
  $("commands").textContent = `commands: ${k.accepted} accepted, ${k.applied} applied, ${k.rejected} rejected, ` +
    `${k.refused} refused, ${k.canceled} canceled, ${k.outstanding} outstanding · device delay ${k.delayMs} ms` +
    (k.rejectNext ? ` · rejecting next ${k.rejectNext}` : "");
  $("error").textContent = s.error ? `latest error: ${s.error}` : "";
  if (audition) report(s);
}

// edit queues one submission behind this page's earlier ones, so edits
// reach the command queue in input order. Each is sent once; a failure is
// shown, never retried, and the next one goes ahead.
function edit(channel, control, value) {
  sending.set(channel, (sending.get(channel) || 0) + 1);
  submissions = submissions.then(() => submit(channel, control, value));
}

async function submit(channel, control, value) {
  try {
    const r = await fetch(api("api/edit"), { method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ channel, control, value }) });
    const out = r.ok ? await r.json() : { error: `HTTP ${r.status}` };
    $("net").textContent = out.accepted ? `edit #${out.sequence} accepted` : `edit refused: ${out.error}`;
  } catch (e) {
    $("net").textContent = `edit failed: ${e}`;
  } finally {
    const left = sending.get(channel) - 1;
    if (left > 0) sending.set(channel, left); else sending.delete(channel);
  }
}

// report tells the automated audition what this page shows and where its
// controls are, in viewport CSS pixels. Only with -audition.
function report(s) {
  const now = Date.now();
  if (now - lastReport < 250) return;
  lastReport = now;
  const centre = (el) => { const r = el.getBoundingClientRect(); return [r.x + r.width / 2, r.y + r.height / 2]; };
  const channels = s.channels.map((c) => {
    const e = strips.get(c.id);
    return { id: c.id, fader: Number(e.fader.value), mute: e.mute.checked, pending: e.pending.textContent,
      faderAt: centre(e.fader), faderLeft: e.fader.getBoundingClientRect().left, faderWidth: e.fader.getBoundingClientRect().width,
      muteAt: centre(e.mute) };
  });
  fetch(api("api/report"), { method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ version: s.version, viewport: [innerWidth, innerHeight], scale: devicePixelRatio, channels,
      commands: $("commands").textContent, error: $("error").textContent, net: $("net").textContent }) }).catch(() => {});
}

poll();
