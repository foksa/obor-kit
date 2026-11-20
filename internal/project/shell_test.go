package project_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"obor/internal/project"
)

// typist is a pane that records what is typed at it and answers each read from reads, in order (the last answer
// repeats); a read may err. marks is the typed line's two words, the success word's echoed form first.
type typist struct {
	typed    []string
	writeErr error
	reads    []func(ok, bad string) (string, error)
	n        int
}

var typedMarks = regexp.MustCompile(` && echo obor-env-"([0-9a-z]+)" \|\| echo obor-env-"([0-9a-z]+)"$`)

func (p *typist) Write(_ context.Context, _, text string, submit bool) error {
	if p.writeErr != nil {
		return p.writeErr
	}
	if !submit {
		return errors.New("the line was not submitted")
	}
	p.typed = append(p.typed, text)
	return nil
}

func (p *typist) Recent(_ context.Context, _ string, lines int) (string, error) {
	if len(p.typed) == 0 {
		return "", errors.New("read before the line was typed")
	}
	m := typedMarks.FindStringSubmatch(p.typed[len(p.typed)-1])
	if m == nil {
		return "", errors.New("the typed line has no marks: " + p.typed[len(p.typed)-1])
	}
	read := p.reads[min(p.n, len(p.reads)-1)]
	p.n++
	return read("obor-env-"+m[1], "obor-env-"+m[2])
}

func run(t *testing.T, p *typist) (project.EnvLineResult, error) {
	t.Helper()
	return project.RunEnvLine(context.Background(), p, "pane1", "cd /x", time.Millisecond, 50*time.Millisecond)
}

// The handshake types the line with both words quoted (so the typed line never reads as an echoed word) and says what
// the shell printed: its success word, its failure word, or neither in the bound.
func TestRunEnvLineSaysWhatTheShellPrinted(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		reads []func(ok, bad string) (string, error)
		want  project.EnvLineResult
	}{
		{"the success word", []func(ok, bad string) (string, error){
			func(ok, bad string) (string, error) { return "% cd /x && echo obor-env-\"1\"\n" + ok + "\n% ", nil }}, project.EnvLineDone},
		{"the failure word", []func(ok, bad string) (string, error){
			func(ok, bad string) (string, error) { return bad + "\n% ", nil }}, project.EnvLineFailed},
		{"neither: only the typed line", []func(ok, bad string) (string, error){
			func(ok, bad string) (string, error) {
				return "% cd /x && echo " + strings.Replace(ok, "obor-env-", `obor-env-"`, 1) + `"`, nil
			}}, project.EnvLineNoMark},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &typist{reads: c.reads}
			got, err := run(t, p)
			if err != nil || got != c.want {
				t.Fatalf("RunEnvLine = %v, %v; want %v", got, err, c.want)
			}
			if len(p.typed) != 1 || !strings.HasPrefix(p.typed[0], "cd /x && echo ") || typedMarks.FindStringSubmatch(p.typed[0]) == nil {
				t.Errorf("typed %q, want the line then both words, quoted", p.typed)
			}
		})
	}
}

// The recent output is what is waited on: a stale read (no word yet) and a read that fails are polled again, and the
// word that shows later ends the wait.
func TestRunEnvLinePollsPastStaleAndFailedReads(t *testing.T) {
	t.Parallel()
	p := &typist{reads: []func(ok, bad string) (string, error){
		func(ok, bad string) (string, error) { return "last prompt\n% ", nil }, // output from before the line ran
		func(ok, bad string) (string, error) { return "", errors.New("terminal.read: boom") },
		func(ok, bad string) (string, error) { return "% line\n" + ok + "\n% ", nil },
	}}
	got, err := run(t, p)
	if err != nil || got != project.EnvLineDone {
		t.Fatalf("RunEnvLine = %v, %v; want the success word that showed on the third read", got, err)
	}
	if p.n != 3 {
		t.Errorf("%d reads, want 3: the stale one, the failed one, the fresh one", p.n)
	}

	always := &typist{reads: []func(ok, bad string) (string, error){func(ok, bad string) (string, error) { return "", errors.New("boom") }}}
	if got, err := run(t, always); err != nil || got != project.EnvLineNoMark || always.n < 2 {
		t.Errorf("reads that always fail: RunEnvLine = %v, %v after %d reads; want no mark, none of them an error", got, err, always.n)
	}
}

// A line that cannot be typed is the write's error.
func TestRunEnvLineWriteErrorIsTheError(t *testing.T) {
	t.Parallel()
	boom := errors.New("terminal.send: boom")
	if _, err := run(t, &typist{writeErr: boom, reads: []func(ok, bad string) (string, error){nil}}); !errors.Is(err, boom) {
		t.Errorf("RunEnvLine = %v, want the write's error", err)
	}
}
