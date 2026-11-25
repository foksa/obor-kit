package app

import (
	"context"
	"fmt"
	"obor/internal/app/port"
	"strings"
	"sync"
)

// FakeAgents is the package's one Launcher and Prompter fake, and a Poster that types what it posts (the lead's lines
// are typed lines): it records every start, stop and prompt, in order, and starts, stops and types nothing. A test adds
// behaviour with the On hooks: each runs inside its call, after the call is recorded, outside the fake's lock, and
// gives the call's outcome. Safe for the loop's goroutines and the test at once; a hook is set while no call runs.
type FakeAgents struct {
	// OnLaunch is each launch's outcome (nil: Started{Line: "STARTED <agent>"}): an agent that obeys its first prompt
	// before the start returns, a pane the start shows, a start held until released, a failed or trust-held start.
	OnLaunch func(context.Context, port.Launch) (port.Started, error)
	// OnStop is each stop's error (nil: stopped, "STOPPED <agent>"): a quit timeout, a stop only force gets past, a
	// pane event while the stop runs.
	OnStop func(ctx context.Context, agent, pane string, force bool) error
	// OnPrompt is each prompt's outcome (nil: typed): refused before typing, a scripted outcome per call, a prompt that
	// never returns.
	OnPrompt func(ctx context.Context, pane, text string, clear bool) (bool, error)

	mu       sync.Mutex
	launches []port.Launch
	events   []string // "start <agent> <pane>", "stop <agent> <pane> force=<force>"
	prompts  []Prompted
}

// Prompted is one Prompt (or Post) the fake took: its pane (a post's agent), text and clear, and began: its outcome's.
type Prompted struct {
	Pane, Text   string
	Clear, Began bool
}

func (f *FakeAgents) Launch(ctx context.Context, l port.Launch) (port.Started, error) {
	f.mu.Lock()
	f.launches = append(f.launches, l)
	f.events = append(f.events, "start "+l.Agent+" "+l.Pane)
	on := f.OnLaunch
	f.mu.Unlock()
	if on != nil {
		return on(ctx, l)
	}
	return port.Started{Line: "STARTED " + l.Agent}, nil
}

func (f *FakeAgents) Stop(ctx context.Context, agent, pane string, force bool) (string, error) {
	f.mu.Lock()
	f.events = append(f.events, fmt.Sprintf("stop %s %s force=%t", agent, pane, force))
	on := f.OnStop
	f.mu.Unlock()
	if on != nil {
		if err := on(ctx, agent, pane, force); err != nil {
			return "", err
		}
	}
	return "STOPPED " + agent, nil
}

func (f *FakeAgents) Prompt(ctx context.Context, pane, text string, clear bool) (bool, error) {
	f.mu.Lock()
	n := len(f.prompts)
	f.prompts = append(f.prompts, Prompted{Pane: pane, Text: text, Clear: clear})
	on := f.OnPrompt
	f.mu.Unlock()
	began, err := true, error(nil)
	if on != nil {
		began, err = on(ctx, pane, text, clear)
	}
	f.mu.Lock()
	f.prompts[n].Began = began
	f.mu.Unlock()
	return began, err
}

// Post types line to agent (the Poster of a loop's Inbox): a Prompt without clear.
func (f *FakeAgents) Post(ctx context.Context, _, agent, line string) (bool, error) {
	return f.Prompt(ctx, agent, line, false)
}

// Launches is every launch, in order.
func (f *FakeAgents) Launches() []port.Launch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]port.Launch(nil), f.launches...)
}

// Events is every start and stop, in order: "start <agent> <pane>", "stop <agent> <pane> force=<force>".
func (f *FakeAgents) Events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

// Stops is every stop, in order: "<agent> <pane> force=<force>".
func (f *FakeAgents) Stops() []string {
	var out []string
	for _, e := range f.Events() {
		if s, ok := strings.CutPrefix(e, "stop "); ok {
			out = append(out, s)
		}
	}
	return out
}

// Prompts is every prompt and post, in order.
func (f *FakeAgents) Prompts() []Prompted {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Prompted(nil), f.prompts...)
}

// Lines is every prompt and post as "<pane>: <text>", in order.
func (f *FakeAgents) Lines() []string {
	var out []string
	for _, p := range f.Prompts() {
		out = append(out, p.Pane+": "+p.Text)
	}
	return out
}

// Texts is every prompt's and post's text, in order.
func (f *FakeAgents) Texts() []string {
	var out []string
	for _, p := range f.Prompts() {
		out = append(out, p.Text)
	}
	return out
}

// Jobs is the job of every prompt and post (jobOf: "" for a line), in order.
func (f *FakeAgents) Jobs() []string {
	var out []string
	for _, p := range f.Prompts() {
		out = append(out, jobOf(p.Text))
	}
	return out
}
