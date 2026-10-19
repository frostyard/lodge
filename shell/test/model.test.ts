import { describe, expect, it } from "vitest";
import {
  canonical,
  filterApps,
  firstApp,
  frameURL,
  parsePlace,
  parseRail,
  type RailApp,
  titleFor,
} from "../src/model.js";

const app = (id: string, name: string, extra: Partial<RailApp> = {}): RailApp => ({
  id,
  name,
  icon: `/app/${id}/icon.svg`,
  key: "",
  content: false,
  companion: false,
  state: "up",
  reason: "",
  ...extra,
});

describe("addresses", () => {
  it("reads the address bar", () => {
    expect(parsePlace("/")).toBeNull();
    expect(parsePlace("/notes")).toEqual({ app: "notes", route: "/" });
    expect(parsePlace("/notes/", "", "")).toEqual({ app: "notes", route: "/" });
    expect(parsePlace("/notes/1/Projects/Plan.md", "?q=1", "#h")).toEqual({
      app: "notes",
      route: "/1/Projects/Plan.md?q=1#h",
    });
    expect(parsePlace("/notes", "?q=1", "")).toEqual({ app: "notes", route: "/?q=1" });
    for (const p of ["/app/notes", "/lodge/x", "/mcp/notes", "/device/notes", "/Notes", "/a-b"]) {
      expect(parsePlace(p)).toBeNull();
    }
  });

  it("writes canonical and frame URLs", () => {
    expect(canonical("notes", "/")).toBe("/notes");
    expect(canonical("notes", "/1/Plan.md?x=1#y")).toBe("/notes/1/Plan.md?x=1#y");
    expect(frameURL("notes", "/")).toBe("/app/notes/");
    expect(frameURL("notes", "/1/Plan.md")).toBe("/app/notes/1/Plan.md");
  });

  it("titles the page", () => {
    expect(titleFor("Plan", "Notes")).toBe("Plan — Notes");
    expect(titleFor("", "Notes")).toBe("Notes");
  });
});

describe("the rail's data", () => {
  it("keeps what it can use", () => {
    const r = parseRail({
      contract: 1,
      contentOrigin: "https://content.test",
      apps: [
        { id: "notes", name: "Notes", icon: "/app/notes/icon.svg", key: "n", content: true, state: "up" },
        { id: "evil", name: "Evil", icon: "https://evil.test/x.svg", key: "NN", state: "weird" },
        { id: "app", name: "Reserved" },
        { id: "notes", name: "Duplicate" },
        "junk",
      ],
    });
    expect(r?.apps.map((a) => a.id)).toEqual(["notes", "evil"]);
    expect(r?.apps[1]).toMatchObject({ icon: "", key: "", state: "down" });
    expect(parseRail(null)).toBeNull();
    expect(parseRail({ apps: [] })).toBeNull();
  });
});

describe("the switcher's filter", () => {
  const apps = [app("mail", "Mail"), app("notes", "Notes"), app("assist", "Assistant"), app("tasks", "Tasks")];
  it("puts names that start with the query first", () => {
    expect(filterApps(apps, "").map((a) => a.id)).toEqual(["mail", "notes", "assist", "tasks"]);
    expect(filterApps(apps, "a").map((a) => a.id)).toEqual(["assist", "mail", "tasks"]);
    expect(filterApps(apps, "NOT").map((a) => a.id)).toEqual(["notes"]);
    expect(filterApps(apps, "zzz")).toEqual([]);
  });

  it("opens the last app shown, or the first enabled one", () => {
    const list = [app("a", "A", { state: "disabled" }), app("b", "B"), app("c", "C")];
    expect(firstApp(list, "c")?.id).toBe("c");
    expect(firstApp(list, "a")?.id).toBe("b");
    expect(firstApp(list, null)?.id).toBe("b");
    expect(firstApp([], null)).toBeUndefined();
  });
});
