// Remove window: confirms taking a remote control folder out of config.yaml's
// agentRoots, opened from that folder's "Remove…" item in the tray. Go keeps
// which folder it is (RemoteControlService); this only answers.

import { Events } from "@wailsio/runtime";
import { RemoteControlService } from "../bindings/github.com/webbite-io/brick-wails";
import { describeError } from "./wizard";

const errorEl = document.getElementById("remove-error")! as HTMLParagraphElement;
const cancelBtn = document.getElementById("cancel-btn")! as HTMLButtonElement;
const deleteBtn = document.getElementById("delete-btn")! as HTMLButtonElement;

function showError(text: string) {
  errorEl.textContent = text;
  errorEl.classList.toggle("visible", !!text);
}

function reset() {
  showError("");
  cancelBtn.disabled = false;
  deleteBtn.disabled = false;
  // Cancel is the safe answer, so Enter lands on it rather than on Delete.
  cancelBtn.focus();
}

async function doDelete() {
  cancelBtn.disabled = true;
  deleteBtn.disabled = true;
  try {
    await RemoteControlService.Delete();
  } catch (err) {
    cancelBtn.disabled = false;
    deleteBtn.disabled = false;
    showError(describeError(err));
  }
}

cancelBtn.onclick = () => void RemoteControlService.Cancel();
deleteBtn.onclick = () => void doDelete();
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") void RemoteControlService.Cancel();
});

Events.On("remove:open", reset);
reset();
