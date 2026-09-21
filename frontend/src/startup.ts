// Setup window: startup routing + the onboarding wizard (the graphical
// counterpart of brick-cli's interactive setup). The window starts hidden:
// on load this routes, and a fully configured machine goes straight to
// syncing without ever showing it. Anything needing the user shows the
// window. The Go side emits "setup:open" (tray "Set Up Brick…", popover
// buttons, or an expired session) to run the flow again.

import { Events } from "@wailsio/runtime";
import { OnboardingService } from "../bindings/github.com/webbite-io/brick-wails";
import type { Route, ScopeInfo } from "../bindings/github.com/webbite-io/brick-wails/internal/onboarding/models";
import {
  CONFLICT_OPTIONS,
  REMOTE_OPTIONS,
  describeError,
  displayPath,
  folderOptions,
  needsWindow,
  presentable,
  progressDots,
  remoteRootOptions,
  scopeOptions,
  screenForRoute,
  type Icon,
  type Option,
  type Screen,
  type WizardStep,
} from "./wizard";

const panelEl = document.getElementById("startup-panel")! as HTMLElement;
const spinnerEl = document.getElementById("stage-spinner")! as HTMLSpanElement;
const iconEl = document.getElementById("stage-icon")! as HTMLSpanElement;
const titleEl = document.getElementById("stage-title")! as HTMLParagraphElement;
const messageEl = document.getElementById("stage-message")! as HTMLParagraphElement;
const detailEl = document.getElementById("stage-detail")! as HTMLParagraphElement;
const progressEl = document.getElementById("wizard-progress")! as HTMLElement;
const bodyEl = document.getElementById("step-body")! as HTMLElement;
const primaryBtn = document.getElementById("primary-btn")! as HTMLButtonElement;
const secondaryBtn = document.getElementById("secondary-btn")! as HTMLButtonElement;

let home = "";

// --- rendering helpers ---

interface Action {
  label: string;
  onClick: () => void;
  cta?: boolean;
}

function render(s: Screen) {
  const icon: Icon = s.icon;
  spinnerEl.classList.toggle("hidden", icon !== "spinner");
  iconEl.classList.toggle("visible", icon !== "spinner");
  iconEl.classList.remove("icon-ok", "icon-error", "icon-warn");
  iconEl.textContent = icon === "ok" ? "✓" : icon === "spinner" ? "" : "!";
  if (icon !== "spinner") iconEl.classList.add(`icon-${icon}`);
  titleEl.textContent = s.title;
  // Backend copy reaches the window unfiltered, so drop anything that is a raw
  // payload rather than a sentence (see presentable).
  messageEl.textContent = presentable(s.message);
  const detail = presentable(s.detail);
  detailEl.textContent = detail;
  detailEl.classList.toggle("visible", !!detail);
}

// reveal shows the panel contents once a screen is laid out. boot() hides them
// again while it routes, so an already-visible window stays dark instead of
// flashing the previous screen (see .startup-panel:not(.ready) in startup.css).
function reveal() {
  panelEl.classList.add("ready");
}

// setWelcomeMode switches the panel between the plain welcome screen and the
// wizard layout. Re-entering welcome replays the intro: dropping the class and
// reading a layout property restarts the CSS animations.
function setWelcomeMode(on: boolean) {
  panelEl.classList.remove("welcome");
  if (!on) return;
  void panelEl.offsetWidth;
  panelEl.classList.add("welcome");
}

function busy(title: string, message = "") {
  setWelcomeMode(false);
  render({ icon: "spinner", title, message });
  setActions();
  bodyEl.innerHTML = "";
}

function applyButton(btn: HTMLButtonElement, a: Action | undefined, variant: "primary" | "quiet") {
  if (!a) {
    btn.onclick = null;
    btn.classList.remove("visible", "btn-cta", "btn-primary", "btn-quiet");
    return;
  }
  btn.textContent = a.label;
  btn.onclick = a.onClick;
  btn.classList.toggle("btn-primary", variant === "primary");
  btn.classList.toggle("btn-quiet", variant === "quiet");
  btn.classList.toggle("btn-cta", !!a.cta);
  btn.classList.add("visible");
}

function setActions(primary?: Action, secondary?: Action) {
  applyButton(primaryBtn, primary, "primary");
  applyButton(secondaryBtn, secondary, "quiet");
}

