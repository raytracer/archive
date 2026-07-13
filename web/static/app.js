const dropzone = document.querySelector("#dropzone");
const fileInput = document.querySelector("#fileInput");

async function uploadFiles(files) {
  const pdfs = [...files].filter(file => file.type === "application/pdf" || file.name.toLowerCase().endsWith(".pdf"));
  if (!pdfs.length) return;
  const form = new FormData();
  pdfs.forEach(file => form.append("pdf", file));
  dropzone?.classList.add("drag");
  const res = await fetch("/upload", { method: "POST", body: form });
  if (!res.ok) {
    alert(await res.text());
    dropzone?.classList.remove("drag");
    return;
  }
  location.href = "/";
}

if (dropzone) {
  ["dragenter", "dragover"].forEach(name => {
    dropzone.addEventListener(name, event => {
      event.preventDefault();
      dropzone.classList.add("drag");
    });
  });
  ["dragleave", "drop"].forEach(name => {
    dropzone.addEventListener(name, event => {
      event.preventDefault();
      if (name === "drop") uploadFiles(event.dataTransfer.files);
      else dropzone.classList.remove("drag");
    });
  });
}

fileInput?.addEventListener("change", event => uploadFiles(event.target.files));
