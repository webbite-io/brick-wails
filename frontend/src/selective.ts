// Selective Sync window: updates which top-level folders on Brick are synced,
// opened from the tray's "Selective Sync…". Like brick-cli's `brick sync -s`,
// every folder not already excluded starts out ticked; Save excludes the
// unticked ones (deleting their local copies) and the running sync adopts the
// change at once.

import { Events } from "@wailsio/runtime";
import { SelectiveSyncService } from "../bindings/github.com/webbite-io/brick-wails";
import { describeError } from "./wizard";

const listEl = document.getElementById("folder-list")! as HTMLDivElement;
const noteEl = document.getElementById("list-note")! as HTMLParagraphElement;
const errorEl = document.getElementById("selective-error")! as HTMLParagraphElement;
const cancelBtn = document.getElementById("cancel-btn")! as HTMLButtonElement;
const saveBtn = document.getElementById("save-btn")! as HTMLButtonElement;

// The folders Save reports on, as listed when the window opened. Null until
// they have loaded, and while loading failed: there is nothing to save then.
let listed: string[] | null = null;
// Bumped on every reload, so a slow load can't overwrite a newer one.
let generation = 0;

function showNote(text: string) {
  noteEl.textContent = text;
  noteEl.hidden = !text;
}

function showError(text: string) {
  errorEl.textContent = text;
  errorEl.hidden = !text;
}

function renderFolders(folders: string[], synced: string[]) {
  listEl.innerHTML = "";
  for (const name of folders) {
    const label = document.createElement("label");
    label.className = "check-row";
    const input = document.createElement("input");
    input.type = "checkbox";
    input.value = name;
    input.checked = synced.includes(name);
    const text = document.createElement("span");
    text.textContent = name;
    label.append(input, text);
    listEl.appendChild(label);
  }
}

function ticked(): string[] {
  return Array.from(listEl.querySelectorAll<HTMLInputElement>("input:checked")).map((i) => i.value);
}

// load resets the window and fetches the folders afresh: it's reused, and
// what's on Brick or in excludeDirs may have changed since it was last open.
async function load() {
  const gen = ++generation;
  listed = null;
  listEl.innerHTML = "";
  showError("");
  showNote("Loading folders…");
  cancelBtn.disabled = false;
  saveBtn.disabled = true;
  try {
    const info = await SelectiveSyncService.Folders();
    if (gen !== generation) return;
    const folders = info?.folders ?? [];
    listed = folders;
    renderFolders(folders, info?.synced ?? []);
    showNote(folders.length ? "" : "No folders on Brick.");
    saveBtn.disabled = folders.length === 0;
  } catch (err) {
    if (gen !== generation) return;
    showNote("");
    showError(describeError(err));
  }
}

async function save() {
  if (!listed) return;
  cancelBtn.disabled = true;
  saveBtn.disabled = true;
  showError("");
  try {
    await SelectiveSyncService.Save(listed, ticked());
  } catch (err) {
    showError(describeError(err));
  } finally {
    cancelBtn.disabled = false;
    saveBtn.disabled = false;
  }
}

cancelBtn.onclick = () => void SelectiveSyncService.Close();
saveBtn.onclick = () => void save();
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") void SelectiveSyncService.Close();
});

Events.On("selective:open", () => void load());