// setProgress draws the dot bar under the tagline for the wizard step now on
// screen; null takes it away (welcome, login, errors — anything that isn't a
// walk through the wizard).
function setProgress(step: WizardStep | null) {
  const dots = progressDots(step);
  progressEl.innerHTML = "";
  for (const done of dots) {
    const dot = document.createElement("span");
    dot.className = done ? "dot done" : "dot";
    progressEl.appendChild(dot);
  }
  progressEl.setAttribute("aria-valuenow", String(dots.filter(Boolean).length));
  progressEl.setAttribute("aria-valuemax", String(dots.length));
  progressEl.classList.toggle("visible", dots.length > 0);
}

// radioGroup renders options as radio rows and returns a getter for the
// selected value.
function radioGroup(name: string, options: Option[], selected?: string, onChange?: (v: string) => void): () => string {
  const group = document.createElement("div");
  group.style.display = "contents";
  for (const [i, o] of options.entries()) {
    const label = document.createElement("label");
    label.className = "option";
    const input = document.createElement("input");
    input.type = "radio";
    input.name = name;
    input.value = o.value;
    input.checked = selected ? o.value === selected : i === 0;
    input.onchange = () => onChange?.(o.value);
    const text = document.createElement("span");
    text.textContent = o.label;
    if (o.hint) {
      const hint = document.createElement("span");
      hint.className = "option-hint";
      hint.textContent = o.hint;
      text.appendChild(hint);
    }
    label.append(input, text);
    group.appendChild(label);
  }
  bodyEl.appendChild(group);
  return () => (group.querySelector<HTMLInputElement>("input:checked")?.value ?? "");
}

function checkboxGroup(options: string[], checked: string[]): () => string[] {
  const group = document.createElement("div");
  group.style.display = "contents";
  for (const name of options) {
    const label = document.createElement("label");
    label.className = "option";
    const input = document.createElement("input");
    input.type = "checkbox";
    input.value = name;
    input.checked = checked.includes(name);
    const text = document.createElement("span");
    text.textContent = name;
    label.append(input, text);
    group.appendChild(label);
  }
  bodyEl.appendChild(group);
  return () => Array.from(group.querySelectorAll<HTMLInputElement>("input:checked")).map((i) => i.value);
}

function stepLabel(text: string) {
  const p = document.createElement("p");
  p.className = "step-label";
  p.textContent = text;
  bodyEl.appendChild(p);
}

function fieldError(text: string) {
  const p = document.createElement("p");
  p.className = "field-error";
  p.textContent = text;
  bodyEl.appendChild(p);
}

function link(text: string, onClick: () => void) {
  const b = document.createElement("button");
  b.className = "link";
  b.textContent = text;
  b.onclick = onClick;
  bodyEl.appendChild(b);
}

async function showWindow() {
  await OnboardingService.ShowWindow().catch(console.error);
}

// hideWindow blanks the panel and lets it paint before hiding, so the frame
// the webview keeps for the next show is the dark background, not this screen.
async function hideWindow() {
  panelEl.classList.remove("ready");
  await new Promise(requestAnimationFrame);
  await OnboardingService.HideWindow().catch(console.error);
}

// --- routing ---

let generation = 0; // bumps on every restart so stale async steps bail out

async function boot() {
  const gen = ++generation;
  panelEl.classList.remove("ready");
  busy("Starting Brick…");
  setProgress(null);
  home = await OnboardingService.HomeDir().catch(() => "");
  const route = await OnboardingService.Route();
  if (gen !== generation) return;
  await handleRoute(route);
}

async function reroute() {
  const gen = generation;
  busy("Checking Brick…");
  const route = await OnboardingService.Route();
  if (gen !== generation) return;
  await handleRoute(route);
}

async function handleRoute(route: Route) {
  if (route.step === "ready") {
    await startSync(false);
    return;
  }
  // The bar belongs to the wizard: it appears with the sync-folder step
  // (folderStep) and is gone on welcome, login and the error screens.
  setProgress(null);
  bodyEl.innerHTML = "";
  render(screenForRoute(route));
  setWelcomeMode(route.step === "welcome");
  // Welcome is laid out before the window appears, so its first painted frame
  // is the finished screen with the intro still to play.
  if (route.step === "welcome") setActions({ label: "Log in", onClick: () => void doLogin(), cta: true });
  reveal();
  if (needsWindow(route.step)) await showWindow();

  switch (route.step) {
    case "welcome":
      break; // laid out above
    case "login":
      setActions(
        { label: "Log in again", onClick: () => void doLogin(), cta: true },
        { label: "Not now", onClick: () => void hideWindow() },
      );
      break;
    case "account":
      await accountStep();
      break;
    case "folder":
      await folderStep();
      break;
    case "locked":
    case "connect-error":
    default:
      setActions({ label: "Retry", onClick: () => void reroute(), cta: true }, { label: "Close", onClick: () => void hideWindow() });
      break;
  }
}

