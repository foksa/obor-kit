// The build obor web serves: the id of the page this tab loaded with, and whether a newer one answers. A newer build's
// page is not what this tab runs, so the nav offers a reload instead: the tab never reloads itself, which would throw
// away Setup's staged edits, a pause form, a filter or a jump half typed.

// buildHeader is the header every /api/ response carries: the id of the page obor web serves (a hash of its index.html).
const buildHeader = "X-Obor-Build";

// loaded is the build this tab loaded with: the first response's header. Nothing names a build, the tab keeps its own.
let loaded: string | null = null;

// build is the nav's flag: newer is true once a response names another build than the one this tab loaded.
export const build = $state<{ newer: boolean }>({ newer: false });

// followBuild is every response's check: the first one tells the tab which build it loaded, a later one from another
// build marks this tab as behind. A response without the header is an obor web from before it: nothing is newer.
export function followBuild(r: Response): void {
  const id = r.headers.get(buildHeader);
  if (id === null) return;
  if (loaded === null) loaded = id;
  else if (id !== loaded) build.newer = true;
}

// reload loads the page again, so the newer build's scripts run (the hash route survives the reload).
export function reload(): void {
  location.reload();
}
