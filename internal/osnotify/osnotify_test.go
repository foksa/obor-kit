package osnotify

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// On darwin a notification shows with the title and body as they are (quotes and backslashes included); when it
// fails, or off darwin, Show's error is the reason.
func TestShow(t *testing.T) {
	ctx := context.Background()
	type call struct{ title, body string }
	var calls []call
	post := func(err error) func(context.Context, string, string) error {
		return func(_ context.Context, title, body string) error {
			calls = append(calls, call{title, body})
			return err
		}
	}

	t.Run("darwin shows it with title and body as given", func(t *testing.T) {
		calls = nil
		want := call{`obor: "w1" needs you \ now`, `it said 'hi' \n "quoted" \"`}
		if err := (Notifier{GOOS: "darwin", Post: post(nil)}).Show(ctx, want.title, want.body); err != nil {
			t.Fatalf("Show = %v, want shown", err)
		}
		if len(calls) != 1 || calls[0] != want {
			t.Errorf("post calls = %q, want one with %q", calls, want)
		}
	})
	t.Run("a failure is an error with osascript's message", func(t *testing.T) {
		calls = nil
		err := (Notifier{GOOS: "darwin", Post: post(errors.New("execution error: not allowed"))}).Show(ctx, "t", "b")
		if len(calls) != 1 {
			t.Fatalf("post calls = %q, want one", calls)
		}
		if err == nil || err.Error() != "osascript: execution error: not allowed" {
			t.Errorf("Show = %v, want osascript's message", err)
		}
	})
	t.Run("another platform is an error and shows nothing", func(t *testing.T) {
		calls = nil
		err := (Notifier{GOOS: "linux", Post: post(nil)}).Show(ctx, "t", "b")
		if err == nil || !strings.Contains(err.Error(), "outside macOS") {
			t.Errorf("Show = %v, want the platform as the reason", err)
		}
		if len(calls) != 0 {
			t.Errorf("post calls = %q, want none off darwin", calls)
		}
	})
}
