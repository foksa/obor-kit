// The idle line (T-803): serve says when the pipeline empties, the lead acknowledges with obor idle "<why>". The page
// words the state exactly as obor status does: "idle, not acknowledged since <time>", or "idle: <why>".
import type { Idle } from "./types";

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

// idleWhen is unix (epoch seconds) as obor status prints it: local "Jan 2 15:04" (Go's layout, no padded day).
export function idleWhen(unix: number): string {
  const d = new Date(unix * 1000);
  const day = String(d.getDate());
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  return `${MONTHS[d.getMonth()]} ${day} ${hh}:${mm}`;
}

// idleText is the idle banner's line: the lead's acknowledgement, or the unacknowledged state and since when.
export function idleText(i: Idle): string {
  if (i.acked) {
    return "idle: " + (i.why || "acknowledged");
  }
  return "idle, not acknowledged since " + idleWhen(i.since);
}
