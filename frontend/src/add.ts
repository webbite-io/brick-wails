// Add window: shares a new folder over remote control, opened from the tray's
// "New Shared Folder…". It starts on the home folder; clicking the folder row
// opens the folder picker, each time it's clicked, and the folder on screen is
// always the one Save adds.

import { Events } from "@wailsio/runtime";
import { RemoteControlService } from "../bindings/github.com/webbite-io/brick-wails";
import { describeError } from "./wizard";

const folderRadio = document.getElementById("folder-radio")! as HTMLInputElement;
const readOnlyRadio = document.getElementById("ro-radio")! as HTMLInputElement;
const folderHint = document.getElementById("folder-hint")! as HTMLSpanElement;
const errorEl = document.getElementById("add-error")! as HTMLParagraphElement;
const cancelBtn = document.getElementById("cancel-btn")! as HTMLButtonElement;
const saveBtn = document.getElementById("save-btn")! as HTMLButtonElement;

let home = "";
let folder = "";

function setFolder(path: string) {
  folder = path;
  folderHint.textContent = path;
}

function showError(text: string) {
  errorEl.textContent = text;
  errorEl.hidden = !text;
}

// reset puts the window back to its defaults: it's reused, and may still
// show the last folder picked or an error.
function reset() {
  setFolder(home);
  readOnlyRadio.checked = true;
  showError("");
  cancelBtn.disabled = false;
  saveBtn.disabled = false;
}

// Cancelling the picker keeps the folder already shown.
folderRadio.addEventListener("click", () => {
  void (async () => {
    const picked = await RemoteControlService.PickFolder(folder || home);
    if (!picked) return;
    setFolder(picked);
    showError("");
  })();
});

async function save() {
  cancelBtn.disabled = true;
  saveBtn.disabled = true;
  try {
    await RemoteControlService.Save(folder, readOnlyRadio.checked);
  } catch (err) {
    showError(describeError(err));
  } finally {
    cancelBtn.disabled = false;
    saveBtn.disabled = false;
  }
}

cancelBtn.onclick = () => void RemoteControlService.CloseAdd();
saveBtn.onclick = () => void save();
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") void RemoteControlService.CloseAdd();
});

Events.On("add:open", reset);
reset();
void RemoteControlService.HomeDir().then((h) => {
  home = h;
  if (!folder) setFolder(h);
});
