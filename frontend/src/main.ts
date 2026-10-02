import {Events, Window} from "@wailsio/runtime";
import {SyncService} from "../bindings/github.com/webbite-io/brick-wails";
import {actionForState, hidesPopoverForState, STATE_LABELS} from "./status";

const stateDot = document.getElementById('state-dot')! as HTMLSpanElement;
const stateLabel = document.getElementById('state-label')! as HTMLSpanElement;
const folderEl = document.getElementById('folder')! as HTMLParagraphElement;
const errorEl = document.getElementById('error')! as HTMLParagraphElement;
const countUploaded = document.getElementById('count-uploaded')! as HTMLSpanElement;
const countDownloaded = document.getElementById('count-downloaded')! as HTMLSpanElement;
const countDeleted = document.getElementById('count-deleted')! as HTMLSpanElement;
const countMoved = document.getElementById('count-moved')! as HTMLSpanElement;
const activityList = document.getElementById('activity-list')! as HTMLUListElement;
const pauseBtn = document.getElementById('pause-btn')! as HTMLButtonElement;
const setupBtn = document.getElementById('setup-btn')! as HTMLButtonElement;

// Path data copied from lucide icons (arrow-up, arrow-down, arrow-right, trash).
const ARROW_UP_PATHS = ['m5 12 7-7 7 7', 'M12 19V5'];
const ARROW_DOWN_PATHS = ['M12 5v14', 'm19 12-7 7-7-7'];
const ARROW_RIGHT_PATHS = ['M5 12h14', 'm12 5 7 7-7 7'];
const TRASH_PATHS = [
    'M3 6h18',
    'M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6',
    'M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2',
];

// Text label shown after the icon for each activity kind. Kinds without an
// entry (move, move-folder) render with just the icon and the file path.
const ACTIVITY_TEXT_LABELS: Record<string, string> = {
    upload: 'Uploaded',
    update: 'Updated',
    download: 'Downloaded',
    trash: 'Removed',
    'trash-folder': 'Removed folder',
    remove: 'Removed',
    'remove-folder': 'Removed folder',
    'keep-both': 'Kept both copies of',
};

// Lucide icon (as path data) shown for each activity kind. Kinds without an
// entry (keep-both) fall back to a plain glyph in the same icon slot.
const ACTIVITY_ICON_PATHS: Record<string, string[]> = {
    upload: ARROW_UP_PATHS,
    update: ARROW_UP_PATHS,
    download: ARROW_DOWN_PATHS,
    trash: TRASH_PATHS,
    'trash-folder': TRASH_PATHS,
    remove: TRASH_PATHS,
    'remove-folder': TRASH_PATHS,
};

const MOVE_ACTIVITY_KINDS = new Set(['move', 'move-folder']);

function createIcon(paths: string[]): SVGSVGElement {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('width', '12');
    svg.setAttribute('height', '12');
    svg.setAttribute('fill', 'none');
    svg.setAttribute('stroke', 'currentColor');
    svg.setAttribute('stroke-width', '2');
    svg.setAttribute('stroke-linecap', 'round');
    svg.setAttribute('stroke-linejoin', 'round');
    svg.classList.add('activity-icon');
    for (const d of paths) {
        const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
        path.setAttribute('d', d);
        svg.appendChild(path);
    }
    return svg;
}

let currentState = '';

function renderStatus(status: any) {
    const state = status.state as string;
    currentState = state;
    stateDot.className = 'dot state-' + state;
    stateLabel.innerText = STATE_LABELS[state] ?? state;

    folderEl.innerText = status.folder || '';
    folderEl.style.display = status.folder ? '' : 'none';

    setInFlight(status.inFlight ?? null);

    if (status.lastError) {
        errorEl.innerText = status.lastError;
        errorEl.style.display = '';
    } else {
        errorEl.style.display = 'none';
    }

    const counters = status.counters ?? {uploaded: 0, downloaded: 0, deleted: 0, moved: 0};
    countUploaded.innerText = String(counters.uploaded ?? 0);
    countDownloaded.innerText = String(counters.downloaded ?? 0);
    countDeleted.innerText = String(counters.deleted ?? 0);
    countMoved.innerText = String(counters.moved ?? 0);

    const running = !!status.running;
    pauseBtn.disabled = !running;
    pauseBtn.style.display = running ? '' : 'none';
    pauseBtn.innerText = state === 'paused' ? 'Resume Sync' : 'Pause Sync';

    // When nothing is syncing, offer the way back (set up / log in / start).
    const action = actionForState(state);
    setupBtn.style.display = action ? '' : 'none';
    setupBtn.innerText = action ?? '';
}

