package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The one-release window of config shape 2 (decisions 2026-11-09 "Config shape 2", docs/research/config-shape.md §4):
// a file of shape 1 is reshaped in memory on every load (readWith) and rewritten once by serve; the release after
// deletes Reshape, its table and fixtures, and refuses shape 1.

// move is one row of the shape table (config-shape.md §3): the member at from goes to to, through fix when set.
type move struct {
	from, to []string
	fix      func(json.RawMessage) json.RawMessage
}

// shapeMoves are the rows in shape 2's order (§2): a group's members land in the order of their rows. Each row moves a
// member that only shape 1 has, so a file of shape 2 (or the shape-2 part of a mixed one) is left as it is.
var shapeMoves = []move{
	{from: p("name"), to: p("project", "name")},
	{from: p("prefix"), to: p("project", "agent_prefix")},
	{from: p("task_id_pattern"), to: p("project", "task_id_pattern")},
	{from: p("terminal", "kind"), to: p("project", "terminal")},
	// lanes as a number or a list is shape 1's (lanesOld); shape 2's is an object
	{from: p("lanes"), to: p("lanes", "count")},
	{from: p("lanes"), to: p("lanes", "list")},
	{from: p("lanes_dir"), to: p("lanes", "dir")},
	{from: p("branches", "task"), to: p("lanes", "task_branch")},
	{from: p("lane_links"), to: p("lanes", "links")},
	{from: p("commands", "lane_setup"), to: p("lanes", "setup")},
	{from: p("commands", "lane_ready"), to: p("lanes", "ready")},
	{from: p("slots", "lane_cpus"), to: p("lanes", "cpus")},
	{from: p("slots", "lane_env"), to: p("lanes", "env")},
	{from: p("roles"), to: p("agents", "roles")},
	{from: p("difficulty"), to: p("agents", "difficulty")},
	{from: p("kinds"), to: p("agents", "kinds"), fix: eachMember(renameKeys(map[string]string{
		"prompt_extra": "job_note", "command": "plugin"}))},
	{from: p("models"), to: p("agents", "models"), fix: eachMember(renameKeys(map[string]string{"model": "id"}))},
	{from: p("handoff_tokens"), to: p("agents", "handoff_tokens")},
	{from: p("setup", "confirmed"), to: p("agents", "confirmed")},
	{from: p("paths", "agents"), to: p("agents", "docs_dir")},
	{from: p("guards", "allow_push"), to: p("rules", "may_push")},
	{from: p("protected_files"), to: p("rules", "protected_files")},
	{from: p("guards", "deny_commands"), to: p("rules", "deny_commands")},
	{from: p("branches", "main"), to: p("merge", "target_branch")},
	{from: p("workflow", "kind"), to: p("merge", "workflow")},
	{from: p("commands", "compile"), to: p("merge", "compile")},
	{from: p("commands", "check"), to: p("merge", "check")},
	{from: p("merge", "guard"), to: p("merge", "by_path", "guard")},
	{from: p("merge", "neutral"), to: p("merge", "by_path", "no_check")},
	{from: p("merge", "classes"), to: p("merge", "by_path", "rules"), fix: eachItem(renameKeys(map[string]string{
		"checks": "run", "with_full": "also_on_full"}))},
	{from: p("commands", "retest"), to: p("merge", "retest")},
	// a retest given as a string becomes {"cmd": …} for its join (commandObject)
	{from: p("merge", "retest_join"), to: p("merge", "retest", "join")},
	{from: p("commands", "after_merge"), to: p("merge", "after"), fix: renameKeys(map[string]string{"when": "when_changed"})},
	{from: p("merge", "nightly_at"), to: p("nightly", "at")},
	{from: p("commands", "check_milestone"), to: p("nightly", "slow_check")},
	{from: p("merge", "nightly_commands"), to: p("nightly", "also_run")},
	{from: p("slots", "max"), to: p("checks", "heavy_at_once")},
	{from: p("slots", "cpus"), to: p("checks", "cpus_per_check")},
	{from: p("slots", "dir"), to: p("checks", "slot_dir")},
	{from: p("merge", "summary_regex"), to: p("checks", "output", "summary")},
	{from: p("merge", "fail_grep"), to: p("checks", "output", "failure")},
	// merge.flake_regex, a pair, splits in two (flakePair)
	{from: p("merge", "flake_regex"), to: p("checks", "output", "failed_test")},
	{from: p("merge", "flake_regex"), to: p("checks", "output", "failed_package")},
	{from: p("paths", "reviews"), to: p("reviews", "dir")},
}

