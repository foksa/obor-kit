// The page routes by the URL's hash (links into obor web keep working): #/ the fleet,
// #/p/<project>, #/s/<project> its setup, #/tasks/<project> its tasks, #/t/<project>/<task>, #/a/<project>/<agent>, #/j/<project>/<job id>.
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
