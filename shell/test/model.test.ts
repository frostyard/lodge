import { describe, expect, it } from "vitest";
import {
  badgeText,
  badgeTotal,
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
  badge: 0,
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
    expect(r?.apps[1]).toMatchObject({ icon: "", key: "", state: "down", badge: 0 });
    expect(parseRail(null)).toBeNull();
    expect(parseRail({ apps: [] })).toBeNull();
  });

  it("reads a count only from an app that is up", () => {
    const r = parseRail({
      contract: 1,
      contentOrigin: "",
      apps: [
        { id: "mail", state: "up", badge: 3 },
        { id: "notes", state: "down", badge: 4 },
        { id: "tasks", state: "up", badge: "3" },
        { id: "read", state: "up", badge: 2.5 },
        { id: "chat", state: "up", badge: -1 },
        { id: "wiki", state: "up", badge: 100000 },
        { id: "cal", state: "up" },
        { id: "maps", state: "up", badge: null },
      ],
    });
    expect(r?.apps.map((a) => a.badge)).toEqual([3, 0, 0, 0, 0, 99999, 0, 0]);
  });
});

describe("badges", () => {
  it("writes a count in full to 99, and 99+ above", () => {
    expect(badgeText(0)).toBe("");
    expect(badgeText(-1)).toBe("");
    expect(badgeText(1)).toBe("1");
    expect(badgeText(3)).toBe("3");
    expect(badgeText(99)).toBe("99");
    expect(badgeText(100)).toBe("99+");
    expect(badgeText(120)).toBe("99+");
    expect(badgeText(99999)).toBe("99+");
  });

  it("adds up the counts, without the app whose id it is given", () => {
    const apps = [
      app("mail", "Mail", { badge: 1 }),
      app("notes", "Notes", { badge: 2 }),
      app("read", "Read", { badge: 3 }),
    ];
    expect(badgeTotal(apps)).toBe(6);
    expect(badgeTotal(apps, null)).toBe(6);
    expect(badgeTotal(apps, "notes")).toBe(4);
    expect(badgeTotal(apps, "nosuchapp")).toBe(6);
    expect(badgeTotal([])).toBe(0);
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
