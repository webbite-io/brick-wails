import { describe, expect, it } from "vitest";
import {
  CONFLICT_OPTIONS,
  GENERIC_ERROR,
  describeError,
  presentable,
  displayPath,
  folderOptions,
  needsWindow,
  progressDots,
  remoteRootOptions,
  scopeOptions,
  screenForRoute,
} from "./wizard";

describe("screenForRoute", () => {
  it("greets a first run and drops the greeting for returning users", () => {
    expect(screenForRoute({ step: "welcome", firstRun: true }).title).toBe("Welcome to Brick");
    expect(screenForRoute({ step: "welcome", firstRun: false }).title).toBe("Log in to Brick");
  });

  it("leaves the welcome screen to the brand block", () => {
    expect(screenForRoute({ step: "welcome", firstRun: true }).message).toBe("");
  });

  it("surfaces backend messages and details for errors", () => {
    const s = screenForRoute({ step: "connect-error", message: "Could not reach", detail: "dial tcp" });
    expect(s).toMatchObject({ icon: "error", message: "Could not reach", detail: "dial tcp" });
    expect(screenForRoute({ step: "locked", message: "CLI" }).icon).toBe("warn");
    expect(screenForRoute({ step: "login", message: "expired" }).message).toBe("expired");
  });

  it("covers every interactive step", () => {
    for (const step of ["welcome", "login", "account", "folder", "locked", "connect-error"]) {
      expect(needsWindow(step)).toBe(true);
      expect(screenForRoute({ step }).title).not.toBe("");
    }
    expect(needsWindow("ready")).toBe(false);
  });
});

describe("progressDots", () => {
  it("fills the steps left behind and empties the ones still ahead", () => {
    expect(progressDots("folder")).toEqual([false, false, false, false, false]);
    expect(progressDots("conflict")).toEqual([true, false, false, false, false]);
    expect(progressDots("remote")).toEqual([true, true, true, false, false]);
  });

  it("counts skipped steps as done so the bar never stalls", () => {
    // Nothing to resolve and nothing to scope: straight from folder to remote.
    expect(progressDots("remote").filter(Boolean)).toHaveLength(3);
  });

  it("fills every dot on the last screen and shows none outside the wizard", () => {
    expect(progressDots("done")).toEqual([true, true, true, true, true]);
    expect(progressDots(null)).toEqual([]);
  });
});

describe("displayPath", () => {
  it("renders paths under home with ~ like brick-cli", () => {
    expect(displayPath("/home/ada/Brick", "/home/ada")).toBe("~/Brick");
    expect(displayPath("/home/ada", "/home/ada/")).toBe("~");
    expect(displayPath("/home/adam/x", "/home/ada")).toBe("/home/adam/x");
    expect(displayPath("C:\\Users\\ada\\Brick", "C:\\Users\\ada")).toBe("~\\Brick");
    expect(displayPath("/x", "")).toBe("/x");
  });
});

describe("options", () => {
  it("offers the three CLI sync-folder choices", () => {
    const opts = folderOptions("/home/ada/Brick", "/home/ada");
    expect(opts.map((o) => o.value)).toEqual(["default", "pick", "create"]);
    expect(opts[0].label).toBe("Use ~/Brick");
    expect(opts[1].label).toBe("Pick existing folder in ~");
  });

  it("uses the CLI conflict modes", () => {
    expect(CONFLICT_OPTIONS.map((o) => o.value)).toEqual(["device", "brick", "copy"]);
  });

  it("shows the total size in the scope choice", () => {
    expect(scopeOptions("12.3 GB")[0].label).toBe("Sync all folders from Brick (12.3 GB)");
  });

  it("shows the custom remote root once picked", () => {
    expect(remoteRootOptions("/home/ada")[1].hint).toBeUndefined();
    expect(remoteRootOptions("/home/ada", "/srv")[1].hint).toBe("/srv");
  });
});

describe("describeError", () => {
  it("handles Error, Wails error objects and strings", () => {
    expect(describeError(new Error("boom"))).toBe("boom");
    expect(describeError({ message: "from go" })).toBe("from go");
    expect(describeError("plain")).toBe("plain");
  });

  it("never surfaces a raw payload", () => {
    expect(describeError(new Error('{"kind":"ReferenceError","message":"x"}'))).toBe(GENERIC_ERROR);
    expect(describeError({})).toBe(GENERIC_ERROR);
    expect(describeError("[1,2]")).toBe(GENERIC_ERROR);
    expect(describeError(new Error(" "))).toBe(GENERIC_ERROR);
  });
});

describe("presentable", () => {
  it("passes sentences through and drops payloads", () => {
    expect(presentable("Could not reach Brick")).toBe("Could not reach Brick");
    expect(presentable('{"code":401}')).toBe("");
    expect(presentable(undefined)).toBe("");
    expect(presentable("{oops}", "fallback")).toBe("fallback");
  });
});
