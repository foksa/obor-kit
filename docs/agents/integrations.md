# Integrations

An integration (a ticket tracker such as Jira, a chat bot, a dashboard) is a process the project runs next to obor:
a script or a service, out of process, nothing compiled in (`docs/plan/architecture-target.md` §2c). It uses
three things obor provides: a custom role with **verbs** to take a task and brief the lead, the **event feed** to
follow what happens, and a spec's **`Ref:` line** to tie a task to the external id.

## The role and its verbs

The integration runs as a custom role: an `agents.roles.<name>` row with a doc `docs/agents/<name>.md`, and
`OBOR_ROLE=<name>` in its environment. Its `verbs` are the wire methods serve admits it to as it admits the lead and
the owner:

```json
"agents": {
  "roles": {
    "tracker": {"verbs": ["spec_set", "dispatch"]}
  }
}
```

| Verb | The CLI that sends it | What it lets the role do |
|---|---|---|
| `spec_set` | `obor spec set <T-xx> <path>` | store a task's spec (set by `<project.agent_prefix>-<name>`) |
| `dispatch` | `obor dispatch <T-xx> …` | run a task, or queue it |
| `watch` | `obor watch <T-xx>` | a line to the lead when tasks merge |
| `send` | `obor send <project> "<text>"` | a line to another project's lead (that project's config grants it) |
| `brief` | `obor brief <role-agent> "<text>"` | a brief to a role agent |

- Verbs add rights, never remove one: the owner and the lead keep every method, a built-in role (worker,
  integrator, reviewer …) never takes verbs.
- `obor config validate` refuses a verb that is not in the table (`unknown wire method`), one listed twice, and
  `verbs` on a built-in role. `obor setup show` lists each role's verbs (`tracker - (no model named) [verbs spec_set
  dispatch]`).
- A call the role has no verb for is refused with the method's own refusal, then the verb and the key that grants it:
  `dispatch: the lead or the owner dispatches, not pj-tracker (role tracker): role tracker has no verb dispatch (add
  it to agents.roles.tracker.verbs)`.
- The role is otherwise a custom role: no pane or lane of its own unless someone places one (`obor setup role`), git
  reads only, writes only its `write` list, never pushes. The feed and `obor status` need no verb.

## The event feed

`obor events [--follow] [--after <seq>] [--json]` prints the project's event log after the cursor `<seq>` (0, the
default: from the first record), one line per record, in seq order.

- `--json`: each line is one record as serve wrote it plus its task's ref:
  `{"seq":42,"ts":"2026-11-08T10:00:00Z","task":"T-xx","attempt":1,"step":"merged","kind":"fact","data":{"head":"…"},"ref":"TICKET-1"}`.
  `seq` is the cursor; `kind` is `intent`, `result` or `fact`; `step` and `data` are the log's
  (`internal/app/event`: `merged`, `dispatched`, `submitted`, `needs-you` …); `ref` is the task's stored spec's
  `Ref:` line (absent when the task has none, and on records of no task: messages, sessions, pauses).
- Without `--json`: `<seq> <ts> <task> <kind> <step>[ ref <ref>][ — <line>]`, the line as `obor status` and obor web
  word the record.
- **The cursor rule**: a record is printed once per cursor, in seq order, nothing skipped. The integration stores the
  seq of the last record it handled and asks `--after` that seq next time: a restart repeats nothing it had and misses
  nothing after it. Handle a record idempotently by its seq.
- Serve up: serve's feed (the `events` wire method). Serve down: the same records from the store
  (`.obor/state/obor.db`, read-only). A cursor past the end prints nothing and exits 0.
- `--follow` keeps the connection and prints each new record as serve writes it; across a serve restart (or while
  serve is down) it reads the store, then serve again, from the last seq it printed. INT or TERM ends it, exit 0.

## The sample: a ticket to merged

`internal/app/ticket_test.go` is the sample integration as a Go test helper (`tracker`), run end to end on fakes:
it takes `TICKET-1` of a fake tracker into obor (`spec_set` with `Ref: TICKET-1`, then `dispatch`), follows the feed
from its cursor, and on the task's `merged` record marks the ticket done. The same as a script:

The caller picks the task id in `id`: it matches `project.task_id_pattern` (default `T-[0-9]+[a-z]?`) and is unused.

```sh
export OBOR_ROLE=tracker
id=T-7
cursor=$(cat .tracker-cursor 2>/dev/null || echo 0)
printf '# %s — Fix the widget\n\nDifficulty: medium\nRef: TICKET-1\n' "$id" > "/tmp/$id.md"
obor spec set "$id" "/tmp/$id.md" && obor dispatch "$id"
obor events --follow --json --after "$cursor" | while read -r line; do
  seq=$(printf '%s' "$line" | jq -r .seq)
  if [ "$(printf '%s' "$line" | jq -r '.step + " " + .kind')" = "merged fact" ]; then
    ticket_done "$(printf '%s' "$line" | jq -r .ref)"   # the tracker's own API
  fi
  echo "$seq" > .tracker-cursor
done
```
