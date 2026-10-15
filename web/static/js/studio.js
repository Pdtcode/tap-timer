// Tap Timer Studio. The New post form sends the video as the raw request body
// (the server streams it to each platform) with the title and captions in a
// header. A video from Record reaches it through the browser's own storage
// (IndexedDB), never the server. The Connected page has a copy button.
(() => {
  "use strict";

  // The latest recording, kept in this browser between Record and New post.
  const idb = (mode, fn) => new Promise((resolve, reject) => {
    const open = indexedDB.open("tt-studio", 1);
    open.onupgradeneeded = () => open.result.createObjectStore("kv");
    open.onerror = () => reject(open.error);
    open.onsuccess = () => {
      const tx = open.result.transaction("kv", mode);
      const req = fn(tx.objectStore("kv"));
      tx.oncomplete = () => resolve(req.result);
      tx.onerror = () => reject(tx.error);
    };
  });
  window.studioRecording = {
    save: (blob) => idb("readwrite", (s) => s.put(blob, "recording")),
    load: () => idb("readonly", (s) => s.get("recording")),
    clear: () => idb("readwrite", (s) => s.delete("recording")),
  };

  for (const btn of document.querySelectorAll("[data-copy]")) {
    btn.addEventListener("click", async () => {
      const field = document.getElementById(btn.dataset.copy);
      try {
        await navigator.clipboard.writeText(field.value);
      } catch {
        field.select(); // let the person copy it by hand
        return;
      }
      btn.textContent = "Copied";
    });
  }

  const form = document.getElementById("new-post");
  if (!form) return;

  const file = form.elements.video;
  const title = form.elements.title;
  const preview = form.querySelector(".st-preview");
  const progress = form.querySelector(".st-progress");
  const results = form.querySelector(".st-results");
  const status = form.querySelector(".st-status");
  const button = form.querySelector("button[type=submit]");
  const maxBytes = Number(form.dataset.maxMb) * 1024 * 1024;

  let recorded = null; // a video from Record, used unless you pick a file

  function showPreview(blob) {
    if (preview.src) URL.revokeObjectURL(preview.src);
    preview.hidden = !blob;
    if (blob) preview.src = URL.createObjectURL(blob);
  }

  file.addEventListener("change", () => {
    if (file.files[0]) recorded = null; // a picked file wins
    showPreview(file.files[0] || recorded);
  });

  // Opened from Record ("Post it"): use that recording.
  if (new URLSearchParams(location.search).has("recording")) {
    window.studioRecording.load().then((blob) => {
      if (!blob) return say("That recording isn't here any more. Record again, or pick a video.");
      recorded = blob;
      file.required = false;
      let hook = "";
      try {
        hook = sessionStorage.getItem("st.hook") || "";
      } catch {}
      if (hook && !title.value) {
        title.value = hook.slice(0, 100);
        title.dispatchEvent(new Event("input")); // fills {title} into the captions
      }
      form.querySelector("[data-file-label]").textContent = "Video: your recording (or pick another)";
      showPreview(blob);
    }).catch(() => say("Couldn't load your recording. Pick the saved video instead."));
  }

  // Fill {title} into each caption until you edit that caption yourself.
  const captions = [...form.querySelectorAll("textarea[data-template]")];
  for (const ta of captions) ta.addEventListener("input", () => (ta.dataset.edited = "1"));
  title.addEventListener("input", () => {
    for (const ta of captions) {
      if (!ta.dataset.edited) ta.value = ta.dataset.template.replaceAll("{title}", title.value.trim());
    }
  });

  const say = (msg) => (status.textContent = msg);

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const f = file.files[0] || recorded;
    if (!f) return say("Pick a video first.");
    if (f.size > maxBytes) return say(`That video is over ${form.dataset.maxMb} MB.`);
    if (!title.value.trim()) return say("Add a title.");

    const picked = [...form.querySelectorAll("input[name=platform]:checked")].map((i) => i.value);
    if (!picked.length) return say("Pick at least one platform.");
    const meta = {
      title: title.value,
      captions: Object.fromEntries(picked.map((p) => [p, form.elements["caption-" + p].value])),
    };

    button.disabled = true;
    results.hidden = true;
    try {
      const data = await upload(f, meta);
      showResults(data.results || []);
      say("");
      // Posted somewhere: the kept recording has done its job.
      if (f === recorded && (data.results || []).some((r) => r.status !== "failed")) {
        window.studioRecording.clear().catch(() => {});
      }
    } catch (err) {
      say(err.message);
    } finally {
      button.disabled = false;
      progress.hidden = true;
    }
  });

  // base64url of the UTF-8 JSON, so captions with emoji survive a header.
  function encodeMeta(meta) {
    const bytes = new TextEncoder().encode(JSON.stringify(meta));
    let bin = "";
    for (const b of bytes) bin += String.fromCharCode(b);
    return btoa(bin).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
  }

  // XHR rather than fetch: fetch can't report upload progress.
  function upload(f, meta) {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open("POST", "/studio/api/upload");
      xhr.setRequestHeader("Content-Type", f.type || "application/octet-stream");
      xhr.setRequestHeader("X-Studio-Post", encodeMeta(meta));
      progress.hidden = false;
      progress.value = 0;
      say("Uploading…");
      xhr.upload.onprogress = (e) => {
        if (!e.lengthComputable) return;
        progress.value = e.loaded / e.total;
        say(e.loaded < e.total ? `Uploading… ${Math.round((100 * e.loaded) / e.total)}%` : "Publishing…");
      };
      xhr.onload = () => {
        let data = {};
        try {
          data = JSON.parse(xhr.responseText);
        } catch {}
        if (xhr.status === 200 || xhr.status === 207) resolve(data);
        else if (xhr.status === 401) reject(new Error("Signed out. Sign in again and retry."));
        else reject(new Error(data.error || `Upload failed (${xhr.status}).`));
      };
      xhr.onerror = () => reject(new Error("Connection lost during upload. Try again on a better signal."));
      xhr.send(f);
    });
  }

  function showResults(list) {
    results.replaceChildren(
      ...list.map((r) => {
        const li = document.createElement("li");
        li.className = "is-" + r.status;
        const text = { published: "posted", draft: "sent to drafts", failed: "failed" }[r.status] || r.status;
        if (r.url) {
          const a = document.createElement("a");
          a.href = r.url;
          a.target = "_blank";
          a.rel = "noopener";
          a.textContent = `${r.label}: ${text}`;
          li.append(a);
        } else {
          li.textContent = `${r.label}: ${text}${r.error ? ` (${r.error})` : ""}`;
        }
        return li;
      })
    );
    results.hidden = false;
  }
})();
