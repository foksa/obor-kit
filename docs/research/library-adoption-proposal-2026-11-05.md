# Library adoption proposal — 2026-11-05

Companion to the [architecture proposal](architecture-proposal-2026-11-05.md). This document proposes evaluations; it does not authorize dependency additions or change the repository's dependency policy.

## Recommendation

Use libraries where they let obor stop maintaining a clearly identified responsibility. The strongest initial candidate is the official MCP Go SDK. Go's standard-library `testing/synctest` is also worth adopting for suitable tests. An embedded durable-workflow engine offers a potentially larger reduction, but requires a separate architectural experiment.

Keep pinned dependencies, cgo-free builds, and the single-binary deployment. Judge adoption by implementation and tests actually deleted, compatibility preserved, and ongoing responsibility transferred upstream.

## Candidates

| Candidate | Responsibility it could replace | Assessment |
|---|---|---|
| [Official MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk) | Handwritten JSON-RPC handling, initialization, tool schemas, argument validation, and protocol-only tests | Best first trial: a bounded external protocol with an official implementation |
| [Go `testing/synctest`](https://pkg.go.dev/testing/synctest) | Some custom clocks, sleeps, polling, and synchronization scaffolding | Worth adopting for isolated concurrency and timer tests; no additional dependency |
| [Cobra](https://github.com/spf13/cobra) | Repeated command/flag parsing, usage handling, and help generation | Useful, probably modest savings; trial one command group |
| [go-workflows](https://cschleiden.github.io/go-workflows/) | Generic workflow persistence, retries, timers, and recovery machinery | Potentially substantial savings, with substantial migration and compatibility work |
| [Stateless](https://github.com/qmuntal/stateless) | Finite-state-machine configuration and transition dispatch | Lower priority: obor's transition table is already compact; effects and recovery carry much of the complexity |

These are source-based assessments, not measured migration results. No candidate has been integrated or benchmarked in obor as part of this proposal.

## 1. Official MCP Go SDK

Obor currently maintains its own MCP implementation in `internal/mcp`: JSON-RPC messages, initialization, tool listing, schemas, argument validation, and tool calls forwarded to serve. The official SDK provides server APIs, typed tool registration, and stdio transport. Its documentation includes support for the protocol revision obor currently names. [SDK documentation](https://github.com/modelcontextprotocol/go-sdk/blob/main/README.md)

At the architecture audit baseline, the MCP package contained 557 implementation lines. Only part would disappear: role checks, caller identity, job context, and mapping tools onto serve calls remain obor responsibilities.

**Expected benefit:** remove generic protocol mechanics and their dedicated tests, and transfer protocol evolution upstream. Keep tests for obor's authorization, identity, error mapping, job handling, and real agent integration.

**Evaluation:**

- Implement the existing worker and integrator tools with a pinned SDK version.
- Preserve role-specific tool visibility, derived identity, job context, and tool-error behavior.
- Delete the corresponding handwritten protocol path and redundant protocol-only tests.
- Measure net lines, binary size, startup behavior, and dependency footprint.
- Run `make check` and the required live conformance for every affected agent kind: Claude, Codex, and OpenCode.

The SDK fits the current published-spec exception in principle. Its selected version and transitive dependencies still need the normal cgo-free and build checks. Dependency adoption remains its own owner-approved task.

## 2. Standard-library `testing/synctest`

The repository already uses a Go version that includes `testing/synctest`. It provides isolated test bubbles, fake time, and synchronization through `synctest.Wait`. This can replace real-duration waits and some custom synchronization in suitable tests. [Go documentation](https://pkg.go.dev/testing/synctest)

Start with timer, retry, cancellation, and concurrent decision tests that can operate with in-memory fakes. Keep actual socket, Git, and process behavior in integration tests. The documentation explicitly advises avoiding real network operations and external processes inside a bubble; wrapping an existing subprocess-heavy fixture will not automatically make it deterministic.

**Expected benefit:** faster tests and less timing scaffolding. The important scenarios and assertions stay.

**Evaluation:** migrate one small behavior group, compare setup size and execution time, and verify that its synchronization and cancellation guarantees remain explicit. Avoid introducing a second general test framework around the standard-library package.

## 3. Cobra for the CLI

Cobra provides subcommands, flag handling, argument validation helpers, and generated help. Those overlap with repeated parsing and usage handling in `cmd/obor`. [Cobra documentation](https://github.com/spf13/cobra/blob/main/README.md)

Most workflow logic currently located in CLI commands would remain. Command and flag declarations also take code, so a smaller implementation is an outcome to demonstrate.

**Evaluation:** port one representative command group, including its unusual argument cases. Preserve positional text, `--` behavior, exit codes, and existing command spelling. Compare the full declarations, helpers, and tests against the implementation they replace. Use Cobra directly; generated scaffolding is unnecessary.

Adopt more broadly only if the trial removes meaningful repetition. This would require an explicit exception or relaxation of the current dependency policy.

## 4. Embedded durable workflows

Obor implements part of a durable execution engine: persisted progress, retries, pending effects, timers, and recovery after interruption. This is the largest generic responsibility a library might take over.

`go-workflows` supports embedded execution and a SQLite backend; its client and worker can run in the same process. An additional server is therefore not inherently required. The workflow code must follow deterministic-execution restrictions. [Project documentation](https://cschleiden.github.io/go-workflows/), [workflow requirements](https://github.com/cschleiden/go-workflows/blob/main/README.md)

Its SQLite backend uses `modernc.org/sqlite`, matching obor's driver family. Compatibility with obor's pinned version, connection ownership, migrations, and durability settings still needs verification. [SQLite backend source](https://github.com/cschleiden/go-workflows/blob/main/backend/sqlite/sqlite.go)

**Potential benefit:** express some procedures as sequential workflows while transferring generic persistence, timers, retries, and resumption to the engine.

**Obor still owns:**

- Task policy, acceptance criteria, and human decisions.
- Git ancestry, patch identity, conflict handling, and safe publication.
- Lane reservations and protection against reusing an active worktree.
- Agent identity, session routing, and proof that a job was fetched.
- Idempotency and recovery checks around external effects. An engine does not make Git operations and its database transaction atomic.

**Evaluation:** build an isolated prototype of one representative workflow with its external effects stubbed. Exercise interruption before an effect, after the effect but before its recorded completion, cancellation, retries, and a workflow-code upgrade with pending work. Identify exactly which obor mechanisms and tests could be deleted.

Before adoption, establish a migration path for existing histories and confirm that the engine preserves required durability and deployment properties. Check maintenance health and supported upgrade/versioning behavior as part of that decision; this source review alone does not establish production suitability.

The experiment succeeds only if the engine replaces equivalent machinery. Retaining two recovery systems for the same workflow would increase maintenance. This option requires a deliberate dependency-policy change and a separate architecture decision.

## Dependency policy

The current policy permits published-spec implementations and database engines that obor would otherwise implement itself. It makes the MCP SDK a natural candidate, while excluding general CLI and workflow libraries without an owner decision.

Recommended extension: allow a narrowly scoped, established library when an evaluation demonstrates deletion of a named responsibility and its maintenance burden. Retain:

- Pinned versions and review of the transitive dependency footprint.
- Cgo-free builds and single-binary deployment.
- One dependency change per task, with `make check` as the gate.
- Existing live-conformance requirements for changes to agent identity, MCP, or agent launch.
- Removal of the replaced path and its redundant tests in the adoption task, or an explicit, time-bounded migration reason.

## Evaluation order

1. **MCP SDK trial:** the clearest bounded replacement of externally specified behavior.
2. **`synctest` pilot:** reduce test timing machinery without adding a dependency.
3. **Durable-workflow experiment:** investigate the larger architectural opportunity before committing to a migration.
4. **Cobra pilot:** proceed when CLI cleanup is scheduled and a representative comparison can establish its value.

Successful adoption should leave obor with fewer responsibilities to maintain. A library that merely changes the syntax of existing code has a weaker case.
