import { Events, Window } from "@wailsio/runtime";
import { BrickService, StartupService } from "../bindings/github.com/webbite-io/brick-wails";
import type { BrickSelfTestCheck } from "../bindings/github.com/webbite-io/brick-wails";

const spinnerEl = document.getElementById("stage-spinner")! as HTMLSpanElement;
const iconEl = document.getElementById("stage-icon")! as HTMLSpanElement;
const titleEl = document.getElementById("stage-title")! as HTMLHeadingElement;
const messageEl = document.getElementById("stage-message")! as HTMLParagraphElement;
const detailEl = document.getElementById("stage-detail")! as HTMLParagraphElement;
const checksListEl = document.getElementById("checks-list")! as HTMLUListElement;
const terminalEl = document.getElementById("terminal")! as HTMLElement;
const terminalOutputEl = document.getElementById("terminal-output")! as HTMLPreElement;
const primaryBtn = document.getElementById("primary-btn")! as HTMLButtonElement;
const secondaryBtn = document.getElementById("secondary-btn")! as HTMLButtonElement;

type Icon = "spinner" | "ok" | "error" | "warn";

interface Action {
  label: string;
  onClick: () => void;
  cta?: boolean;
  variant?: "primary" | "quiet";
}

// Renders the header (spinner/icon + title) and the primary/secondary
// message lines. Every stage transition goes through this, so the panel
// never ends up with stale text from a previous stage.
function render(opts: { icon: Icon; title: string; message: string; detail?: string }) {
  spinnerEl.classList.toggle("hidden", opts.icon !== "spinner");
  iconEl.classList.toggle("visible", opts.icon !== "spinner");
  iconEl.classList.remove("icon-ok", "icon-error", "icon-warn");
  iconEl.textContent = "";
  if (opts.icon === "ok") {
    iconEl.classList.add("icon-ok");
    iconEl.textContent = "✓";
  } else if (opts.icon === "error") {
    iconEl.classList.add("icon-error");
    iconEl.textContent = "!";
  } else if (opts.icon === "warn") {
    iconEl.classList.add("icon-warn");
    iconEl.textContent = "!";
  }

  titleEl.textContent = opts.title;
  messageEl.textContent = opts.message;

  if (opts.detail) {
    detailEl.textContent = opts.detail;
    detailEl.classList.add("visible");
  } else {
    detailEl.textContent = "";
    detailEl.classList.remove("visible");
  }
}

function showChecks(checks: BrickSelfTestCheck[] | null | undefined) {
  checksListEl.innerHTML = "";
  if (!checks || checks.length === 0) {
    checksListEl.classList.remove("visible");
    return;
  }
  for (const check of checks) {
    const li = document.createElement("li");
    const dot = document.createElement("span");
    dot.className = "dot " + (check.status === "ok" ? "status-ok" : "status-error");
    li.appendChild(dot);
    const label = document.createElement("span");
    label.textContent = `${check.id}: ${check.message}`;
    li.appendChild(label);
    checksListEl.appendChild(li);
  }
  checksListEl.classList.add("visible");
}

function clearTerminal() {
  terminalOutputEl.textContent = "";
}

function showTerminal(visible: boolean) {
  terminalEl.classList.toggle("visible", visible);
}

function appendTerminalLine(line: string) {
  terminalOutputEl.textContent += (terminalOutputEl.textContent ? "\n" : "") + line;
  terminalOutputEl.scrollTop = terminalOutputEl.scrollHeight;
}

function applyButtonAction(btn: HTMLButtonElement, action: Action | undefined, defaultVariant: "primary" | "quiet") {
  if (!action) {
    btn.onclick = null;
    btn.classList.remove("visible", "btn-cta", "btn-primary", "btn-quiet");
    return;
  }
  btn.textContent = action.label;
  btn.onclick = action.onClick;
  const variant = action.variant ?? defaultVariant;
  btn.classList.toggle("btn-primary", variant === "primary");
  btn.classList.toggle("btn-quiet", variant === "quiet");
  btn.classList.toggle("btn-cta", !!action.cta);
  btn.classList.add("visible");
}

