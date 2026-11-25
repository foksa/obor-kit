// Package osnotify shows a desktop notification for a terminal that has none of its own (internal/hc, internal/rex):
// on macOS a notification from osascript; on another platform none.
package osnotify

import (
	"cmp"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// Notifier shows a notification. Post is its seam (nil: osascript); GOOS names the platform it acts for ("": this
// one).
type Notifier struct {
	Post func(ctx context.Context, title, body string) error
	GOOS string
}

// Show posts title and body, bounded by ctx; its error is why nothing showed: another platform than darwin, or
// osascript's own message.
func (n Notifier) Show(ctx context.Context, title, body string) error {
	if cmp.Or(n.GOOS, runtime.GOOS) != "darwin" {
		return errors.New("no notifications outside macOS")
	}
	post := n.Post
	if post == nil {
		post = osascript
	}
	if err := post(ctx, title, body); err != nil {
		return errors.New("osascript: " + err.Error())
	}
	return nil
}

// script is the script osascript runs: its title and body are its arguments, never part of its source.
var script = []string{
	"on run argv",
	"display notification (item 2 of argv) with title (item 1 of argv)",
	"end run",
}

// osascript posts the notification: the title and body follow the script as its arguments (after `--`, so a title
// that starts with a dash is no option). A failure carries osascript's own message.
func osascript(ctx context.Context, title, body string) error {
	args := make([]string, 0, 2*len(script)+3)
	for _, l := range script {
		args = append(args, "-e", l)
	}
	args = append(args, "--", title, body)
	out, err := exec.CommandContext(ctx, "osascript", args...).CombinedOutput()
	if err != nil {
		if m := strings.TrimSpace(string(out)); m != "" {
			return errors.New(m)
		}
		return err
	}
	return nil
}
