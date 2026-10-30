(function () {
  const progress = document.querySelector('[data-check-batch-progress]');
  if (progress) {
    const status = progress.querySelector('[data-batch-status]');
    const bar = progress.querySelector('[role=progressbar]');
    const retry = progress.querySelector('[data-progress-retry]');
    let failures = 0;
    async function poll() {
      try {
        const response = await fetch(progress.dataset.progress, { credentials: 'same-origin', cache: 'no-store' });
        if (!response.ok) throw new Error('Progress update failed.');
        const data = await response.json();
        failures = 0; retry.hidden = true;
        status.textContent = `${data.done} / ${data.total} assessments saved · ${data.failed} failed.`;
        bar.setAttribute('aria-valuenow', String(Math.round(data.percent)));
        bar.querySelector('i').style.width = `${data.percent}%`;
        if (data.finished) { location.replace(progress.dataset.results); return; }
        setTimeout(poll, 1000);
      } catch {
        failures++;
        status.textContent = 'Progress updates are unavailable. The batch may still be running; saved results are retained.';
        if (failures < 3) setTimeout(poll, 1500); else retry.hidden = false;
      }
    }
    retry.addEventListener('click', () => { failures = 0; retry.hidden = true; poll(); });
    poll();
  }
  const form = document.querySelector('[data-batch-keep]');
  const all = document.querySelector('#batch-select-all');
  if (form && all) {
    const boxes = [...form.querySelectorAll('input[name=case]')];
    function changed() {
      const count = boxes.filter(box => box.checked).length;
      form.querySelector('[data-selected-count]').textContent = `${count} selected`;
      all.checked = count === boxes.length && count > 0;
      all.indeterminate = count > 0 && count < boxes.length;
    }
    all.addEventListener('change', () => { boxes.forEach(box => { box.checked = all.checked; }); changed(); });
    boxes.forEach(box => box.addEventListener('change', changed));
  }
})();
