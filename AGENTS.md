# AGENTS.md — dfx2

dfx2 is the planned successor to dfx (immediate-mode UI on Dear ImGui) and
dfw (webview windows around a Go HTTP server): one Go framework, without
cgo, that draws native UI itself and shows web UI in the system webview,
above `df`'s foundation. It is in planning. The root module has no code yet;
the work so far is design notes and experiments.

## Start here

1. `docs/journal/`: read the most recent entries first (see "Project memory"
   below).
2. `docs/future/direction.md`: what dfx2 is, the decisions made so far and
   why, and the open questions.
3. `docs/future/text.md`: the text system, the furthest-along part, with the
   spike's measurements and the problems it hit.
4. `docs/future/mygo-study.md`: mechanisms from mygo worth rebuilding,
   roughly in build order.
5. `docs/future/roadmap/`: the work, as prompts. The `dfx2` epic card frames
   the design work.

## Layout

- `docs/future/`: intent: design notes and the roadmap. Nothing here
  describes built behavior. That goes into `docs/current/` once code exists.
- `docs/journal/`: project memory.
- `experiments/<name>/`: spikes, each its own Go module, so the root module
  never builds them. Each has a README saying what it tries and how to run
  it. `experiments/text-spike` is a no-cgo GTK window with a pure-Go text
  system, and `go run ./compare` measures it against Pango.

## Related trees

- `../dfx`, `../dfw`, `../df`: the stacks dfx2 replaces and the foundation
  it builds on. dfx's and dfw's products keep using them until they migrate.
- mygo, studied for ideas only: `github.com/egoist/mygo` at `da6e0fbf`,
  cloned at `~/Sandbox/mygo` on Michael's machine. Don't add it as a
  dependency or copy its code.

## Constraints decided so far

- **No cgo.** Call native code through purego (Linux) or `syscall`/COM
  (Windows), never `import "C"`. Experiments build with `CGO_ENABLED=0`.
- **purego callbacks are never freed.** Create one per signature at startup
  and route by user data, never one per window or call.
- **The platform layer lives in dfx2.** Platform packages never import UI
  packages.

## Project memory

Durable knowledge about this project lives in `docs/journal/`, dated files
`docs/journal/YYYY-MM-DD.md`. This is project memory; it does not go in
harness-local storage (`.claude/` or equivalent), where it's invisible to every
other harness and collaborator and dies with the host. Concretely: do not write
to your harness's memory directory or memory tool for this project — even when
the harness presents it as the default place for durable knowledge. That tool is
the silo this convention exists to replace; the journal is the only durable home.

On arrival, read the most recent entries to pick up where the last session left
off, before you start changing things. Treat them as prior-session context, not
verified truth — if an entry conflicts with the code or a `docs/current/` doc,
the code wins.

Write the smallest entry that carries the session's durable insight, and nothing
more. The test for every line: *would a competent agent get this wrong, or waste
time rediscovering it, working from the tree alone?* If it's recoverable by
reading the code, the diff, `docs/current/`, or git history, leave it out.

That filter keeps four kinds of thing and discards the rest:

- **Decisions whose rationale isn't visible in the result** — why a value was
  chosen, what a line guards against, why something that looks like dead code or
  a no-op is load-bearing.
- **Deliberate non-actions** — a change you considered and chose not to make, so
  the next agent doesn't "fix" it. An unchanged file leaves no trace in a diff.
- **Couplings that span files** — two places that must move together, an ordering
  that matters, an assumption one file makes about another.
- **Live state** — what's unverified, unfinished, or waiting on something
  external.

Skip change inventories, restatements of the diff, and play-by-play of how you
worked. There's no write-time approval gate; Michael reviews on commit. Append
to the day's file if it exists, and write the few lines you'd want the next agent
to read — honest and self-contained.

## Commits

The operator commits; agents don't. Never run `git commit` or `git push` in
this repo. Finish the edit, leave the change in the working tree (staged is
fine), report what changed, and hand off — the uncommitted diff is the review
queue and the commit is the operator's act of acceptance. Approval of a change
is not direction to commit; only an explicit instruction to commit is, and only
for that commit.

## Roadmap

This repo's roadmap lives in `docs/future/roadmap/` — one frontmatter-markdown
item per file, per the roadmap convention in the grimoire
(software/conventions/roadmap-convention.md). You may add items freely: write
the file directly with required `title`, `state: inbox`, and `created:` (today,
YYYY-MM-DD), optional `tags`/`source`/`log`, and a body that is a small, clear
prompt -- the problem or solution to execute, not documentation of it; trust
the code and the day's journal entry for what's discoverable, and point a
`log:` stamp at the specific journal entry when a card leans on hard-won
context. Everything above the first `##` heading is the prompt; supporting
material that isn't the prompt goes in named sections below it (`## why` for
justification, `## background` for a longer description), which are
conventional, never required, and never validated. The filename is the slug of
the title (lowercase ASCII, hyphens; discard every other character); never
overwrite an existing file. Read sibling items for the shape.

Hard rules: never touch `order.yaml` (priority is the operator's judgment, set
at triage); never commit roadmap changes unless directed — the uncommitted diff
is the review queue; never delete items; edits change only the lines that
express them. Label the kind from the house set when one fits: defect,
documentation, enhancement, epic, feature, story; add `spike` alongside it when
the work carries unknowns that need discovery.