async function startSync(fromWizard: boolean) {
  busy("Starting Brick…", "Starting to sync your files…");
  reveal();
  const res = await OnboardingService.StartSync();
  if (res.ok) {
    render({ icon: "ok", title: "Brick is syncing", message: "" });
    if (fromWizard) await new Promise((r) => setTimeout(r, 600));
    await hideWindow();
    return;
  }
  await handleRoute({ step: res.step ?? "connect-error", firstRun: false, message: res.message ?? "", detail: undefined } as Route);
}

// --- login ---

async function doLogin() {
  busy("Opening browser for login…");
  let url: string;
  try {
    url = await OnboardingService.BeginLogin();
  } catch (err) {
    render({ icon: "error", title: "Login failed", message: describeError(err) });
    setActions({ label: "Try again", onClick: () => void doLogin(), cta: true });
    return;
  }
  render({ icon: "spinner", title: "Waiting for authorization…", message: "Complete the login in your browser. If the browser did not open, use the link below." });
  bodyEl.innerHTML = "";
  link("Open the login page again", () => void OnboardingService.OpenURL(url));
  setActions(undefined, {
    label: "Cancel",
    onClick: () => void OnboardingService.CancelLogin(),
  });

  try {
    const res = await OnboardingService.AwaitLogin();
    render({ icon: "ok", title: res?.greeting ?? "Login successful 🎉", message: "" });
    bodyEl.innerHTML = "";
    await new Promise((r) => setTimeout(r, 700));
    await reroute();
  } catch (err) {
    render({ icon: "error", title: "Login did not complete", message: describeError(err) });
    bodyEl.innerHTML = "";
    setActions({ label: "Try again", onClick: () => void doLogin(), cta: true }, { label: "Not now", onClick: () => void hideWindow() });
  }
}

// --- account ---

async function accountStep() {
  const accounts = (await OnboardingService.Accounts()) ?? [];
  stepLabel("Select an account");
  const get = radioGroup("account", accounts.map((a) => ({ value: a.id, label: a.name })));
  setActions({
    label: "Continue",
    cta: true,
    onClick: async () => {
      try {
        await OnboardingService.SelectAccount(get());
        await reroute();
      } catch (err) {
        fieldError(describeError(err));
      }
    },
  });
}

// --- sync folder (promptForSyncFolder) ---

async function folderStep(error?: string) {
  const def = await OnboardingService.DefaultSyncFolder();
  render({ icon: "ok", title: "Sync folder", message: "Please choose a sync folder." });
  setProgress("folder");
  bodyEl.innerHTML = "";
  stepLabel("Choose a sync folder");
  const get = radioGroup("folder", folderOptions(def, home));
  if (error) fieldError(error);
  setActions({
    label: "Continue",
    cta: true,
    onClick: async () => {
      switch (get()) {
        case "default":
          await chooseFolder(def);
          break;
        case "pick": {
          const picked = await OnboardingService.PickDirectory(home, "Pick a folder to sync");
          if (picked) await chooseFolder(picked); // cancelled: stay here
          break;
        }
        case "create":
          createFolderStep();
          break;
      }
    },
  });
}

function createFolderStep(error?: string) {
  render({ icon: "ok", title: "Create folder", message: `Create folder in ${displayPath(home, home)}` });
  setProgress("folder"); // still the sync-folder step, just a different screen
  bodyEl.innerHTML = "";
  const input = document.createElement("input");
  input.className = "text-input";
  input.placeholder = "folder or folder1/folder2";
  bodyEl.appendChild(input);
  if (error) fieldError(error);
  const submit = async () => {
    if (!input.value.trim()) {
      void folderStep(); // empty = back, like brick-cli
      return;
    }
    try {
      const created = await OnboardingService.CreateFolderInHome(input.value);
      await chooseFolder(created);
    } catch (err) {
      createFolderStep(describeError(err));
    }
  };
  input.onkeydown = (e) => {
    if (e.key === "Enter") void submit();
    if (e.key === "Escape") void folderStep();
  };
  setActions({ label: "Create", cta: true, onClick: () => void submit() }, { label: "Back", onClick: () => void folderStep() });
  input.focus();
}

