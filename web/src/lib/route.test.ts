import { describe, expect, it } from "vitest";
import { href, pageKey, parseRoute } from "./route";

describe("routes", () => {
  it("parses every page's hash, a job's id keeping its slashes", () => {
    expect(parseRoute("")).toEqual({ kind: "", project: "", id: "" });
    expect(parseRoute("#/p/axis-alies")).toEqual({ kind: "p", project: "axis-alies", id: "" });
    expect(parseRoute("#/s/obor-kit")).toEqual({ kind: "s", project: "obor-kit", id: "" });
    expect(parseRoute("#/t/pj/T-12")).toEqual({ kind: "t", project: "pj", id: "T-12" });
    expect(parseRoute("#/a/pj/pj-o1")).toEqual({ kind: "a", project: "pj", id: "pj-o1" });
    expect(parseRoute("#/j/pj/T-12/1/work")).toEqual({ kind: "j", project: "pj", id: "T-12/1/work" });
    expect(parseRoute("#/j/pj/T-12%2F1%2Fwork")).toEqual({ kind: "j", project: "pj", id: "T-12/1/work" });
  });
  it("sends an unknown kind, a page with no project or a task with no id to the fleet", () => {
    for (const h of ["#/x/pj", "#/p", "#/t/pj", "#/a/pj/", "#/%E0%A4%A"]) expect(parseRoute(h).kind).toBe("");
  });
  it("parses help, the one page of no project (T-854)", () => {
    for (const h of ["#/h", "#/h/", "#h"]) expect(parseRoute(h)).toEqual({ kind: "h", project: "", id: "" });
    expect(parseRoute(href.help())).toEqual({ kind: "h", project: "", id: "" });
    for (const h of ["#/h/pj", "#/h/pj/x"]) expect(parseRoute(h).kind).toBe("");
  });
  it("parses a project's tasks page, tasks/<project>, and links to it", () => {
    expect(parseRoute("#/tasks/obor-kit")).toEqual({ kind: "tasks", project: "obor-kit", id: "" });
    expect(parseRoute("#/tasks/a%20b")).toEqual({ kind: "tasks", project: "a b", id: "" });
    expect(parseRoute(href.tasks("a b"))).toEqual({ kind: "tasks", project: "a b", id: "" });
    for (const h of ["#/tasks", "#/tasks/", "#/task/pj"]) expect(parseRoute(h).kind).toBe("");
  });
  it("parses a project's docs page, docs/<project>[/<path>], and links to it with the path's slashes kept", () => {
    expect(parseRoute("#/docs/pj")).toEqual({ kind: "docs", project: "pj", id: "" });
    expect(parseRoute("#/docs/pj/docs/plan/a.md")).toEqual({ kind: "docs", project: "pj", id: "docs/plan/a.md" });
    expect(href.docs("a b", "docs/x y.md")).toBe("#/docs/a%20b/docs/x%20y.md");
    expect(parseRoute(href.docs("a b", "docs/x y.md"))).toEqual({ kind: "docs", project: "a b", id: "docs/x y.md" });
    expect(parseRoute(href.docs("pj"))).toEqual({ kind: "docs", project: "pj", id: "" });
    for (const h of ["#/docs", "#/docs/"]) expect(parseRoute(h).kind).toBe("");
  });
  it("links every kind the router handles, and back", () => {
    const links = [href.project("a b"), href.setup("pj"), href.task("pj", "T-1"), href.agent("pj", "x"), href.job("pj", "T-1/1/work")];
    expect(links.map((l) => parseRoute(l).kind)).toEqual(["p", "s", "t", "a", "j"]);
    expect(parseRoute(href.project("a b")).project).toBe("a b");
    expect(parseRoute(href.job("pj", "T-1/1/work")).id).toBe("T-1/1/work");
  });
});

describe("the routed page's key (T-1306)", () => {
  it("keeps the docs page mounted from doc to doc and on a navigation, remounting it for another project", () => {
    const a = pageKey(parseRoute(href.docs("pj", "docs/a.md")), 1);
    expect(a).toBe(pageKey(parseRoute(href.docs("pj", "docs/b.md")), 2));
    expect(a).toBe(pageKey(parseRoute(href.docs("pj")), 3));
    expect(a).not.toBe(pageKey(parseRoute(href.docs("other", "docs/a.md")), 1));
  });
  it("remounts any other page on another route or a navigation to it", () => {
    const t = parseRoute(href.task("pj", "T-1"));
    expect(pageKey(t, 1)).not.toBe(pageKey(t, 2));
    expect(pageKey(t, 1)).not.toBe(pageKey(parseRoute(href.task("pj", "T-2")), 1));
  });
});
