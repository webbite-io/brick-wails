import {Events} from "@wailsio/runtime";
import {BrickService} from "../bindings/github.com/requestbite/brick-wails";

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
const quitBtn = document.getElementById('quit-btn')! as HTMLButtonElement;

const STATE_LABELS: Record<string, string> = {
    'not-running': 'Brick is not running',
    starting: 'Starting…',
    syncing: 'Syncing…',
    idle: 'Up to date',
    paused: 'Paused',
    error: 'Error',
};

const ACTIVITY_LABELS: Record<string, string> = {
    upload: '↑ Uploaded',
    update: '↑ Updated',
    download: '↓ Downloaded',
    trash: '🗑 Removed',
    'trash-folder': '🗑 Removed folder',
    remove: '🗑 Removed',
    'remove-folder': '🗑 Removed folder',
    'keep-both': '⧉ Kept both copies of',
};

// Kinds rendered with a lucide "arrow-right" icon instead of a text label.
const MOVE_ACTIVITY_KINDS = new Set(['move', 'move-folder']);

function createArrowRightIcon(): SVGSVGElement {
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
    for (const d of ['M5 12h14', 'm12 5 7 7-7 7']) {
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
    quitBtn.disabled = !running;
    pauseBtn.innerText = state === 'paused' ? 'Resume' : 'Pause';
}

function renderActivity(events: any[]) {
    activityList.innerHTML = '';
    for (const event of events) {
        const li = document.createElement('li');
        if (MOVE_ACTIVITY_KINDS.has(event.kind)) {
            li.appendChild(createArrowRightIcon());
            li.appendChild(document.createTextNode(' ' + event.relPath));
        } else {
            const label = ACTIVITY_LABELS[event.kind] ?? event.kind;
            li.innerText = `${label} ${event.relPath}`;
        }
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

quitBtn.addEventListener('click', async () => {
    try {
        await BrickService.QuitBrick();
    } catch (err) {
        console.error(err);
    }
});

// Initial paint before the first status event arrives, plus a periodic
// activity refresh (the activity feed isn't pushed the way status is).
BrickService.Status().then(renderStatus).catch(console.error);
refreshActivity();
setInterval(refreshActivity, 5000);
