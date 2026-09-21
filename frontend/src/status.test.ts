import { describe, expect, it } from "vitest";
import { STATE_LABELS, actionForState, hidesPopoverForState } from "./status";

describe("status", () => {
  it("labels every state the Go side can report", () => {
    for (const s of ["not-configured", "auth-required", "locked", "stopped", "starting", "syncing", "idle", "paused", "error"]) {
      expect(STATE_LABELS[s]).toBeTruthy();
    }
  });

  it("offers a way back only when not syncing", () => {
    expect(actionForState("not-configured")).toBe("Set Up Brick");
    expect(actionForState("auth-required")).toBe("Log In Again");
    expect(actionForState("locked")).toBe("Start Syncing");
    expect(actionForState("stopped")).toBe("Start Syncing");
    for (const s of ["starting", "syncing", "idle", "paused", "error"]) {
      expect(actionForState(s)).toBeNull();
    }
  });

  it("closes the popover behind Set Up Brick only", () => {
    expect(hidesPopoverForState("not-configured")).toBe(true);
    for (const s of ["auth-required", "locked", "stopped", "idle"]) {
      expect(hidesPopoverForState(s)).toBe(false);
    }
  });
});
