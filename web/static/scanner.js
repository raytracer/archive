const video = document.querySelector("#camera");
const editor = document.querySelector("#pageEditor");
const ctx = editor.getContext("2d");
const pagesEl = document.querySelector("#pages");
const statusEl = document.querySelector("#scanStatus");

const pages = [];
let activePage = -1;
let draggingPoint = -1;

document.querySelector("#startCamera").addEventListener("click", async () => {
  const stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: "environment" }, audio: false });
  video.srcObject = stream;
  await video.play();
});

document.querySelector("#capturePage").addEventListener("click", () => {
  if (!video.videoWidth) return;
  const raw = document.createElement("canvas");
  raw.width = video.videoWidth;
  raw.height = video.videoHeight;
  raw.getContext("2d").drawImage(video, 0, 0);
  pages.push({ raw, points: proposeDocumentBox(raw) });
  setActivePage(pages.length - 1);
  renderThumbnails();
  updateStatus();
});

document.querySelector("#uploadScan").addEventListener("click", async () => {
  if (!pages.length) return;
  const croppedPages = pages.map(page => cropCanvas(page.raw, page.points));
  const pdf = makePDF(croppedPages);
  const form = new FormData();
  form.append("pdf", new Blob([pdf], { type: "application/pdf" }), `scan-${Date.now()}.pdf`);
  const res = await fetch("/upload", { method: "POST", body: form });
  if (!res.ok) {
    alert(await res.text());
    return;
  }
  location.href = "/";
});

function setActivePage(index) {
  activePage = index;
  drawEditor();
  renderThumbnails();
}

function renderThumbnails() {
  pagesEl.replaceChildren();
  pages.forEach((page, index) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = `page-thumb${index === activePage ? " active" : ""}`;
    button.addEventListener("click", () => setActivePage(index));
    const img = new Image();
    img.src = page.raw.toDataURL("image/jpeg", .72);
    const badge = document.createElement("span");
    badge.textContent = String(index + 1);
    button.append(img, badge);
    pagesEl.appendChild(button);
  });
}

function updateStatus() {
  statusEl.textContent = `${pages.length} page${pages.length === 1 ? "" : "s"}`;
}

function drawEditor() {
  const page = pages[activePage];
  const parent = editor.parentElement.getBoundingClientRect();
  const cssWidth = Math.max(1, parent.width - 24);
  const cssHeight = Math.max(1, parent.height - 24);
  editor.style.width = `${cssWidth}px`;
  editor.style.height = `${cssHeight}px`;
  editor.width = cssWidth * devicePixelRatio;
  editor.height = cssHeight * devicePixelRatio;
  ctx.setTransform(devicePixelRatio, 0, 0, devicePixelRatio, 0, 0);
  ctx.clearRect(0, 0, cssWidth, cssHeight);
  if (!page) {
    ctx.fillStyle = "#111820";
    ctx.fillRect(0, 0, cssWidth, cssHeight);
    return;
  }
  const view = imageView(page.raw, cssWidth, cssHeight);
  ctx.drawImage(page.raw, view.x, view.y, view.w, view.h);
  const screenPoints = page.points.map(point => rawToScreen(point, page.raw, view));
  ctx.fillStyle = "rgba(0,0,0,.34)";
  ctx.fillRect(0, 0, cssWidth, cssHeight);
  ctx.beginPath();
  screenPoints.forEach((p, i) => i ? ctx.lineTo(p.x, p.y) : ctx.moveTo(p.x, p.y));
  ctx.closePath();
  ctx.save();
  ctx.globalCompositeOperation = "destination-out";
  ctx.fill();
  ctx.restore();
  ctx.strokeStyle = "#48d17c";
  ctx.lineWidth = 3;
  ctx.stroke();
  screenPoints.forEach(point => {
    ctx.beginPath();
    ctx.arc(point.x, point.y, 11, 0, Math.PI * 2);
    ctx.fillStyle = "#fff";
    ctx.fill();
    ctx.strokeStyle = "#48d17c";
    ctx.lineWidth = 3;
    ctx.stroke();
  });
}

function imageView(image, maxWidth, maxHeight) {
  const scale = Math.min(maxWidth / image.width, maxHeight / image.height);
  const w = image.width * scale;
  const h = image.height * scale;
  return { x: (maxWidth - w) / 2, y: (maxHeight - h) / 2, w, h, scale };
}

function rawToScreen(point, image, view) {
  return { x: view.x + point.x * view.scale, y: view.y + point.y * view.scale };
}

function screenToRaw(point, image, view) {
  return {
    x: clamp((point.x - view.x) / view.scale, 0, image.width),
    y: clamp((point.y - view.y) / view.scale, 0, image.height),
  };
}

function pointerPoint(event) {
  const rect = editor.getBoundingClientRect();
  return { x: event.clientX - rect.left, y: event.clientY - rect.top };
}

function nearestPoint(screenPoint) {
  const page = pages[activePage];
  if (!page) return -1;
  const view = imageView(page.raw, editor.clientWidth, editor.clientHeight);
  const points = page.points.map(point => rawToScreen(point, page.raw, view));
  return points.findIndex(point => Math.hypot(point.x - screenPoint.x, point.y - screenPoint.y) < 34);
}

editor.addEventListener("pointerdown", event => {
  draggingPoint = nearestPoint(pointerPoint(event));
  if (draggingPoint >= 0) {
    event.preventDefault();
    event.stopPropagation();
    editor.setPointerCapture(event.pointerId);
  }
});

editor.addEventListener("pointermove", event => {
  if (draggingPoint < 0 || activePage < 0) return;
  event.preventDefault();
  event.stopPropagation();
  const page = pages[activePage];
  const view = imageView(page.raw, editor.clientWidth, editor.clientHeight);
  page.points[draggingPoint] = screenToRaw(pointerPoint(event), page.raw, view);
  drawEditor();
});