function setActions(primary?: Action, secondary?: Action) {
  applyButtonAction(primaryBtn, primary, "primary");
  applyButtonAction(secondaryBtn, secondary, "quiet");
}

function describeError(err: unknown): string {
  if (err instanceof Error) return err.message;
  return String(err);
}

async function closeWindow() {
  try {
    await Window.Close();
  } catch (err) {
    console.error("failed to close startup window", err);
  }
}

// Step 1: is brick already running? Reuses BrickService (the same call the
// popover polls) rather than duplicating the control-API probe here.
async function checkRunning() {
  render({ icon: "spinner", title: "Checking Brick…", message: "Looking for a running Brick process." });
  setActions();
  showTerminal(false);
  showChecks(null);

  let running = false;
  try {
    const status = await BrickService.Status();
    running = status.running;
  } catch (err) {
    // BrickService.Status() treats "not running" as a normal, non-error
    // result — a thrown error here means something else went wrong
    // talking to the control API. Either way, self-test is the next
    // useful step: it'll surface a clearer reason.
    console.error("BrickService.Status() failed", err);
  }

  if (running) {
    render({ icon: "ok", title: "Brick is running", message: "" });
    await closeWindow();
    return;
  }

  await locateAndTest();
}

// Step 2: find the brick binary; if it's missing, offer to install it.
async function locateAndTest() {
  render({ icon: "spinner", title: "Checking Brick…", message: "Looking for the Brick CLI…" });
  setActions();

  let found = false;
  try {
    const result = await StartupService.LocateBrick();
    found = result.found;
  } catch (err) {
    render({
      icon: "error",
      title: "Brick setup",
      message: "Could not check for the Brick CLI.",
      detail: describeError(err),
    });
    setActions({ label: "Retry", onClick: () => void locateAndTest() });
    return;
  }

  if (!found) {
    promptInstall();
    return;
  }

  await runSelfTest();
}

function promptInstall() {
  render({
    icon: "warn",
    title: "Brick CLI",
    message: "The Brick CLI is required to sync your files but it's not installed. Would you like to install it now?",
  });
  setActions(
    { label: "Install Brick CLI", onClick: () => void doInstall(), cta: true },
    {
      label: "Close Brick",
      onClick: () => void StartupService.QuitApp(),
      cta: true,
      variant: "primary",
    },
  );
}

async function doInstall() {
  render({ icon: "spinner", title: "Installing Brick…", message: "Running the Brick installer…" });
  setActions();
  clearTerminal();
  showTerminal(true);

  const offInstall = Events.On("brick:install-output", (e) => appendTerminalLine(String(e.data)));
  try {
    const result = await StartupService.InstallBrick();
    if (result.ok) {
      await locateAndTest();
      return;
    }
    render({
      icon: "error",
      title: "Install failed",
      message: `The installer exited with code ${result.exitCode}. See the output above for details.`,
    });
    setActions(
      { label: "Try again", onClick: () => void doInstall() },
      {
        label: "Cancel",
        onClick: () => {
          render({
            icon: "error",
            title: "Brick setup",
            message: "Brick needs to be installed to continue.",
          });
          setActions({ label: "Check again", onClick: () => void locateAndTest() });
        },
      },
    );
  } catch (err) {
    render({ icon: "error", title: "Install failed", message: describeError(err) });
    setActions({ label: "Try again", onClick: () => void doInstall() });
  } finally {
    offInstall();
  }
}

