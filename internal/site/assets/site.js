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

for (const workflow of document.querySelectorAll('.first-workflow')) {
  const queue = workflow.querySelector('.workflow-queue');
  const code = workflow.querySelector('.workflow-code code');
  const button = workflow.querySelector('.copy-workflow');
  const status = workflow.querySelector('.copy-status');
  const original = code.textContent;
  queue.addEventListener('change', () => {
    code.textContent = original.replace(/^    runs-on: .+$/m, `    runs-on: ${queue.value}`);
    button.textContent = 'Copy workflow';
    status.textContent = '';
  });
  button.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(code.textContent);
      button.textContent = 'Copied';
      status.textContent = 'Workflow copied. Save it in .github/workflows/chickadee.yml.';
    } catch {
      status.textContent = 'Select and copy the workflow above.';
    }
  });
}