async function chooseFolder(path: string) {
  try {
    const choice = await OnboardingService.ChooseSyncFolder(path);
    if (choice?.hasFiles) {
      conflictStep(choice.display);
      return;
    }
    await OnboardingService.ConfirmSyncFolder("");
    await connectStep();
  } catch (err) {
    await folderStep(describeError(err));
  }
}

// --- conflicts (promptConflictMode) ---

function conflictStep(display: string) {
  render({
    icon: "warn",
    title: "Conflict resolution",
    message: `${display} contains files. How should possible conflicts be handled on first sync?`,
  });
  setProgress("conflict");
  bodyEl.innerHTML = "";
  const get = radioGroup("conflict", CONFLICT_OPTIONS);
  setActions(
    {
      label: "Continue",
      cta: true,
      onClick: async () => {
        try {
          await OnboardingService.ConfirmSyncFolder(get());
          await connectStep();
        } catch (err) {
          fieldError(describeError(err));
        }
      },
    },
    { label: "Back", onClick: () => void folderStep() },
  );
}

// --- connect + scope (runSyncScopeOnboarding) ---

async function connectStep() {
  busy("Connecting to Brick…");
  let info: ScopeInfo | null;
  try {
    info = await OnboardingService.Connect();
  } catch (err) {
    render({ icon: "error", title: "Can't reach Brick", message: describeError(err) });
    setActions({ label: "Retry", onClick: () => void connectStep(), cta: true });
    return;
  }
  if (info?.showScope) {
    scopeStep(info);
  } else if (info?.showRemote) {
    remoteStep();
  } else {
    await doneStep();
  }
}

function scopeStep(info: ScopeInfo) {
  render({ icon: "ok", title: "Sync scope", message: "One last decision to make:" });
  setProgress("scope");
  bodyEl.innerHTML = "";
  let picking = false;
  let getExcluded: () => string[] = () => [];
  const get = radioGroup("scope", scopeOptions(info.totalHuman), "all", (v) => {
    if (v === "pick" && !picking) {
      picking = true;
      stepLabel("Select the folders to EXCLUDE from sync");
      getExcluded = checkboxGroup(info.folders ?? [], info.alreadyExcluded ?? []);
    }
  });
  setActions({
    label: "Continue",
    cta: true,
    onClick: async () => {
      try {
        const all = get() === "all";
        await OnboardingService.SetSyncScope(all, all ? [] : getExcluded());
        remoteStep();
      } catch (err) {
        fieldError(describeError(err));
      }
    },
  });
}

// --- remote access (promptForRemoteControl) ---

function remoteStep(custom?: string) {
  render({ icon: "ok", title: "Remote access", message: "Do you want to remotely access files on this device via Brick?" });
  setProgress("remote");
  bodyEl.innerHTML = "";
  let getRoot: (() => string) | null = null;
  const showRoots = () => {
    if (getRoot) return;
    stepLabel("Which folder should be accessible remotely?");
    getRoot = radioGroup("root", remoteRootOptions(home, custom), custom ? "custom" : "home");
  };
  const getYesNo = radioGroup("remote", REMOTE_OPTIONS, "yes", (v) => {
    if (v === "yes") showRoots();
  });
  showRoots();
  setActions({
    label: "Continue",
    cta: true,
    onClick: async () => {
      try {
        if (getYesNo() === "no") {
          await OnboardingService.SetRemoteAccess(false, "");
        } else if (getRoot?.() === "custom") {
          const picked = custom || (await OnboardingService.PickDirectory("/", "Pick a folder to expose remotely"));
          if (!picked) return; // cancelled: stay
          if (!custom) {
            remoteStep(picked); // show the choice before saving
            return;
          }
          await OnboardingService.SetRemoteAccess(true, picked);
        } else {
          await OnboardingService.SetRemoteAccess(true, home);
        }
        await doneStep();
      } catch (err) {
        fieldError(describeError(err));
      }
    },
  });
}

// --- done ---

async function doneStep() {
  await OnboardingService.FinishOnboarding();
  bodyEl.innerHTML = "";
  render({ icon: "ok", title: "Done and ready to go!", message: "Brick will now keep your sync folder up to date." });
  setProgress("done");
  setActions({ label: "Start syncing", cta: true, onClick: () => void startSync(true) });
}

// --- entry ---

Events.On("setup:open", () => void boot());
void boot().catch((err) => {
  render({ icon: "error", title: "Brick setup", message: describeError(err) });
  reveal();
  void showWindow();
  setActions({ label: "Retry", onClick: () => void boot(), cta: true });
});
