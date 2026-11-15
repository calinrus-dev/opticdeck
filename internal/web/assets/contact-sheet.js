const defaults = { frameCount: 6, intervalMs: 220 };
const maxSampleTime = 12_000;
const gutter = 12;
const labelHeight = 24;

function wait(ms, signal) {
  signal?.throwIfAborted();
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', abort);
      resolve();
    }, Math.max(0, Math.ceil(ms)));
    function abort() {
      clearTimeout(timer);
      reject(signal.reason);
    }
    signal?.addEventListener('abort', abort, { once: true });
  });
}

export function encodeJPEG(canvas) {
  return new Promise((resolve, reject) => {
    canvas.toBlob(blob => {
      if (blob?.type === 'image/jpeg') resolve(blob);
      else reject(new Error('Could not encode this contact sheet as JPEG.'));
    }, 'image/jpeg', 0.86);
  });
}

// The result's canvas and frame bounds can also be used to encode individual
// frames from the identical samples without touching the live source again.
export async function captureContactSheet(getCanvas, {
  frameCount = defaults.frameCount, intervalMs = defaults.intervalMs, signal, onFrame,
} = {}) {
  if (!Number.isInteger(frameCount) || frameCount < 4 || frameCount > 9 ||
      !Number.isInteger(intervalMs) || intervalMs < 150 || intervalMs > 1000) {
    throw new RangeError('Choose 4–9 frames at an interval of 150–1000 ms.');
  }
  signal?.throwIfAborted();
  const source = getCanvas();
  if (!(source instanceof HTMLCanvasElement) || !source.width || !source.height) {
    throw new Error('Wait until the desktop is connected and try again.');
  }
  const sourceWidth = source.width;
  const sourceHeight = source.height;
  const scale = Math.min(1, 480 / sourceWidth, 300 / sourceHeight);
  const imageWidth = Math.max(1, Math.round(sourceWidth * scale));
  const imageHeight = Math.max(1, Math.round(sourceHeight * scale));
  const tileWidth = Math.max(100, imageWidth);
  const columns = frameCount === 4 ? 2 : 3;
  const rows = Math.ceil(frameCount / columns);
  const canvas = document.createElement('canvas');
  canvas.width = columns * (tileWidth + gutter) + gutter;
  canvas.height = rows * (imageHeight + labelHeight + gutter) + gutter;
  const context = canvas.getContext('2d', { alpha: false });
  if (!context) throw new Error('Could not prepare a canvas for the contact sheet.');
  context.fillStyle = '#111c25';
  context.fillRect(0, 0, canvas.width, canvas.height);
  context.font = '13px system-ui, sans-serif';
  context.textBaseline = 'middle';
  context.imageSmoothingQuality = 'high';

  const frames = [];
  const started = performance.now();
  let firstSample;
  let previousSample;
  for (let index = 0; index < frameCount; index++) {
    if (index > 0) await wait(previousSample + intervalMs - performance.now(), signal);
    signal?.throwIfAborted();
    if (getCanvas() !== source || source.width !== sourceWidth || source.height !== sourceHeight) {
      throw new Error('The desktop changed during capture. Capture again when it is ready.');
    }
    const sampled = performance.now();
    if (sampled - started > maxSampleTime) {
      throw new Error('Capture took too long. Keep the viewer tab open and try again.');
    }
    firstSample ??= sampled;
    previousSample = sampled;
    const x = gutter + (index % columns) * (tileWidth + gutter) + Math.floor((tileWidth - imageWidth) / 2);
    const y = gutter + Math.floor(index / columns) * (imageHeight + labelHeight + gutter);
    context.drawImage(source, x, y, imageWidth, imageHeight);
    const atMs = sampled - firstSample;
    const number = index + 1;
    context.fillStyle = '#cceade';
    context.fillText(`${String(number).padStart(2, '0')} · +${Math.round(atMs)} ms`, x, y + imageHeight + 13);
    const frame = { number, atMs, x, y, width: imageWidth, height: imageHeight };
    frames.push(frame);
    onFrame?.(frame, frameCount);
  }

  const encodeStarted = performance.now();
  const blob = await encodeJPEG(canvas);
  signal?.throwIfAborted();
  return {
    blob, canvas, width: canvas.width, height: canvas.height, frames,
    sampleMs: frames[frames.length - 1].atMs, encodeMs: performance.now() - encodeStarted,
  };
}

export function mountContactSheet(getCanvas) {
  const trigger = document.getElementById('capture-contact-sheet');
  const status = document.getElementById('contact-sheet-status');
  const dialog = document.getElementById('contact-sheet-dialog');
  const detail = document.getElementById('contact-sheet-detail');
  const image = document.getElementById('contact-sheet-image');
  const download = document.getElementById('contact-sheet-download');
  let previewURL;
  let active;
  let available = false;

  function updateTrigger() {
    trigger.disabled = !!active || (!available && !previewURL);
    trigger.textContent = previewURL ? 'View contact sheet' : 'Capture contact sheet';
  }

  function clearPreview() {
    if (!previewURL) return;
    URL.revokeObjectURL(previewURL);
    previewURL = null;
    image.removeAttribute('src');
    download.removeAttribute('href');
    updateTrigger();
  }

  function setAvailable(ready) {
    available = ready;
    if (!ready && active) {
      active.abort(new Error('The desktop connection changed. Capture again when it is ready.'));
    }
    updateTrigger();
  }

  trigger.addEventListener('click', async () => {
    if (previewURL) {
      if (!dialog.open) dialog.showModal();
      return;
    }
    if (!available || active) return;
    active = new AbortController();
    updateTrigger();
    status.textContent = 'Starting contact sheet…';
    try {
      const { blob, frames, width, height, sampleMs } = await captureContactSheet(getCanvas, {
        signal: active.signal,
        onFrame: (frame, count) => { status.textContent = `Captured ${frame.number} of ${count} frames`; },
      });
      previewURL = URL.createObjectURL(blob);
      image.src = previewURL;
      image.alt = `Contact sheet with ${frames.length} sequential desktop frames and elapsed time labels.`;
      detail.textContent = `${frames.length} frames across ${(sampleMs / 1000).toFixed(2)} s · ${width} × ${height} JPEG`;
      download.href = previewURL;
      download.download = `opticdeck-contact-sheet-${new Date().toISOString().replace(/[:.]/g, '-')}.jpg`;
      status.textContent = 'Contact sheet ready';
      if (!document.querySelector('dialog[open]')) dialog.showModal();
    } catch (error) {
      status.textContent = error.message || 'Could not capture the contact sheet.';
    } finally {
      active = null;
      updateTrigger();
    }
  });
  dialog.addEventListener('close', () => {
    clearPreview();
    status.textContent = '';
  });
  window.addEventListener('pagehide', event => {
    active?.abort();
    if (!event.persisted) clearPreview();
  });
  updateTrigger();
  return { setAvailable };
}