// activityRow is one line of the list: an icon slot and the text.
function activityRow(icon: SVGSVGElement | string | null, text: string): HTMLLIElement {
    const li = document.createElement('li');

    const iconSlot = document.createElement('span');
    iconSlot.className = 'activity-icon-slot';
    if (typeof icon === 'string') {
        iconSlot.innerText = icon;
    } else if (icon) {
        iconSlot.appendChild(icon);
    }
    li.appendChild(iconSlot);

    const label = document.createElement('span');
    label.className = 'activity-text';
    label.innerText = text;
    li.appendChild(label);

    return li;
}

// The transfer running right now. It heads the activity list rather than
// sitting in a line of its own above it, so the panel below doesn't jump as
// files start and finish.
let inFlight: any = null;
let activityEvents: any[] = [];

function renderActivity() {
    activityList.innerHTML = '';

    if (inFlight) {
        const down = inFlight.direction !== 'upload';
        const row = activityRow(createIcon(down ? ARROW_DOWN_PATHS : ARROW_UP_PATHS), `${down ? 'Downloading' : 'Uploading'} ${inFlight.relPath}`);
        row.className = 'in-flight';
        activityList.appendChild(row);
    }

    for (const event of activityEvents) {
        let icon: SVGSVGElement | string | null = null;
        if (MOVE_ACTIVITY_KINDS.has(event.kind)) {
            icon = createIcon(ARROW_RIGHT_PATHS);
        } else if (ACTIVITY_ICON_PATHS[event.kind]) {
            icon = createIcon(ACTIVITY_ICON_PATHS[event.kind]);
        } else if (event.kind === 'keep-both') {
            icon = '⧉';
        }
        const label = ACTIVITY_TEXT_LABELS[event.kind];
        activityList.appendChild(activityRow(icon, label ? `${label} ${event.relPath}` : event.relPath));
    }

    if (!inFlight && activityEvents.length === 0) {
        const li = document.createElement('li');
        li.className = 'activity-empty';
        li.innerText = 'No activity yet';
        activityList.appendChild(li);
    }
}

// setInFlight redraws only when the transfer actually changes: status events
// arrive every two seconds, and rebuilding the list each time would throw away
// the user's scroll position.
let inFlightKey = '';

function setInFlight(next: any) {
    const key = next ? `${next.direction} ${next.relPath}` : '';
    if (key === inFlightKey) return;
    inFlightKey = key;
    inFlight = next;
    renderActivity();
}

async function refreshActivity() {
    try {
        activityEvents = (await SyncService.Activity(20)) ?? [];
        renderActivity();
    } catch (err) {
        console.error(err);
    }
}

// The Go side pushes "brick:status" whenever the engine's status changes
// (and every 2s), and "brick:activity" for each sync event.
Events.On('brick:status', (event) => {
    renderStatus(event.data);
});
Events.On('brick:activity', () => {
    void refreshActivity();
});

setupBtn.addEventListener('click', async () => {
    const hide = hidesPopoverForState(currentState);
    try {
        await SyncService.OpenSetup();
    } catch (err) {
        console.error(err);
        return; // stay open: the setup window never got the message
    }
    if (hide) await Window.Hide().catch(console.error);
});

pauseBtn.addEventListener('click', async () => {
    try {
        const status = await SyncService.Status();
        if (status.state === 'paused') {
            await SyncService.Resume();
        } else {
            await SyncService.Pause();
        }
    } catch (err) {
        console.error(err);
    }
});

// Initial paint before the first status event arrives.
SyncService.Status().then(renderStatus).catch(console.error);
refreshActivity();
