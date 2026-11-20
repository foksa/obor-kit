package herdrapp

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
)

// Commands is every command this client sends in `herdr-app do` and, for each, the arguments it puts in it: the one
// list of what obor depends on in herdr-app's command surface. TestContract checks it against the committed copy of
// `herdr-app commands` (testdata/herdr-app-0.1.16.commands.json, the version MinVersion names); a call that sends a
// new command or argument adds it here in the same change. The CLI's verbs beside them are `ls`, `do <command>
// <json>`, `commands` and `--version`.
var Commands = map[string][]string{
	"space.new":    {"name", "folder"},
	"space.rename": {"id", "name"},
	"space.close":  {"id", "confirmed"},
	"tab.close":    {"id", "confirmed"},
	"tab.new":      {"name", "space", "folder"},
	"tab.rename":   {"id", "name"},
}

//go:embed testdata/herdr-app-0.1.16.commands.json
var contract []byte

// Contract is the committed copy of herdr-app MinVersion's `herdr-app commands`: what Commands is checked against and
// what Drift compares an installed app with.
func Contract() []byte { return slices.Clone(contract) }

// CommandsOf parses a `herdr-app commands` answer: each command's name and its argument names. An answer with no
// command is an error (a spelling drift that parsed as nothing is not a contract).
func CommandsOf(b []byte) (map[string][]string, error) {
	var list []struct {
		Name string `json:"name"`
		Args []struct {
			Name string `json:"name"`
		} `json:"args"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("herdr-app commands: unreadable answer: %w", err)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("herdr-app commands: no commands in the answer")
	}
	out := make(map[string][]string, len(list))
	for _, c := range list {
		var args []string
		for _, a := range c.Args {
			args = append(args, a.Name)
		}
		out[c.Name] = args
	}
	return out, nil
}

// Drift lists how an installed app's `herdr-app commands` answer differs from the committed copy, one line per
// command: one the app lacks, an argument it lacks or adds, one the app has that the copy does not. The commands'
// order and their descriptions are not compared.
func Drift(installed []byte) ([]string, error) {
	have, err := CommandsOf(installed)
	if err != nil {
		return nil, err
	}
	want, err := CommandsOf(contract)
	if err != nil {
		return nil, err
	}
	var out []string
	for name, args := range want {
		got, ok := have[name]
		if !ok {
			out = append(out, name+": command gone")
			continue
		}
		for _, a := range args {
			if !slices.Contains(got, a) {
				out = append(out, name+": no argument "+a)
			}
		}
		for _, a := range got {
			if !slices.Contains(args, a) {
				out = append(out, name+": new argument "+a)
			}
		}
	}
	for name := range have {
		if _, ok := want[name]; !ok {
			out = append(out, name+": new command")
		}
	}
	slices.Sort(out)
	return out, nil
}
