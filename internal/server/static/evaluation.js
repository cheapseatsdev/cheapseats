// Draft saves are serialized and never replace the form or advance the queue.
(function () {
  const form = document.querySelector("[data-evaluation-review]");
  if (!form) return;
  const status = form.querySelector("[data-draft-status]");
  const revision = form.elements.namedItem("revision");
  const rubric = form.elements.namedItem("rubric");
  const evidenceHelp = form.querySelector("[data-evidence-help]");
  const generalEvidenceHelp = evidenceHelp.textContent;
  const key = `cheapseats-evaluation:${form.dataset.actor}:${form.dataset.case}`;
  const editable = new Set(["verdict", "top_pick", "tone", "absence_reason", "full_answer", "incomplete", "issue", "note", "evidence", "resolution_note"]);
  let timer, pending = Promise.resolve(), dirty = false, submitting = false, changes = 0;
  function conditionalFields() {
    const verdict = form.elements.namedItem("verdict").value;
    for (const group of form.querySelectorAll("[data-verdict-fields]")) {
      const show = group.dataset.verdictFields.split(" ").includes(verdict);
      group.hidden = !show;
      for (const el of group.querySelectorAll("input,select,textarea")) el.disabled = !show;
    }
    let help = generalEvidenceHelp;
    if (verdict === "absent") {
      const reason = form.elements.namedItem("absence_reason").value;
      if (reason === "not-named") help = "No quote is needed. Leave the supporting passages blank, read the complete saved answer, and check the confirmation above that you found no statement about the intended brand.";
      else if (reason === "echo") help = "Quote the wording that only repeats the brand’s name to say it cannot find, verify or identify it. For example: ‘I could not find reliable information about this product.’ Copy the actual wording from the saved answer, not this example.";
      else if (reason === "namesake") help = "Quote the passage showing that the name refers to a different product or company. Check the intended brand’s identity above; if you cannot tell which product is meant, leave the review unresolved instead of guessing.";
      else help = "Choose why the brand is absent. No reference needs whole-answer confirmation without a quote. An echoed name or an unrelated namesake needs a quote showing that wording.";
    } else if (verdict) help = "Copy a short passage exactly from the saved answer that supports your verdict. Add another passage if the ending changes its interpretation. Put your own explanation in the review note.";
    if (evidenceHelp.textContent !== help) evidenceHelp.textContent = help;
    for (const passage of form.querySelectorAll('textarea[name="evidence"]')) passage.title = help;
  }
  conditionalFields();
  function values() {
    const out = {};
    for (const [name, value] of new FormData(form)) {
      if (!editable.has(name)) continue;
      (out[name] ||= []).push(String(value));
    }
    return out;
  }
  function stash() {
    try { sessionStorage.setItem(key, JSON.stringify({ rubric: rubric.value, revision: revision.value, values: values() })); } catch {}
  }
  function forget() { try { sessionStorage.removeItem(key); } catch {} }
  // A failed request might have committed. Recovery is deliberate and keeps the
  // current server revision, so an old local draft cannot silently win a conflict.
  try {
    if (new URLSearchParams(location.search).get("resolved_revision") === revision.value) forget();
    const backup = JSON.parse(sessionStorage.getItem(key) || "null");
    if (backup && JSON.stringify(backup.values) !== JSON.stringify(values())) {
      const box = document.createElement("section"); box.className = "card";
      const explanation = document.createElement("p");
      explanation.textContent = "This tab has unsaved changes. Compare them with the saved fields before restoring. Restoring does not commit a judgment.";
      const details = document.createElement("details"), summary = document.createElement("summary"), text = document.createElement("pre");
      summary.textContent = "Unsaved draft text"; text.textContent = JSON.stringify(backup.values, null, 2); details.append(summary, text);
      const restore = document.createElement("button"), discard = document.createElement("button");
      restore.type = discard.type = "button"; restore.className = discard.className = "ghost";
      restore.textContent = "Restore this tab’s draft"; discard.textContent = "Discard local draft";
      restore.addEventListener("click", () => {
        if (backup.rubric !== rubric.value) status.textContent = "The rules changed. Read the current rubric before applying the older draft.";
        const indexes = {};
        for (const el of form.elements) {
          if (!editable.has(el.name)) continue;
          const i = indexes[el.name] || 0; indexes[el.name] = i + 1;
          if (el.type === "checkbox") el.checked = (backup.values[el.name] || []).includes("on");
          else el.value = (backup.values[el.name] || [])[i] || "";
        }
        conditionalFields(); dirty = true; changes++; stash(); box.remove();
        status.textContent = "Local draft restored. Review the fields, then save deliberately.";
      });
      discard.addEventListener("click", () => { forget(); box.remove(); });
      box.append(explanation, details, restore, discard); form.before(box);
    } else if (backup) forget();
  } catch {}
  function saveDraft() {
    pending = pending.then(async () => {
      if (!dirty || submitting || form.hasAttribute("data-resolution")) return;
      const generation = changes, body = new URLSearchParams(new FormData(form));
      status.textContent = "Saving draft…";
      try {
        const response = await fetch(`/api/evaluation/cases/${encodeURIComponent(form.dataset.case)}/draft`, { method: "POST", body, headers: { accept: "application/json" }, credentials: "same-origin" });
        if (!response.ok) {
          if (response.status === 409) throw new Error("Another tab saved this case. Your fields remain here; reload and reconcile before saving.");
          throw new Error("Draft was not confirmed saved. Your fields remain here; retry or keep this tab open.");
        }
        const saved = await response.json(); revision.value = String(saved.revision);
        if (generation === changes) { dirty = false; forget(); } else stash();
        status.textContent = "Draft saved. This is not a committed human judgment.";
      } catch (error) { stash(); status.textContent = error.message; }
    });
    return pending;
  }
  function edited() {
    if (submitting) return;
    conditionalFields(); dirty = true; changes++; stash(); clearTimeout(timer); if (!form.hasAttribute("data-resolution")) timer = setTimeout(saveDraft, 600);
  }
  form.addEventListener("input", edited); form.addEventListener("change", edited);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (submitting) return;
    const action = event.submitter?.value || "judgment";
    clearTimeout(timer); submitting = true; stash(); await pending;
    const intent = document.createElement("input"); intent.type = "hidden"; intent.name = "action"; intent.value = action;
    form.append(intent); form.submit();
  });
  window.addEventListener("pagehide", () => { if (dirty && !submitting) stash(); });
  if (new URLSearchParams(location.search).has("advanced")) document.querySelector("#case-heading")?.focus();
  if (document.querySelector("[data-eval-error]")) form.querySelector("select,textarea")?.focus();
})();
