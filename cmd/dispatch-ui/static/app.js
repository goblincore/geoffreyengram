// State
let tasks = [];
let selectedTask = null;
let eventSource = null;
let config = null;
let currentView = null; // tracks what the detail panel is showing: 'logs', 'plan', 'report', or null

// Init
async function init() {
  config = await (await fetch('/api/config')).json();
  populateDropdowns();
  document.getElementById('btn-new-task').onclick = openModal;
  await refreshTasks();
  setInterval(refreshTasks, 3000);
}

// Fetch and render task list
async function refreshTasks() {
  tasks = await (await fetch('/api/tasks')).json();
  document.getElementById('task-count').textContent = tasks.length;
  renderTaskList();
  if (selectedTask) {
    const updated = tasks.find(t => t.name === selectedTask);
    if (updated) {
      // Only update header/meta/actions — don't clobber the body if user is viewing plan/report
      updateDetailHeader(updated);
    }
  }
}

// Render sidebar task list
function renderTaskList() {
  const list = document.getElementById('task-list');
  list.innerHTML = tasks.map(t => `
    <div class="task-item ${t.name === selectedTask ? 'selected' : ''} status-${t.status}"
         onclick="selectTask('${t.name}')">
      <div class="task-dot ${t.status}"></div>
      <div class="task-info">
        <div class="task-name">${escapeHtml(t.title || t.name)}</div>
        <div class="task-meta">${escapeHtml(t.model || '')} · ${taskMetaText(t)}</div>
      </div>
    </div>
  `).join('');
}

function taskMetaText(t) {
  if (t.status === 'running')   return timeAgo(t.started_at);
  if (t.status === 'pending')   return `priority ${t.priority} · waiting`;
  if (t.status === 'done')      return `done · exit ${t.exit_code}`;
  if (t.status === 'failed')    return `failed · exit ${t.exit_code}`;
  return t.status;
}

function timeAgo(iso) {
  if (!iso) return '';
  const secs = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (secs < 60)   return `${secs}s ago`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  return `${Math.floor(secs / 3600)}h ago`;
}

// Select a task — show detail panel
function selectTask(name) {
  selectedTask = name;
  const task = tasks.find(t => t.name === name);
  if (task) renderDetail(task);
  renderTaskList();
}

// Update just the header/meta/actions without touching the body
function updateDetailHeader(task) {
  document.getElementById('detail-empty').hidden = true;
  document.getElementById('detail-content').hidden = false;
  document.getElementById('detail-title').textContent = task.title || task.name;

  document.getElementById('detail-meta').innerHTML = `
    <span class="status-badge ${task.status}">${task.status}</span>
    ${escapeHtml(task.model || '')}
    ${task.branch ? '· ' + escapeHtml(task.branch) : ''}
    ${task.started_at ? '· ' + timeAgo(task.started_at) : ''}
  `;

  renderActionButtons(task);
}

function renderActionButtons(task) {
  const actions = document.getElementById('detail-actions');
  let btns = `<button class="btn-sm" onclick="viewPlan('${task.name}')">View Plan</button>`;
  if (task.status === 'running') {
    btns += `<button class="btn-sm btn-danger" onclick="cancelTask('${task.name}')">Cancel</button>`;
  }
  if (task.status === 'pending') {
    btns += `<button class="btn-sm btn-primary" onclick="runTask('${task.name}')">Run Now</button>`;
  }
  if (task.status === 'failed' || task.status === 'cancelled') {
    btns += `<button class="btn-sm" onclick="retryTask('${task.name}')">Retry</button>`;
  }
  if (task.status === 'done' || task.status === 'failed') {
    btns += `<button class="btn-sm" onclick="viewReport('${task.name}')">View Report</button>`;
  }
  btns += `<button class="btn-sm btn-danger" onclick="deleteTask('${task.name}')">Delete</button>`;
  actions.innerHTML = btns;
}

// Render full detail panel (header + body) — called on task selection
function renderDetail(task) {
  updateDetailHeader(task);

  const body = document.getElementById('detail-body');
  if (task.status === 'running') {
    currentView = 'logs';
    body.innerHTML = '<div class="log-viewer" id="log-output"></div>';
    connectLogs(task.name);
  } else if (task.status === 'done' || task.status === 'failed') {
    currentView = 'report';
    fetch(`/api/tasks/${task.name}/report`).then(r => r.text()).then(text => {
      body.innerHTML = `<div class="log-viewer"><pre>${escapeHtml(text)}</pre></div>`;
    }).catch(() => {
      body.innerHTML = '<p class="muted">No report available</p>';
    });
  } else {
    currentView = 'plan';
    fetch(`/api/tasks/${task.name}/plan`).then(r => r.text()).then(text => {
      body.innerHTML = `<div class="plan-viewer"><pre>${escapeHtml(text)}</pre></div>`;
    });
  }
}