// shapeGone are shape 1's keys with no home in shape 2 (§3 "Gone without a new home"): Reshape drops them. The free-form
// paths map goes with them: its members other than agents and reviews.
var shapeGone = [][]string{
	p("done_file_sections"), p("limits", "max_panes_per_tab"), p("merge", "scratch_dir"), p("merge", "trailer"),
	p("merge", "affected_full"), p("paths"),
}

// shapeGroups are shape 2's top-level keys in their order (§2).
var shapeGroups = []string{"version", "project", "lanes", "agents", "rules", "merge", "nightly", "commands", "checks",
	"reviews"}

func p(keys ...string) []string { return keys }

// Reshape is doc, a config file of shape 1 or a part of one (local.json), in shape 2 (docs/research/config-shape.md
// §3): each key moved and renamed by the table, the keys with no home dropped, the groups in §2's order and version 2
// when doc names one below it (a local.json names none, and gets none). A member it does not know stays where it is (the load then names it). It works on a mixed
// file too, the test kit's (a shape-2 file with shape-1 paths set): a value already at a key's new home wins over the
// moved one, objects merging key by key. On a file of shape 2 it changes nothing.
func Reshape(doc []byte) ([]byte, error) {
	if _, ok := object(doc); !ok {
		return nil, fmt.Errorf("not a JSON object")
	}
	rest := json.RawMessage(doc)
	type landed struct {
		to  []string
		val json.RawMessage
	}
	var moved []landed
	take := func(from []string) (json.RawMessage, error) {
		v, err := MemberAt(rest, from)
		if err != nil || v == nil {
			return nil, err
		}
		rest, err = EditJSON(rest, from, nil)
		return v, err
	}
	for i := 0; i < len(shapeMoves); i++ {
		m := shapeMoves[i]
		switch {
		case len(m.from) == 1 && m.from[0] == "lanes":
			v, err := MemberAt(rest, m.from)
			if err != nil {
				return nil, err
			}
			if to := lanesOld(v); to != "" {
				if _, err := take(m.from); err != nil {
					return nil, err
				}
				if string(bytes.TrimSpace(v)) != "null" {
					moved = append(moved, landed{p("lanes", to), v})
				}
			}
			i++ // the list row: lanesOld picked the home
			continue
		case slices.Equal(m.from, p("merge", "flake_regex")):
			v, err := MemberAt(rest, m.from)
			if err != nil {
				return nil, err
			}
			i++ // the failed_package row
			var pair []json.RawMessage
			if v == nil || json.Unmarshal(v, &pair) != nil {
				continue // no pair: left in place, the load names it
			}
			if _, err := take(m.from); err != nil {
				return nil, err
			}
			for j, to := range []string{"failed_test", "failed_package"} {
				if j < len(pair) {
					moved = append(moved, landed{p("checks", "output", to), pair[j]})
				}
			}
			continue
		}
		v, err := take(m.from)
		if err != nil {
			return nil, err
		}
		if v == nil {
			continue
		}
		if m.fix != nil {
			v = m.fix(v)
		}
		if slices.Equal(m.from, p("merge", "retest_join")) {
			for j := range moved {
				if slices.Equal(moved[j].to, p("merge", "retest")) {
					moved[j].val = commandObject(moved[j].val)
				}
			}
		}
		moved = append(moved, landed{m.to, v})
	}
	for _, gone := range shapeGone {
		var err error
		if rest, err = EditJSON(rest, gone, nil); err != nil {
			return nil, err
		}
	}
	// the groups in order: each one's own members first (shape 2's, or ones the table does not know), then the moved
	// ones in table order; then any other top-level member as it was
	ms, _ := object(rest)
	var out []member
	for _, g := range shapeGroups {
		var val json.RawMessage
		for _, m := range ms {
			if m.Key == g {
				val = m.Val
			}
		}
		if g == "version" {
			if val != nil && ShapeOf(joinMembers([]member{{g, val}})) < ShapeVersion {
				val = json.RawMessage(fmt.Sprint(ShapeVersion))
			}
			if val != nil {
				out = append(out, member{g, val})
			}
			continue
		}
		for _, l := range moved {
			if l.to[0] != g {
				continue
			}
			cur, err := MemberAt(val, l.to[1:])
			if err != nil {
				return nil, err
			}
			v := l.val
			if cur != nil {
				v = merge(l.val, cur) // the value already at the new home wins
			}
			if val, err = EditJSON(val, l.to[1:], v); err != nil {
				return nil, err
			}
		}
		if val != nil {
			out = append(out, member{g, val})
		}
	}
	for _, m := range ms {
		if !slices.Contains(shapeGroups, m.Key) {
			out = append(out, m)
		}
	}
	var b bytes.Buffer
	if err := json.Compact(&b, joinMembers(out)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// lanesOld is the shape-2 home of a shape-1 lanes value: "count" for a number, "list" for a list, "count" for null
// (dropped); "" for anything else (an object: shape 2's lanes group).
func lanesOld(v json.RawMessage) string {
	t := bytes.TrimSpace(v)
	switch {
	case len(t) == 0 || t[0] == '{':
		return ""
	case t[0] == '[':
		return "list"
	}
	return "count"
}

// renameKeys renames v's members by names, each in its place; v unchanged when it is no object.
func renameKeys(names map[string]string) func(json.RawMessage) json.RawMessage {
	return func(v json.RawMessage) json.RawMessage {
		ms, ok := object(v)
		if !ok {
			return v
		}
		for i, m := range ms {
			if to, ok := names[m.Key]; ok {
				ms[i].Key = to
			}
		}
		return joinMembers(ms)
	}
}

// eachMember is fix applied to every member of the object v.
func eachMember(fix func(json.RawMessage) json.RawMessage) func(json.RawMessage) json.RawMessage {
	return func(v json.RawMessage) json.RawMessage {
		ms, ok := object(v)
		if !ok {
			return v
		}
		for i := range ms {
			ms[i].Val = fix(ms[i].Val)
		}
		return joinMembers(ms)
	}
}

// eachItem is fix applied to every item of the list v.
func eachItem(fix func(json.RawMessage) json.RawMessage) func(json.RawMessage) json.RawMessage {
	return func(v json.RawMessage) json.RawMessage {
		var items []json.RawMessage
		if json.Unmarshal(v, &items) != nil {
			return v
		}
		for i := range items {
			items[i] = fix(items[i])
		}
		b, err := json.Marshal(items)
		if err != nil {
			return v
		}
		return b
	}
}

// commandObject is a command given as a string as {"cmd": …}, so a member can be added to it (merge.retest's join);
// any other value as it is.
func commandObject(v json.RawMessage) json.RawMessage {
	var s string
	if json.Unmarshal(v, &s) != nil {
		return v
	}
	b, _ := json.Marshal(map[string]string{"cmd": s})
	return b
}

// ShapeOf is the shape of a config.json: its version when it is 2 or more, else 1 (a file with no version, or 1, is
// shape 1).
func ShapeOf(doc []byte) int {
	var v struct {
		Version *int `json:"version"`
	}
	if json.Unmarshal(doc, &v) != nil || v.Version == nil || *v.Version < ShapeVersion {
		return 1
	}
	return *v.Version
}

// ShapeDropped are the keys of doc, a file of shape 1, that Reshape drops (shapeGone, and the free-form paths map's
// members other than agents and reviews), as dotted paths in the table's order: what serve's rewrite commit names.
func ShapeDropped(doc []byte) []string {
	var out []string
	for _, path := range shapeGone {
		v, _ := MemberAt(doc, path)
		if v == nil {
			continue
		}
		if !slices.Equal(path, p("paths")) {
			out = append(out, strings.Join(path, "."))
			continue
		}
		ms, _ := object(v)
		for _, m := range ms {
			if m.Key != "agents" && m.Key != "reviews" {
				out = append(out, "paths."+m.Key)
			}
		}
	}
	return out
}
