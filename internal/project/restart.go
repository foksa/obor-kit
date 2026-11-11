package project

import "errors"

// RetryHint marks a failed start's error whose message already tells the owner how to run the start again: a hint
// that names the command (RestartCmd), or a failure with advice of its own (a drive's no-model line). A caller that
// adds that command to the whole failure (obor drive's FAIL line) adds none of its own when the error carries one.
func RetryHint(err error) error { return &retryHint{err: err} }

type retryHint struct{ err error }

func (e *retryHint) Error() string { return e.err.Error() }
func (e *retryHint) Unwrap() error { return e.err }

// RetryHinted reports whether err is, or wraps, a failure whose message already says how to run its start again.
func RetryHinted(err error) bool {
	var h *retryHint
	return errors.As(err, &h)
}

// RestartCmd is the command that starts agent again: own when the start has one (a drive's `obor drive <T-xx>`, which
// keeps its prompt), else `obor restart <agent>`. With no recorded session the restart adds --fresh, which a plain
// restart needs to start a new session; pane adds --pane ("" leaves it out: a drive places its own pane).
func RestartCmd(own, agent string, hasSession bool, pane string) string {
	if own != "" {
		return own
	}
	cmd := "obor restart " + agent
	if !hasSession {
		cmd += " --fresh"
	}
	if pane != "" {
		cmd += " --pane " + pane
	}
	return cmd
}
