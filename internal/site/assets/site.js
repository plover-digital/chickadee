'use strict';
const size = document.getElementById('size');
const os = document.getElementById('os');
const label = document.getElementById('label');
const copy = document.getElementById('copy');
if (size && os && label && copy) {
  const update = () => { label.textContent = `runs-on: chickadee-${size.value}-${os.value}`; };
  size.addEventListener('change', update);
  os.addEventListener('change', update);
  copy.addEventListener('click', async () => {
    try { await navigator.clipboard.writeText(label.textContent.replace('runs-on: ', '')); document.getElementById('copied').textContent = 'Runner label copied'; copy.textContent = 'Copied'; }
    catch { document.getElementById('copied').textContent = 'Copy the label from the code below'; }
  });
}