// Step 3: run brick's self-test and branch on the result.
async function runSelfTest() {
  render({ icon: "spinner", title: "Checking Brick…", message: "Running Brick self-test…" });
  setActions();
  showTerminal(false);
  showChecks(null);

  let result;
  try {
    result = await StartupService.SelfTest();
  } catch (err) {
    render({
      icon: "error",
      title: "Self-test failed",
      message: "Could not run the Brick self-test.",
      detail: describeError(err),
    });
    setActions({ label: "Retry", onClick: () => void runSelfTest() });
    return;
  }

  const checks = result.checks ?? [];
  showChecks(checks);

  const instanceLock = checks.find((c) => c.id === "instance_lock");
  if (instanceLock && instanceLock.status !== "ok") {
    render({
      icon: "error",
      title: "Brick setup",
      message: "Another instance of Brick is running with the IPC API turned off.",
      detail: instanceLock.message,
    });
    setActions({ label: "Retry", onClick: () => void runSelfTest() });
    return;
  }

  const failing = checks.find((c) => c.id !== "instance_lock" && c.status !== "ok");
  if (failing) {
    await runSetup(failing.message);
    return;
  }

  await launchBrick();
}

// Step 4a: everything checked out except brick just wasn't running — start
// it via `brick -d --json`, which reports back a single JSON status line
// once the handoff to the detached daemon either succeeds or fails (see
// StartupService.StartBrick / startup.go) rather than leaving us to assume
// success just because the OS could exec the binary.
async function launchBrick() {
  render({ icon: "spinner", title: "Starting Brick…", message: "Starting the Brick sync process…" });
  setActions();
  showTerminal(false);

  let result;
  try {
    result = await StartupService.StartBrick();
  } catch (err) {
    render({ icon: "error", title: "Could not start Brick", message: describeError(err) });
    setActions({ label: "Retry", onClick: () => void launchBrick() });
    return;
  }

  if (result.status !== "ok") {
    if (result.code === "already_running") {
      // Some other brick process (started outside this app, or left over
      // from an earlier session) already holds the lock — that's a running
      // brick either way, so treat it as success rather than a failure.
      render({ icon: "ok", title: "Brick is running", message: "" });
      setTimeout(() => void closeWindow(), 500);
      return;
    }
    render({
      icon: "error",
      title: "Could not start Brick",
      message: result.message || `brick -d --json failed (${result.code ?? "unknown error"}).`,
    });
    setActions({ label: "Retry", onClick: () => void launchBrick() });
    return;
  }

  render({ icon: "ok", title: "Brick is starting", message: "" });
  setTimeout(() => void closeWindow(), 500);
}

// Step 4b: a check other than instance_lock failed — brick's guided setup
// is interactive (it can prompt for login, confirmations, etc.), so the Go
// side opens it in a real native terminal window rather than piping its
// output into this UI (see StartupService.RunSetup / terminal.go). This
// call just waits for that terminal to finish and reacts to the exit code.
async function runSetup(detail?: string) {
  render({
    icon: "spinner",
    title: "Brick setup",
    message:
      "Brick needs to be properly set up to continue. Follow the instructions in the terminal window that just opened — this will continue automatically once it's done.",
    detail,
  });
  setActions();
  showTerminal(false);

  try {
    const result = await StartupService.RunSetup();
    if (result.ok) {
      await launchBrick();
      return;
    }
    render({
      icon: "error",
      title: "Setup didn't finish",
      message: `brick --setup-and-exit exited with code ${result.exitCode}. Try running it again?`,
    });
    setActions(
      { label: "Run setup again", onClick: () => void runSetup() },
      {
        label: "Cancel",
        onClick: () => {
          render({
            icon: "error",
            title: "Brick setup",
            message:
              "Brick setup did not complete. Reopen this window from the tray menu once you're ready to try again.",
          });
          setActions({ label: "Retry", onClick: () => void runSelfTest() });
        },
      },
    );
  } catch (err) {
    render({ icon: "error", title: "Setup failed", message: describeError(err) });
    setActions({ label: "Retry", onClick: () => void runSetup() });
  }
}

void checkRunning();
