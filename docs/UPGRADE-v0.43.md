# Upgrading an obor project from v0.42 to v0.43

## v0.43.0 (the model table lives in your config)

**`agents.models` is the only model table.** Up to v0.42 obor resolved eight model names from a table compiled into
the binary (opus, sonnet, haiku, fable → claude; deepseek → opencode; terra, sol, luna → codex), and
`agents.models.<name>` only added a model or overrode one of those. From v0.43 a model name means exactly what your
`.obor/config.json` says under `agents.models`: a new model, a new version or a renamed id is a config edit, not an
obor release.

- Every entry names its `kind` (`claude`, `codex`, `opencode` or a plugin kind); `id` is the model id the CLI gets,
  verbatim (`""`: the kind's own default).
- A version or a variant is one more entry, under any name without `:`, `,` or whitespace:

  ```json
  "models": {
    "opus":     { "kind": "claude", "id": "opus" },
    "opus-4.6": { "kind": "claude", "id": "claude-opus-4-6" },
    "opus-1m":  { "kind": "claude", "id": "opus[1m]" },
    "atra":     { "kind": "codex",  "id": "gpt-6.2-atra" }
  }
  ```

  Then name it anywhere a model goes: `"hard": ["opus-4.6:high"]`, a spec's `Model: atra`, `--model opus-1m`.
- A name with no entry is refused everywhere (`agents.difficulty`, `agents.roles`, a spec's `Model:`, `--model`,
  `obor pause --model`). For one of the eight names above the refusal carries the entry to paste.
- `agents.difficulty` names all three levels: `obor config validate` fails on a missing one (a project `obor init`
  set up has all three). A level the config left out used to fall back to a default; add it if validate names it,
  for example `"medium": ["sonnet", "deepseek"]`.
- `obor setup show` prints the models table from the config (name → kind, model id).
- The web page's Setup tab lists the config's models with no "project's" mark.

The step below adds an entry for each of the eight former names your levels and roles use (with its former kind and
id) and writes nothing else; run it in the project's main checkout and commit `.obor/config.json`. A second run
changes nothing. A name only a spec's `Model:` line or a `--model` uses is not in the config: add its entry by hand
(the refusal prints it).

```sh
obor setup agents
```
