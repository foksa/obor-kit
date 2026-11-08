# Quarantined Codex helper: who is affected, and what `obor setup` should do

Researched 2026-11-08 on macOS 27.0.1 (26A434), Apple Silicon, Homebrew 7.0.12, codex-cli 0.167.0 (Homebrew cask), herdr 0.9.5.
Context: T-1045 (obor's Codex starts failed with "timed out negotiating with the code-mode host" until the owner ran `xattr -dr`).

## Answer first

**Who is affected: every Homebrew-cask user on macOS 27, not only obor.** Homebrew leaves `com.apple.quarantine` on every file of
the cask. macOS 27 shows a Gatekeeper dialog once per binary the first time it is executed, and again for each new cask
version. `codex`, `codex-code-mode-host` and `codex-exec-server` each get their own dialog. The rest is how the dialog reaches the user:

- **At a screen:** Codex starts the host as a child process on every tool call, and the first tool call blocks until the
  dialog is answered. If the user clicks Open, the call passes (slowly) and the flag becomes `…c1` (user-approved). It
  looks like a one-off hiccup.
- **Unattended (obor's agents: `obor serve` starting Codex in a herdr pane, nobody looking):** the dialog sits unanswered,
  Codex's handshake timer expires, and every tool call fails "timed out negotiating with the code-mode host". Nothing in
  the pane says a dialog is open. This is what the owner hit.
- **Plain interactive `codex` in a terminal:** the same, but the person is there, so they see the dialog and click. A
  user who dismisses it, or is on a remote/SSH session or away, gets the same failure as obor.
- **Upstream confirms this is the mechanism** (openai/codex #49780, #47521, below): same dialog, same error, same cause.

**I did not reproduce the hang myself.** Every quarantined copy I ran passed (details below). The owner got two
Gatekeeper prompts during my trials and approved them; that is the likely reason they passed, not evidence that quarantine is
harmless. Unattended reproduction needs a copy with a fresh inode and nobody to click, which would pop a dialog on the
owner's screen, so I stopped (lead's instruction).

**Recommended `obor setup` behaviour: warn only, with a corrected fix line; at most an explicit `--fix` the user confirms.
Do not auto-fix, do not push npm as the answer.**

1. Keep the T-1045 warning (it names the right files), but **drop `brew reinstall --cask codex --no-quarantine` from the
   fix line**: Homebrew 7.0.12's `brew install --help` has no `--no-quarantine` option (grep for "quarantine" is empty;
   the flag was deprecated and removed; I did not run it). Offer `xattr -dr com.apple.quarantine <version dir>` or a plain `brew reinstall --cask codex`
   (#47521 reports it re-checks fresh files; unverified here).
2. Add `obor setup mcp --fix-quarantine` (name is the lead's call) that runs exactly that `xattr -dr` on the resolved cask
   version dir after the user types a confirm. Reasons it is an explicit action and not silent: it removes a macOS
   security check, and the files are OpenAI-signed (Developer ID, Team 2DC432GLL2, #47521) but the user should decide.
   The fix must be redone after every `brew upgrade` (#49780: approval is not carried over for binary-only casks).
3. Make the failure diagnosable at the unattended point: when a Codex start's tool call times out with that error, obor
   can say "a Gatekeeper dialog may be open on the Mac screen for codex-code-mode-host" instead of leaving a bare timeout.
   (Idea only; not tested.)
4. npm is an alternative to mention, not to require (section 5).

The check itself (is the file flagged `com.apple.quarantine`) is the right trigger for the warning; the `0x40` bit
(user-approved) is a refinement: a flagged file whose flag already has `0x40` was approved and will not prompt
(section 3), so the warning could skip it. #49780 shows `0181 → 01c1` after Open.

## 1. What I ran

Method: `cp -Rc` (APFS clone, new inodes) of `/opt/homebrew/Caskroom/codex/0.167.0` into the scratchpad; the real install
was never touched (it now carries only `com.apple.provenance`, the owner having cleared the flag). Quarantine re-added with
`xattr -w com.apple.quarantine '<flags>;<hex time>;<agent>;<uuid>'`. Run (a) in a throwaway `herdr --session probe-quar`
(workspace + `herdr agent start <name> --kind codex --pane … -- -s workspace-write -a never --no-daemon`, then
`herdr agent prompt` "Run the shell command: echo PROBE_OK. Then reply with just the word done."), with a scratch
`CODEX_HOME` holding a copy of the owner's `auth.json` (deleted afterwards) and the copy's `bin/` first on the pane's PATH. The pane shell's profile
resets PATH, so I exported it inside the pane; `which codex` showed the copy. The session was stopped with
`herdr --session probe-quar server stop`; no process of the copy remained.

| # | Copy | Flags | Result | "Worked for" |
|---|---|---|---|---|
| 1 | clone A | helper `01c1;6af071dc;;<uuid>`, others none | tool call ran, `PROBE_OK` printed | 5 s |
| 2 | clone B (fresh inode) | all files `0181`, helper included | tool call ran | 12 s |
| 3 | `0083;…;Chrome` (browser-style) | prepared, **not run** | – | – |
| 4 | `0081;…;curl` | prepared, **not run** | – | – |

- Trial 2's 12 s versus 5 s fits a dialog waiting for the owner's click (inference; the dialog text was not captured).
- Trials 3/4 were cancelled when the owner objected to more dialogs.
- Trial 1 also ran `codex-code-mode-host --help` directly from a shell with the `01c1` flag: it ran and exited 0 (a direct
  launch from a terminal child, not via Codex).
- (b) a plain interactive codex: the same binary and TUI as in (a), the pane being an ordinary terminal; I did not run it
  separately. What the owner can check: on a freshly flagged cask copy, run `codex`, ask it to run `echo hi`, and look for a
  Gatekeeper dialog "…codex-code-mode-host… downloaded from the Internet" plus, if it is left open, the error
  `timed out negotiating with the code-mode host` in the TUI.
- **Owner observation:** the owner saw a Gatekeeper prompt per binary (`codex`, `codex-code-mode-host`) and approved both.
  Text not captured. Another prompt appeared while they pressed Esc; not identified.
- Limitation: my herdr server was started from a sandboxed Claude Code shell, not from obor serve, so the responsible-process
  chain differs from obor's. I do not know whether that changes when a dialog is raised.

## 2. Does Gatekeeper prompt or silently deny a quarantined helper spawned by a CLI?

It prompts, even for a non-GUI child. Sources:

- openai/codex #49780 (macOS 27.0.1, Homebrew 7.0.7, cask 0.159.0→0.159.2): after `brew upgrade --cask codex` each program asks
  once before first run: *"codex-code-mode-host" is an app downloaded from the Internet. Are you sure you want to open it?
  Homebrew Cask downloaded this file … Apple checked it for malicious software and none was detected.* Same dialog for
  `bin/codex` (Homebrew runs it to generate completions), the helper, and `codex-path/rg`. `syspolicyd` logs
  `Prompt shown (5, 0)` with `bundle_id: NOT_A_BUNDLE`. "On an unattended Mac nobody sees it, and the program fails."
  The reporter says on macOS 26 in July the same notarized files ran without a dialog (#32171), so this looks new with macOS 27.
- openai/codex #47521 (0.156.0 and a 0.161.0 comment, Intel and Apple Silicon): the helper "blocked by Gatekeeper". The
  0.161.0 comment shows the dialog ("Apple could not verify codex-code-mode-host…") and stderr
  `code-mode host exited during handshake` or `timed out negotiating with the code-mode host   # while the dialog is left
  open`. `--disable code_mode_host` is no workaround (shell execution is then disabled, "fails closed"). It reports a
  byte-identical `cp -R` copy (same quarantine value) running fine while the original kept failing, and guesses macOS cached a rejection for the original file
  (first launch before the notarization ticket was reachable). That matches my result 1/2 (fresh clones pass); the
  cached-verdict explanation is the reporter's guess, not verified. The same issue says 0.156.0's helper was
  "Unnotarized Developer ID" per `spctl`; 0.161.0's is "Notarized Developer ID". I did not run `spctl` on 0.167.0.
- Net: the dialog is a per-file, per-inode first-run assessment. Cloning gives a new file, so it can behave
  differently from the original; this is why a copy is a poor reproduction of an unanswered dialog.

## 3. Flag bits (`01c1` vs `0181`)

Format: `flags;hex-timestamp;agent;uuid`. Bits, with confidence:

| Bit | Meaning | Source / confidence |
|---|---|---|
| `0x0001` | set by a downloading process (quarantined) | eclecticlight; `0081` = AirDrop, `0083` = internet download. Medium |
| `0x0002` | sandboxed quarantiner (`0083`) | eclecticlight. Medium |
| `0x0040` | **user approved** (Gatekeeper opened it once) | #49780 states `0181 → 01c1 (USER_APPROVED_FLAG set)` after clicking Open; eclecticlight: "00c1 – allowed to execute". **High** for this bit |
| `0x0080` | (set on every value seen here) | not found documented |
| `0x0100` | item was moved/copied from where it was quarantined | eclecticlight, one author. Low |
| `0x0200` | seen in `03c1` (#47521) | not found documented |

So `0181` = quarantined, not yet approved; `01c1` = quarantined and the user approved it. The owner's real install had
the helper at `01c1` (already clicked once, after the 13:58 install) and `codex`/`codex-exec-server` at `0181`; the
helper's flag therefore changed during the session, which fits a dialog answered late. I did **not** read the xnu source or Apple's private
`quarantine.h` (`kQtnFlag*`); the `0x40` identification rests on the issue and eclecticlight, not on Apple documentation.

## 4. Do Homebrew or OpenAI mention it?

- The cask (`Homebrew/homebrew-cask/Casks/c/codex.rb`, fetched with `gh api`) has only `binary "bin/codex"`,
  `generate_completions_from_executable`, and `zap`. No `postflight`, no `xattr`, no caveat. Homebrew quarantines
  everything it downloads for a cask; for a cask with no `app` artifact the old version's approval is never carried over
  (#49780 cites `Cask::Upgrade.quarantine_release_decision` handling only `Artifact::App`; I did not read Homebrew's code).
- `brew install --help` (7.0.12): no quarantine option. T-1045's `--no-quarantine` line is therefore likely stale.
- OpenAI's README lists `brew install --cask codex` (WebSearch snippet of github.com/openai/codex); I found no OpenAI
  doc text on quarantine. OpenAI's FAQ covers formula→cask migration only.
- Open issues: **openai/codex #47521** (helper blocked by Gatekeeper), **#49780** (approval not carried over, every
  upgrade asks again per tool), **#38816** (same timeout message on 0.147.0 via VS Code remote; cause not established there),
  #46607 (computer-use helper re-asks after updates). Nothing found in Homebrew/homebrew-cask (`gh search issues "codex quarantine"` returned `[]`).
- Third-party guides (verdent.ai etc.) recommend `xattr -d com.apple.quarantine`; not authoritative.

## 5. Does the npm install (`@openai/codex`) avoid it?

Probably, **not verified**. I did not install it. Reasoning: `npm view @openai/codex` shows a small JS wrapper
(`bin/codex.js`) with per-platform optional dependencies (`@openai/codex-darwin-arm64` etc.); npm unpacks tarballs without
setting `com.apple.quarantine` (that flag comes from quarantine-aware downloaders such as browsers and Homebrew's cask code).
Counter-evidence: #34737 shows the npm native binary can still be refused (Gatekeeper: `CSSMERR_TP_CERT_REVOKED` for 0.120.0 /
0.116.0, files had `com.apple.provenance` and no quarantine), so npm is not immune to macOS trust checks, only to this
quarantine prompt. To check: `npm i -g @openai/codex` in a scratch prefix, then `xattr -lr` the vendor dir for
`com.apple.quarantine`, and confirm the package includes `codex-code-mode-host`.

## Unknown / how to find out

- Dialog text and which binary each prompt was for: owner can reproduce by re-flagging a *scratch copy* and watching.
- Whether an unanswered dialog is what timed out obor's reviewer start (high likelihood from #47521/#49780; not reproduced here).
- Whether macOS 26 users are affected (#49780 says no dialog on 26; unverified).
- Whether `brew reinstall --cask codex` clears it (#47521 says so; I did not run it).
- Real `quarantine.h` flag names.

## Side effect to undo

My `brew cat --cask codex` turned Homebrew developer mode on ("developer mode has been automatically turned on").
Undo with `brew developer off`.

## Sources

- https://github.com/openai/codex/issues/49780 · https://github.com/openai/codex/issues/47521 · https://github.com/openai/codex/issues/38816 · https://github.com/openai/codex/issues/34737 · https://github.com/openai/codex/issues/32171
- https://github.com/Homebrew/homebrew-cask/blob/HEAD/Casks/c/codex.rb
- https://eclecticlight.co/2019/04/13/why-all-those-quarantine-flags/ · https://eclecticlight.co/2020/10/29/quarantine-and-the-quarantine-flag/
- `brew install --help`, `npm view @openai/codex`, `xattr -l` on the real install (Homebrew 7.0.12, 2026-11-08)
- internal: commit 35f5dc06 (T-1045), `internal/agents/codex/codex.go` `InstallWarnings`
