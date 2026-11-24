#!/usr/bin/env node
// Checks the mixer page's edit submission against controlled responses: one
// page submits its edits in input order, one at a time; a fader with an edit
// still queued or unanswered keeps its latest value through an older
// snapshot; a failed submission lets the next one through and is never
// retried. It loads examples/mixer/web/mixer.js into a bare context with a
// minimal DOM stub and a fetch whose responses this script releases.
//
//     node evidence/webview-integration/linux/mixer_page_order.js [PAGE_SCRIPT]
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const page = process.argv[2] || path.join(__dirname, "../../../examples/mixer/web/mixer.js");

class El {
  constructor(tag) {
    Object.assign(this, { tag, children: [], dataset: {}, style: {}, listeners: {}, textContent: "", value: "", checked: false, parent: null });
  }
  append(...items) {
    for (const x of items) {
      if (typeof x === "string") continue;
      x.remove();
      x.parent = this;
      this.children.push(x);
    }
  }
  remove() {
    if (this.parent) this.parent.children.splice(this.parent.children.indexOf(this), 1);
    this.parent = null;
  }
  addEventListener(type, f) { (this.listeners[type] ||= []).push(f); }
  dispatch(type) { for (const f of this.listeners[type] || []) f({}); }
  setAttribute() {}
  getBoundingClientRect() { return { x: 0, y: 0, width: 10, height: 10, left: 0 }; }
}

const byId = {};
const requests = []; // every fetch: { url, body, settle(ok, body) }
const timers = [];
const context = vm.createContext({
  document: { getElementById: (id) => (byId[id] ||= new El("#" + id)), createElement: (tag) => new El(tag) },
  location: { search: "" },
  URLSearchParams, Promise, Set, Map, JSON, Math, Number, String, Date, Object, Error, console, encodeURIComponent,
  addEventListener() {}, innerWidth: 900, innerHeight: 600, devicePixelRatio: 1,
  setTimeout: (f) => timers.push(f),
  fetch: (url, opts = {}) => new Promise((resolve, reject) => {
    requests.push({
      url, body: opts.body && JSON.parse(opts.body),
      settle: (ok, body) => ok ? resolve({ ok: true, status: 200, json: async () => body }) : reject(new Error("network down")),
    });
  }),
});

const flush = () => new Promise((r) => setImmediate(r));
const edits = () => requests.filter((r) => r.url.startsWith("api/edit"));
let failures = 0;
function check(name, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${ok ? "" : " " + JSON.stringify(detail)}`);
  if (!ok) failures++;
}

function snapshot(version, gain) {
  const ch = (id) => ({ id, name: id, level: 0, db: -60, gain: 0.5, muted: false, gainPending: false, pendingGain: 0, gainSeq: 0,
    mutePending: false, pendingMute: false, muteSeq: 0, shownGain: id === "c" ? gain : 0.5, shownMute: false });
  return { view: "B", version, tick: 0, running: false, channels: [..."abcdef"].map(ch), error: "",
    commands: { accepted: 0, applied: 0, refused: 0, rejected: 0, canceled: 0, outstanding: 0, delayMs: 0, rejectNext: 0 } };
}

// Answers the outstanding poll with a snapshot whose channel c shows gain.
async function poll(version, gain) {
  const r = requests.filter((x) => x.url.startsWith("api/state")).pop();
  r.settle(true, snapshot(version, gain));
  await flush();
  timers.shift()(); // the page's next poll, 100 ms later
  await flush();
}

(async () => {
  vm.runInContext(fs.readFileSync(page, "utf8"), context, { filename: page });
  await flush();
  await poll(1, 0.5);
  const fader = byId.strips.children[2].children[1]; // channel c: name, fader, …

  // Two edits queued: only the first POST starts.
  fader.value = "0.3"; fader.dispatch("change");
  fader.value = "0.31"; fader.dispatch("change");
  await flush();
  check("only the first of two queued edits is submitted", edits().length === 1 && edits()[0].body.value === 0.3,
    edits().map((r) => r.body));

  // Its answer starts the second, with the second value.
  edits()[0].settle(true, { accepted: true, sequence: 1 });
  await flush();
  check("the second edit starts after the first is answered, with its own value", edits().length === 2 && edits()[1].body.value === 0.31,
    edits().map((r) => r.body));

  // While the second is unanswered, an older snapshot leaves the fader alone.
  await poll(2, 0.5);
  check("an older snapshot keeps the latest value while an edit is unanswered", fader.value === "0.31", fader.value);

  // Its answer clears the guard: the next snapshot is shown.
  edits()[1].settle(true, { accepted: true, sequence: 2 });
  await flush();
  await poll(3, 0.42);
  check("the guard clears once every edit is answered", fader.value === "0.42", fader.value);

  // A failed submission lets the next through and is not retried.
  fader.value = "0.2"; fader.dispatch("change");
  fader.value = "0.21"; fader.dispatch("change");
  await flush();
  edits()[2].settle(false);
  await flush();
  check("after a failed submission the next edit is submitted", edits().length === 4 && edits()[3].body.value === 0.21,
    edits().map((r) => r.body));
  edits()[3].settle(true, { accepted: true, sequence: 3 });
  await flush();
  check("the failed submission is never retried", edits().filter((r) => r.body.value === 0.2).length === 1, edits().map((r) => r.body));
  check("the failure is shown", byId.net.textContent.startsWith("edit") , byId.net.textContent);

  console.log(failures ? `${failures} failed` : "all passed");
  process.exit(failures ? 1 : 0);
})();
