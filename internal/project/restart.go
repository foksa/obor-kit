package project

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
