import { describe, expect, it } from "vitest";
import { render } from "svelte/server";
import { filterHelp } from "./help";
import type { Help } from "./types";
import HelpView from "../ui/HelpView.svelte";

const help: Help = {
  groups: ["tasks", "reviews", "agents", "internals"],
  commands: [
    { name: "dispatch", usage: "obor dispatch <T-xx> [--after <T-yy>]", desc: "run a task on a free lane", group: "tasks" },
    { name: "drop", usage: 'obor drop <T-xx> "<why>"', desc: "free its lane", group: "tasks" },
    { name: "review", usage: "obor review [--after <T-a>]", desc: "request a code review", group: "reviews" },
    { name: "hook", usage: "obor hook noop", desc: "the agents' hooks", group: "internals" },
  ],
  sections: [
    { title: "Install", body: "```sh\ncurl -fsSL https://x/install.sh | sh\n```\n\nThis clones the kit." },
    { title: "Set up a project", body: "Run `obor init` once." },
  ],
};

// groupsOf is the rendered page's groups (data-group), in order.
const groupsOf = (html: string) => [...html.matchAll(/data-group="([^"]+)"/g)].map((m) => m[1]);

describe("help (T-854)", () => {
  it("groups the commands in the table's group order, leaving out a group with none", () => {
    const g = filterHelp(help, "");
    expect(g.map((x) => x.group)).toEqual(["tasks", "reviews", "internals"]);
    expect(g[0].commands.map((c) => c.name)).toEqual(["dispatch", "drop"]);
  });
  it("filters on name, usage and description, every word, case ignored", () => {
    expect(filterHelp(help, "--AFTER").flatMap((g) => g.commands.map((c) => c.name))).toEqual(["dispatch", "review"]);
    expect(filterHelp(help, "lane WHY").flatMap((g) => g.commands.map((c) => c.name))).toEqual(["drop"]);
    expect(filterHelp(help, "hook").map((g) => g.group)).toEqual(["internals"]);
    expect(filterHelp(help, "no-such-thing")).toEqual([]);
  });
  it("renders the setup sections with md, then every group and command", () => {
    const { body } = render(HelpView, { props: { help } });
    expect(body).toContain("Install");
    expect(body).toContain("Set up a project");
    expect(body).toContain("<pre><code>curl -fsSL https://x/install.sh | sh</code></pre>");
    expect(body).toContain("<code>obor init</code>");
    expect(groupsOf(body)).toEqual(["tasks", "reviews", "internals"]);
    for (const c of help.commands) expect(body).toContain(c.desc);
    expect(body).toContain("obor dispatch &lt;T-xx> [--after &lt;T-yy>]");
  });
  it("renders only the matching commands under a filter, or says none matches", () => {
    const { body } = render(HelpView, { props: { help, q: "review" } });
    expect(groupsOf(body)).toEqual(["reviews"]);
    expect(body).toContain("request a code review");
    expect(body).not.toContain("run a task on a free lane");
    expect(body).toContain("1 of 4");
    const none = render(HelpView, { props: { help, q: "zzz" } }).body;
    expect(groupsOf(none)).toEqual([]);
    expect(none).toContain("No command matches");
  });
});