editor.addEventListener("pointerup", event => {
  if (draggingPoint >= 0) {
    event.preventDefault();
    event.stopPropagation();
  }
  draggingPoint = -1;
});

editor.addEventListener("pointercancel", () => {
  draggingPoint = -1;
});

editor.addEventListener("lostpointercapture", () => {
  draggingPoint = -1;
});

window.addEventListener("resize", drawEditor);

function proposeDocumentBox(canvas) {
  const max = 180;
  const scale = Math.min(max / canvas.width, max / canvas.height, 1);
  const w = Math.max(1, Math.round(canvas.width * scale));
  const h = Math.max(1, Math.round(canvas.height * scale));
  const sample = document.createElement("canvas");
  sample.width = w;
  sample.height = h;
  const sampleCtx = sample.getContext("2d", { willReadFrequently: true });
  sampleCtx.drawImage(canvas, 0, 0, w, h);
  const data = sampleCtx.getImageData(0, 0, w, h).data;
  let minX = w, minY = h, maxX = 0, maxY = 0, hits = 0;
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const i = (y * w + x) * 4;
      const r = data[i], g = data[i + 1], b = data[i + 2];
      const brightness = (r + g + b) / 3;
      const contrast = Math.max(r, g, b) - Math.min(r, g, b);
      if (brightness > 150 && contrast < 82) {
        minX = Math.min(minX, x);
        minY = Math.min(minY, y);
        maxX = Math.max(maxX, x);
        maxY = Math.max(maxY, y);
        hits++;
      }
    }
  }
  if (hits < w * h * .08 || maxX - minX < w * .28 || maxY - minY < h * .28) {
    minX = w * .12;
    minY = h * .10;
    maxX = w * .88;
    maxY = h * .90;
  }
  const padX = (maxX - minX) * .03;
  const padY = (maxY - minY) * .03;
  minX = clamp(minX - padX, 0, w);
  minY = clamp(minY - padY, 0, h);
  maxX = clamp(maxX + padX, 0, w);
  maxY = clamp(maxY + padY, 0, h);
  return [
    { x: minX / scale, y: minY / scale },
    { x: maxX / scale, y: minY / scale },
    { x: maxX / scale, y: maxY / scale },
    { x: minX / scale, y: maxY / scale },
  ];
}

function cropCanvas(raw, points) {
  const xs = points.map(p => p.x);
  const ys = points.map(p => p.y);
  const minX = Math.floor(clamp(Math.min(...xs), 0, raw.width));
  const minY = Math.floor(clamp(Math.min(...ys), 0, raw.height));
  const maxX = Math.ceil(clamp(Math.max(...xs), 0, raw.width));
  const maxY = Math.ceil(clamp(Math.max(...ys), 0, raw.height));
  const out = document.createElement("canvas");
  out.width = Math.max(10, maxX - minX);
  out.height = Math.max(10, maxY - minY);
  out.getContext("2d").drawImage(raw, minX, minY, out.width, out.height, 0, 0, out.width, out.height);
  return out;
}

function makePDF(canvases) {
  const objects = [null, null];
  const pagesKids = [];
  function add(bytes) {
    objects.push(bytes);
    return objects.length;
  }
  canvases.forEach(canvas => {
    const jpg = dataURLBytes(canvas.toDataURL("image/jpeg", .9));
    const imgId = add(concatBytes(
      ascii(`<< /Type /XObject /Subtype /Image /Width ${canvas.width} /Height ${canvas.height} /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length ${jpg.length} >>\nstream\n`),
      jpg,
      ascii("\nendstream"),
    ));
    const content = `q\n${canvas.width} 0 0 ${canvas.height} 0 0 cm\n/Im${imgId} Do\nQ`;
    const contentId = add(ascii(`<< /Length ${ascii(content).length} >>\nstream\n${content}\nendstream`));
    const pageId = add(ascii(`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 ${canvas.width} ${canvas.height}] /Resources << /XObject << /Im${imgId} ${imgId} 0 R >> >> /Contents ${contentId} 0 R >>`));
    pagesKids.push(`${pageId} 0 R`);
  });
  objects[0] = ascii("<< /Type /Catalog /Pages 2 0 R >>");
  objects[1] = ascii(`<< /Type /Pages /Kids [${pagesKids.join(" ")}] /Count ${pagesKids.length} >>`);
  const chunks = [ascii("%PDF-1.4\n")];
  const offsets = [0];
  objects.forEach((obj, i) => {
    offsets.push(totalLength(chunks));
    chunks.push(ascii(`${i + 1} 0 obj\n`), obj, ascii("\nendobj\n"));
  });
  const xref = totalLength(chunks);
  chunks.push(ascii(`xref\n0 ${objects.length + 1}\n0000000000 65535 f \n`));
  offsets.slice(1).forEach(off => chunks.push(ascii(String(off).padStart(10, "0") + " 00000 n \n")));
  chunks.push(ascii(`trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF`));
  return concatBytes(...chunks);
}

function dataURLBytes(url) {
  const bin = atob(url.split(",")[1]);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes;
}

function ascii(text) {
  return new TextEncoder().encode(text);
}

function totalLength(chunks) {
  return chunks.reduce((sum, chunk) => sum + chunk.length, 0);
}

function concatBytes(...chunks) {
  const out = new Uint8Array(totalLength(chunks));
  let offset = 0;
  chunks.forEach(chunk => {
    out.set(chunk, offset);
    offset += chunk.length;
  });
  return out;
}

function clamp(value, min, max) {
  return Math.max(min, Math.min(max, value));
}

drawEditor();
