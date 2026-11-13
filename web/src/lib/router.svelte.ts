// The page routes by the URL's hash (links into obor web keep working): #/ the fleet,
// #/p/<project>, #/s/<project> its setup, #/tasks/<project> its tasks, #/docs/<project>[/<path>[#<heading id>]] its docs,
// #/t/<project>/<task>, #/a/<project>/<agent>, #/j/<project>/<job id>.
import { parseRoute, type Route } from "./route";

export const router = $state<{ route: Route; n: number }>({ route: parseRoute(location.hash), n: 0 });

window.addEventListener("hashchange", () => {
  router.route = parseRoute(location.hash);
  router.n++;
  window.scrollTo(0, 0);
});

export function go(hash: string) {
  if (location.hash === hash) router.n++; // the page shown: read it again
  else location.hash = hash;
}

// replace puts hash in the URL in place of the shown one, the page as it is: no history entry, no remount, no scroll.
// A section a reader picks (a heading, a text hit) reads that way, so Back leaves the doc instead of walking its
// headings; the route follows, so the page sees the section it now has.
export function replace(hash: string) {
  if (location.hash === hash) return;
  history.replaceState(history.state, "", hash);
  router.route = parseRoute(hash);
}
