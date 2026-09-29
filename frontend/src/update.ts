// Update window: the prompt shown when a check finds a newer release, and the
// answer when the user runs a check themselves from the tray menu. It holds no
// state of its own — Go decides what it should say (UpdateService.setView) and
// this renders whatever it's handed.

import { Events } from "@wailsio/runtime";
import { UpdateService } from "../bindings/github.com/webbite-io/brick-wails";
import type { UpdateView } from "../bindings/github.com/webbite-io/brick-wails/models";
import { describeError } from "./wizard";

const titleEl = document.getElementById("update-title")! as HTMLHeadingElement;
const messageEl = document.getElementById("update-message")! as HTMLParagraphElement;
const errorEl = document.getElementById("update-error")! as HTMLParagraphElement;
const continueBtn = document.getElementById("continue-btn")! as HTMLButtonElement;
const updateBtn = document.getElementById("update-btn")! as HTMLButtonElement;

function render(view: UpdateView) {
  errorEl.textContent = "";
  continueBtn.disabled = false;
  updateBtn.hidden = view.state !== "available";

  switch (view.state) {
    case "available":
      titleEl.textContent = "Update available";
      messageEl.textContent = `You're running v${view.current}. v${view.latest} is now available.`;
      continueBtn.textContent = "Cancel";
      updateBtn.disabled = false;
      updateBtn.textContent = "Update";
      // Update is the default action: focusing it lets Enter trigger it as soon
      // as the window is shown, matching its visual emphasis as the CTA.
      updateBtn.focus();
      break;
    case "uptodate":
      titleEl.textContent = "You're up to date";
      messageEl.textContent = `v${view.current} is the latest version.`;
      continueBtn.textContent = "Close";
      continueBtn.focus();
      break;
    case "failed":
      titleEl.textContent = "Couldn't check for updates";
      messageEl.textContent = "";
      errorEl.textContent = view.error ?? "";
      continueBtn.textContent = "Close";
      continueBtn.focus();
      break;
    default:
      // "checking": the user asked, so the window opens on this and is replaced
      // by the result a moment later.
      titleEl.textContent = "Checking for updates…";
      messageEl.textContent = "";
      continueBtn.textContent = "Close";
      break;
  }
}

async function doUpdate() {
  continueBtn.disabled = true;
  updateBtn.disabled = true;
  updateBtn.textContent = "Opening terminal…";
  try {
    await UpdateService.InstallUpdate();
    // The app quits itself once the terminal has launched; nothing left to do.
  } catch (err) {
    continueBtn.disabled = false;
    updateBtn.disabled = false;
    updateBtn.textContent = "Update";
    errorEl.textContent = describeError(err);
  }
}

continueBtn.onclick = () => void UpdateService.Continue();
updateBtn.onclick = () => void doUpdate();

// Belt-and-suspenders for the default action: Enter anywhere in the window
// triggers Update, not just when the button itself has focus.
document.addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !updateBtn.hidden && !updateBtn.disabled) void doUpdate();
});

Events.On("update:view", (event) => render(event.data as UpdateView));
// The window is hidden until Go has something to show, but it may still have
// been loading when the event fired — so ask for the current view on load too.
void UpdateService.View().then((view) => {
  if (view) render(view);
});
