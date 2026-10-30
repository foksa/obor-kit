# Tool gotchas — obor-kit specifics

Kit list: `templates/agents/gotchas.md` in the obor kit (herdr, Claude Code, Codex, bash). Here only obor-kit's own,
symptom → what to do.

- macOS `/bin/bash` 3.2 and BSD tools (`stat`, `sed -i`, `date`) → keep `bin/obor`, the githooks and tests 3.2- and BSD-safe.
- System python is 3.9 (no `tomllib`), and shipped code may not use python anyway → config is JSON, parsed in Go (internal/config).
- herdr agent names are global across workspaces (`o-01` was taken by axis-alies) → obor-kit's prefix is `ob-`.
