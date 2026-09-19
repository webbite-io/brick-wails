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
  remoteRootOptions,
  scopeOptions,
  screenForRoute,
  type Icon,
  type Option,
  type Screen,
} from "./wizard";

const spinnerEl = document.getElementById("stage-spinner")! as HTMLSpanElement;
const iconEl = document.getElementById("stage-icon")! as HTMLSpanElement;
const titleEl = document.getElementById("stage-title")! as HTMLParagraphElement;
const messageEl = document.getElementById("stage-message")! as HTMLParagraphElement;
const detailEl = document.getElementById("stage-detail")! as HTMLParagraphElement;
const checklistEl = document.getElementById("checklist")! as HTMLOListElement;
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
  messageEl.textContent = s.message;
  detailEl.textContent = s.detail ?? "";
  detailEl.classList.toggle("visible", !!s.detail);
}

function busy(title: string, message = "") {
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

async function refreshChecklist() {
  const items = (await OnboardingService.Checklist().catch(() => [])) ?? [];
  checklistEl.innerHTML = "";
  items.forEach((text, i) => {
    const li = document.createElement("li");
    const mark = document.createElement("span");
    mark.className = "check";
    mark.textContent = `${i + 1}. ✓`;
    const label = document.createElement("span");
    label.textContent = text;
    li.append(mark, label);
    checklistEl.appendChild(li);
  });
  checklistEl.classList.toggle("visible", items.length > 0);
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

// --- routing ---

let generation = 0; // bumps on every restart so stale async steps bail out

async function boot() {
  const gen = ++generation;
  busy("Starting Brick…");
  checklistEl.innerHTML = "";
  checklistEl.classList.remove("visible");
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
  if (needsWindow(route.step)) await showWindow();
  await refreshChecklist();
  bodyEl.innerHTML = "";
  render(screenForRoute(route));

  switch (route.step) {
    case "welcome":
      setActions(
        { label: "Log in", onClick: () => void doLogin(), cta: true },
        { label: "Not now", onClick: () => void OnboardingService.HideWindow() },
      );
      break;
    case "login":
      setActions(
        { label: "Log in again", onClick: () => void doLogin(), cta: true },
        { label: "Not now", onClick: () => void OnboardingService.HideWindow() },
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
      setActions({ label: "Retry", onClick: () => void reroute(), cta: true }, { label: "Close", onClick: () => void OnboardingService.HideWindow() });
      break;
  }
}

async function startSync(fromWizard: boolean) {
  busy("Starting Brick…", "Starting to sync your files…");
  const res = await OnboardingService.StartSync();
  if (res.ok) {
    render({ icon: "ok", title: "Brick is syncing", message: "" });
    if (fromWizard) await new Promise((r) => setTimeout(r, 600));
    await OnboardingService.HideWindow();
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
    await refreshChecklist();
    await new Promise((r) => setTimeout(r, 700));
    await reroute();
  } catch (err) {
    render({ icon: "error", title: "Login did not complete", message: describeError(err) });
    bodyEl.innerHTML = "";
    setActions({ label: "Try again", onClick: () => void doLogin(), cta: true }, { label: "Not now", onClick: () => void OnboardingService.HideWindow() });
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
  render({ icon: "ok", title: "Sync folder", message: "You have no sync folder configured. Choose a sync folder." });
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
    await refreshChecklist();
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
  bodyEl.innerHTML = "";
  const get = radioGroup("conflict", CONFLICT_OPTIONS);
  setActions(
    {
      label: "Continue",
      cta: true,
      onClick: async () => {
        try {
          await OnboardingService.ConfirmSyncFolder(get());
          await refreshChecklist();
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
        await refreshChecklist();
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
  await refreshChecklist();
  bodyEl.innerHTML = "";
  render({ icon: "ok", title: "Done and ready to go!", message: "Brick will now keep your sync folder up to date." });
  setActions({ label: "Start syncing", cta: true, onClick: () => void startSync(true) });
}

// --- entry ---

Events.On("setup:open", () => void boot());
void boot().catch((err) => {
  void showWindow();
  render({ icon: "error", title: "Brick setup", message: describeError(err) });
  setActions({ label: "Retry", onClick: () => void boot(), cta: true });
});
