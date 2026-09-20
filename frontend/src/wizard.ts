// Pure view-model for the setup window: which text, options and buttons each
// step shows. No DOM or Wails imports, so it can be unit tested
// (wizard.test.ts). The copy mirrors brick-cli's interactive onboarding.

export type Icon = "spinner" | "ok" | "error" | "warn";

export interface Screen {
  icon: Icon;
  title: string;
  message: string;
  detail?: string;
}

export interface Option {
  value: string;
  label: string;
  hint?: string;
}

export interface RouteLike {
  step: string;
  firstRun?: boolean;
  message?: string;
  detail?: string;
}

// Route steps that need the user (everything but "ready").
export const INTERACTIVE_STEPS = new Set(["welcome", "login", "account", "folder", "locked", "connect-error"]);

export function needsWindow(step: string): boolean {
  return INTERACTIVE_STEPS.has(step);
}

// screenForRoute describes the header for a route step.
export function screenForRoute(r: RouteLike): Screen {
  switch (r.step) {
    // The welcome screen shows the brand and a "Log in" button, so it needs no
    // status copy of its own (see .welcome in startup.css).
    case "welcome":
      return { icon: "ok", title: r.firstRun ? "Welcome to Brick" : "Log in to Brick", message: "" };
    case "login":
      return { icon: "warn", title: "Authentication failed", message: r.message || "Log in again to continue.", detail: r.detail };
    case "account":
      return {
        icon: "ok",
        title: "Choose an account",
        message: "You have access to more than one account, which one do you want to use?",
      };
    case "folder":
      return { icon: "ok", title: "Sync folder", message: "You have no sync folder configured. Choose a sync folder." };
    case "locked":
      return { icon: "warn", title: "Brick CLI is running", message: r.message || "", detail: r.detail };
    case "connect-error":
      return { icon: "error", title: "Can't reach Brick", message: r.message || "Something went wrong.", detail: r.detail };
    default:
      return { icon: "spinner", title: "Starting Brick…", message: "" };
  }
}

// displayPath renders path relative to home as ~/…, like brick-cli.
export function displayPath(path: string, home: string): string {
  if (!home) return path;
  const h = home.replace(/[\\/]+$/, "");
  if (path === h) return "~";
  for (const sep of ["/", "\\"]) {
    if (path.startsWith(h + sep)) return "~" + sep + path.slice(h.length + 1);
  }
  return path;
}

export function folderOptions(defaultFolder: string, home: string): Option[] {
  return [
    { value: "default", label: `Use ${displayPath(defaultFolder, home)}` },
    { value: "pick", label: `Pick existing folder in ${displayPath(home, home)}` },
    { value: "create", label: "Create folder" },
  ];
}

export const CONFLICT_OPTIONS: Option[] = [
  { value: "device", label: "Overwrite any duplicate files on this device." },
  { value: "brick", label: "Overwrite any duplicate files on Brick." },
  { value: "copy", label: "Make a copy of any duplicate files (so nothing is lost)." },
];

export function scopeOptions(totalHuman: string): Option[] {
  return [
    { value: "all", label: `Sync all folders from Brick (${totalHuman})` },
    { value: "pick", label: "Pick which folders to sync" },
  ];
}

export const REMOTE_OPTIONS: Option[] = [
  { value: "yes", label: "Yes" },
  { value: "no", label: "No" },
];

export function remoteRootOptions(home: string, custom?: string): Option[] {
  return [
    { value: "home", label: "Home folder", hint: home },
    { value: "custom", label: "Custom folder…", hint: custom },
  ];
}

// describeError turns a rejected binding call into a message.
export function describeError(err: unknown): string {
  if (err instanceof Error) return err.message;
  if (err && typeof err === "object" && "message" in err) return String((err as { message: unknown }).message);
  return String(err);
}
