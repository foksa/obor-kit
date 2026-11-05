import { describe, expect, it } from "vitest";
import { href, parseRoute } from "./route";

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
  it("links every kind the router handles, and back", () => {
    const links = [href.project("a b"), href.setup("pj"), href.task("pj", "T-1"), href.agent("pj", "x"), href.job("pj", "T-1/1/work")];
    expect(links.map((l) => parseRoute(l).kind)).toEqual(["p", "s", "t", "a", "j"]);
    expect(parseRoute(href.project("a b")).project).toBe("a b");
    expect(parseRoute(href.job("pj", "T-1/1/work")).id).toBe("T-1/1/work");
  });
});
