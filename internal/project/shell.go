package project

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

// ShellQuote is s as one word of a POSIX shell's command line: as it is when it holds only characters no shell reads
// specially, else in single quotes (a quote in it closed, escaped and reopened). The one quoting of what obor types
// at a pane's shell: launch's env line and the hc adapter's agent command line.
func ShellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// EnvLine is the shell line that changes to cwd ("": stays where it is) and sets env, NAME=VALUE entries: every name
// with a value exported, every name with none ("NAME=") unset, in env's order. The one builder of the env line obor
// types at a pane's shell: launch's and the hc adapter's for a terminal herdr-app started.
func EnvLine(cwd string, env []string) string {
	var unset, export []string
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if value == "" {
			unset = append(unset, name)
		} else {
			export = append(export, name+"="+ShellQuote(value))
		}
	}
	var parts []string
	if cwd != "" {
		parts = append(parts, "cd "+ShellQuote(cwd))
	}
	if len(unset) > 0 {
		parts = append(parts, "unset "+strings.Join(unset, " "))
	}
	if len(export) > 0 {
		parts = append(parts, "export "+strings.Join(export, " "))
	}
	return strings.Join(parts, " && ")
}

// Typist is the terminal's side of the env line's handshake: text typed at a pane's shell and the pane's recent
// output read back (port.Terminal's Write and Recent).
type Typist interface {
	Write(ctx context.Context, pane, text string, submit bool) error
	Recent(ctx context.Context, pane string, lines int) (string, error)
}

// EnvLineResult is how a typed env line ended.
type EnvLineResult int

const (
	EnvLineDone   EnvLineResult = iota // the shell printed the success word: the whole line ran
	EnvLineFailed                      // the shell printed the failure word: the line failed
	EnvLineNoMark                      // neither word showed within the bound
)

// screenLines is how many of a pane's last lines of output a look for an echoed mark reads.
const screenLines = 60

// RunEnvLine types line (EnvLine's) at pane's shell and waits for the shell to say how it ended: the line followed by
// an echo of one of two words, the success word only when the whole line (its cd, its exports) succeeded (a `; echo`
// after a cd that failed still printed it) and the failure word when it failed, so a visible failure ends the wait at
// once instead of at the bound. The pane's recent output (not its visible screen, which can lag behind what the shell
// printed after an agent exits) is read every poll for at most bound for either word, the echoed one, which the typed
// line's quoted forms never match; output that cannot be read shows none. The shell's prompt drawn after the word
// is the shell back at its line editor: before that, zsh runs the line with the tty in cooked mode, where a start's
// command typed at once is echoed raw and cut at the tty's line limit (~1 KB on macOS). The error is the write's.
func RunEnvLine(ctx context.Context, t Typist, pane, line string, poll, bound time.Duration) (EnvLineResult, error) {
	okMark, badMark := newMark(), newMark()
	if err := t.Write(ctx, pane, line+" && echo "+okMark.typed+" || echo "+badMark.typed, true); err != nil {
		return EnvLineNoMark, err
	}
	deadline := time.Now().Add(bound)
	for {
		if screen, err := t.Recent(ctx, pane, screenLines); err == nil {
			switch {
			case strings.Contains(screen, badMark.echoed):
				return EnvLineFailed, nil
			case strings.Contains(screen, okMark.echoed):
				return EnvLineDone, nil
			}
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return EnvLineNoMark, nil
		}
		time.Sleep(min(poll, time.Until(deadline)))
	}
}

// mark is a word the env line echoes. typed is the word as typed, quoted in its middle so no wrap of the typed line
// on the screen reads as the echoed word.
type mark struct{ typed, echoed string }

var markSeq atomic.Int64

func newMark() mark {
	n := strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.FormatInt(markSeq.Add(1), 36)
	return mark{typed: `obor-env-"` + n + `"`, echoed: "obor-env-" + n}
}
