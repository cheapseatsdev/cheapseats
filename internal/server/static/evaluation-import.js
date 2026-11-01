(() => {
  const form = document.querySelector('[data-evaluation-import]');
  if (!form) return;
  const all = form.querySelector('[data-import-all]');
  const boxes = [...form.querySelectorAll('input[name="result"]')];
  const count = form.querySelector('[data-import-count]');
  form.querySelector('[data-import-selection]').hidden = false;
  const update = () => {
    const n = boxes.filter(b => b.checked).length;
    count.textContent = `${n} selected`;
    all.checked = boxes.length > 0 && n === boxes.length;
    all.indeterminate = n > 0 && n < boxes.length;
  };
  all.addEventListener('change', () => {
    boxes.forEach(b => { b.checked = all.checked; });
    update();
  });
  boxes.forEach(b => b.addEventListener('change', update));
  update();
})();