// SSE log streaming
function connectLogs(name) {
  if (eventSource) eventSource.close();
  const output = document.getElementById('log-output');
  if (!output) return;

  eventSource = new EventSource(`/api/tasks/${name}/logs`);
  eventSource.onmessage = (e) => {
    const line = document.createElement('div');
    line.className = 'log-line';
    line.textContent = e.data;
    output.appendChild(line);
    if (output.scrollHeight - output.scrollTop - output.clientHeight < 100) {
      output.scrollTop = output.scrollHeight;
    }
  };
  eventSource.addEventListener('done', () => {
    eventSource.close();
    eventSource = null;
    refreshTasks();
  });
  eventSource.onerror = () => {
    eventSource.close();
    eventSource = null;
  };
}

// Task actions
async function cancelTask(name) {
  await fetch(`/api/tasks/${name}/cancel`, { method: 'POST' });
  refreshTasks();
}

async function retryTask(name) {
  await fetch(`/api/tasks/${name}/retry`, { method: 'POST' });
  refreshTasks();
}

async function runTask(name) {
  await fetch(`/api/tasks/${name}/run`, { method: 'POST' });
  refreshTasks();
}

async function deleteTask(name) {
  if (!confirm(`Delete task "${name}"?`)) return;
  await fetch(`/api/tasks/${name}`, { method: 'DELETE' });
  selectedTask = null;
  document.getElementById('detail-empty').hidden = false;
  document.getElementById('detail-content').hidden = true;
  refreshTasks();
}

async function viewPlan(name) {
  if (eventSource) { eventSource.close(); eventSource = null; }
  currentView = 'plan';
  const text = await (await fetch(`/api/tasks/${name}/plan`)).text();
  document.getElementById('detail-body').innerHTML = `<div class="plan-viewer"><pre>${escapeHtml(text)}</pre></div>`;
}

async function viewReport(name) {
  if (eventSource) { eventSource.close(); eventSource = null; }
  currentView = 'report';
  const text = await (await fetch(`/api/tasks/${name}/report`)).text();
  document.getElementById('detail-body').innerHTML = `<div class="log-viewer"><pre>${escapeHtml(text)}</pre></div>`;
}

// Modal
function openModal() {
  document.getElementById('modal-overlay').hidden = false;
  document.getElementById('step-form').hidden = false;
  document.getElementById('step-review').hidden = true;
}

function closeModal() {
  document.getElementById('modal-overlay').hidden = true;
}

function backToForm() {
  document.getElementById('step-form').hidden = false;
  document.getElementById('step-review').hidden = true;
}

function populateDropdowns() {
  const projSelect = document.getElementById('inp-project');
  (config.projects || []).forEach(p => {
    const opt = document.createElement('option');
    opt.value = p;
    opt.textContent = p.split('/').pop();
    projSelect.appendChild(opt);
  });
  const modelSelect = document.getElementById('inp-model');
  (config.models || []).forEach(m => {
    const opt = document.createElement('option');
    opt.value = m;
    opt.textContent = m;
    modelSelect.appendChild(opt);
  });
}

async function generatePlan() {
  const desc = document.getElementById('inp-desc').value;
  const project = document.getElementById('inp-project').value;
  if (!desc || !project) { alert('Description and project are required'); return; }

  document.getElementById('step-form').hidden = true;
  document.getElementById('step-review').hidden = false;
  document.getElementById('gen-status').textContent = 'generating...';
  document.getElementById('btn-dispatch').disabled = true;

  try {
    const res = await fetch('/api/generate-plan', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ description: desc, project })
    });
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    document.getElementById('inp-plan-body').value = data.body;
    document.getElementById('gen-status').textContent = 'ready';
    document.getElementById('btn-dispatch').disabled = false;
  } catch (e) {
    document.getElementById('gen-status').textContent = 'error';
    document.getElementById('inp-plan-body').value = 'Plan generation failed: ' + e.message + '\n\nYou can write the plan manually here.';
    document.getElementById('btn-dispatch').disabled = false;
  }
}

async function dispatchTask() {
  const name = document.getElementById('inp-name').value.trim().replace(/\s+/g, '-').toLowerCase();
  const title = document.getElementById('inp-desc').value.split('\n')[0];
  if (!name) { alert('Task name is required'); return; }

  const body = document.getElementById('inp-plan-body').value;
  const res = await fetch('/api/tasks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      name,
      title,
      project: document.getElementById('inp-project').value,
      model: document.getElementById('inp-model').value,
      priority: parseInt(document.getElementById('inp-priority').value),
      max_runtime: document.getElementById('inp-runtime').value,
      body
    })
  });
  if (res.ok) {
    closeModal();
    refreshTasks();
    selectTask(name);
  } else {
    alert('Failed to create task: ' + await res.text());
  }
}

function escapeHtml(text) {
  const div = document.createElement('div');
  div.textContent = text;
  return div.innerHTML;
}

// Start
init();
