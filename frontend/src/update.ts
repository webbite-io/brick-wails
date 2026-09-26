// Update window: shown when the startup check in main.go (internal/update)
// finds a newer release than the one that's running. It starts hidden and
// Go shows it itself once the check resolves, so this only has to render
// whatever it's told and drive the two buttons.

import { Events } from "@wailsio/runtime";
import { UpdateService } from "../bindings/github.com/webbite-io/brick-wails";
import type { Info } from "../bindings/github.com/webbite-io/brick-wails/internal/update/models";
import { describeError } from "./wizard";

const currentEl = document.getElementById("current-version")! as HTMLSpanElement;
const latestEl = document.getElementById("latest-version")! as HTMLSpanElement;
const errorEl = document.getElementById("update-error")! as HTMLParagraphElement;
const continueBtn = document.getElementById("continue-btn")! as HTMLButtonElement;
const updateBtn = document.getElementById("update-btn")! as HTMLButtonElement;

function render(info: Info) {
  currentEl.textContent = info.current;
  latestEl.textContent = info.latest;
  errorEl.textContent = "";
  // Update is the default action: focusing it lets Enter trigger it as soon
  // as the window is shown, matching its visual emphasis as the CTA.
  updateBtn.focus();
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
  if (e.key === "Enter" && !updateBtn.disabled) void doUpdate();
});

Events.On("update:available", (event) => render(event.data as Info));
void UpdateService.Info().then((info) => {
  if (info) render(info);
});
