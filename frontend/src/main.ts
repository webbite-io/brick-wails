import {Events} from "@wailsio/runtime";
import {BrickService} from "../bindings/github.com/webbite-io/brick-wails";

const stateDot = document.getElementById('state-dot')! as HTMLSpanElement;
const stateLabel = document.getElementById('state-label')! as HTMLSpanElement;
const folderEl = document.getElementById('folder')! as HTMLParagraphElement;
const inFlightEl = document.getElementById('in-flight')! as HTMLParagraphElement;
const errorEl = document.getElementById('error')! as HTMLParagraphElement;
const countUploaded = document.getElementById('count-uploaded')! as HTMLSpanElement;
const countDownloaded = document.getElementById('count-downloaded')! as HTMLSpanElement;
const countDeleted = document.getElementById('count-deleted')! as HTMLSpanElement;
const countMoved = document.getElementById('count-moved')! as HTMLSpanElement;
const activityList = document.getElementById('activity-list')! as HTMLUListElement;
const pauseBtn = document.getElementById('pause-btn')! as HTMLButtonElement;

const STATE_LABELS: Record<string, string> = {
    'not-running': 'Brick is not running',
    starting: 'Starting…',
    syncing: 'Syncing…',
    idle: 'Up to date',
    paused: 'Paused',
    error: 'Error',
};

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

function renderStatus(status: any) {
    const state = status.state as string;
    stateDot.className = 'dot state-' + state;
    stateLabel.innerText = STATE_LABELS[state] ?? state;

    folderEl.innerText = status.folder || '';
    folderEl.style.display = status.folder ? '' : 'none';

    if (status.inFlight) {
        const arrow = status.inFlight.direction === 'upload' ? '↑' : '↓';
        inFlightEl.innerText = `${arrow} ${status.inFlight.relPath}`;
        inFlightEl.style.display = '';
    } else {
        inFlightEl.style.display = 'none';
    }

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

    const running = state !== 'not-running';
    pauseBtn.disabled = !running;
    pauseBtn.innerText = state === 'paused' ? 'Resume Sync' : 'Pause Sync';
}

function renderActivity(events: any[]) {
    activityList.innerHTML = '';
    for (const event of events) {
        const li = document.createElement('li');

        const iconSlot = document.createElement('span');
        iconSlot.className = 'activity-icon-slot';
        if (MOVE_ACTIVITY_KINDS.has(event.kind)) {
            iconSlot.appendChild(createIcon(ARROW_RIGHT_PATHS));
        } else if (ACTIVITY_ICON_PATHS[event.kind]) {
            iconSlot.appendChild(createIcon(ACTIVITY_ICON_PATHS[event.kind]));
        } else if (event.kind === 'keep-both') {
            iconSlot.innerText = '⧉';
        }
        li.appendChild(iconSlot);

        const text = document.createElement('span');
        text.className = 'activity-text';
        const label = ACTIVITY_TEXT_LABELS[event.kind];
        text.innerText = label ? `${label} ${event.relPath}` : event.relPath;
        li.appendChild(text);

        activityList.appendChild(li);
    }
    if (events.length === 0) {
        const li = document.createElement('li');
        li.className = 'activity-empty';
        li.innerText = 'No activity yet';
        activityList.appendChild(li);
    }
}

async function refreshActivity() {
    try {
        renderActivity(await BrickService.Activity(20));
    } catch (err) {
        console.error(err);
    }
}

// The Go side already polls brick's control API every 2s and emits the
// result as a "brick:status" event, so the frontend just listens rather
// than polling brick itself a second time.
Events.On('brick:status', (event) => {
    renderStatus(event.data);
});

pauseBtn.addEventListener('click', async () => {
    try {
        const status = await BrickService.Status();
        if (status.state === 'paused') {
            await BrickService.Resume();
        } else {
            await BrickService.Pause();
        }
    } catch (err) {
        console.error(err);
    }
});

// Initial paint before the first status event arrives, plus a periodic
// activity refresh (the activity feed isn't pushed the way status is).
BrickService.Status().then(renderStatus).catch(console.error);
refreshActivity();
setInterval(refreshActivity, 5000);
