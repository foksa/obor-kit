// The Help page's data (/api/help): every obor command by group, and README's setup sections; filterHelp is the
// filter box.
import type { Help, HelpCommand } from "./types";

export interface HelpGroup {
  group: string;
  commands: HelpCommand[];
}

// filterHelp is the commands by group (help.groups' order), only those whose name, usage or description contains every
// word of q (case ignored); a group left with none is left out.
export function filterHelp(help: Help, q: string): HelpGroup[] {
  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  const hit = (c: HelpCommand) => {
    const text = `${c.name} ${c.usage} ${c.desc}`.toLowerCase();
    return words.every((w) => text.includes(w));
  };
  return help.groups
    .map((group) => ({ group, commands: help.commands.filter((c) => c.group === group && hit(c)) }))
    .filter((g) => g.commands.length > 0);
}
