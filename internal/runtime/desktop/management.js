const element = (id) => document.getElementById(id);
const tabs = Array.from(document.querySelectorAll('[data-tab]'));
const panels = {
  overview: element('panel-overview'),
  mcp: element('panel-mcp'),
  logs: element('panel-logs'),
  configuration: element('panel-configuration'),
};
const states = {
  setup_required: '需要首次初始化',
  initializing: '正在初始化',
  starting: '正在启动',
  ready: '服务已就绪',
  stopping: '正在停止',
  stopped: '服务已停止',
  failed: '启动或运行失败',
};
const stateCopy = {
  setup_required: '首次使用，请点击右侧「初始化数据」。完成后点击「启动服务」，再打开论文库。',
  initializing: '正在初始化本地数据，请稍候。',
  starting: '正在启动本地服务，请稍候。',
  ready: '服务已就绪，可以打开论文库。',
  stopping: '正在停止本地服务。',
  stopped: '服务已停止。启动后可打开论文库。',
  failed: '启动或运行失败，请查看错误后重试。',
};
const serviceNames = {
  postgres: 'PostgreSQL',
  redis: 'Redis',
  parser: '解析器',
  api: 'API 服务',
};
const serviceDescriptions = {
  postgres: '论文库与引用数据',
  redis: '异步任务队列',
  parser: 'PDF 识别与元数据提取',
  api: '本地 HTTP 与 MCP 接口',
};
const serviceStates = {
  ready: '运行中',
  starting: '启动中',
  stopped: '已停止',
};
const serviceOrder = ['postgres', 'redis', 'parser', 'api'];
const tabStorageKey = 'paper_manager.management.tab';
const tutorialStorageKey = 'paper_manager.management.tutorial.v1';

let noticeTimer;
let currentSettings;
let settingsDirty = false;
let tutorialOpenedAutomatically = false;
let tutorialReturnFocus;

function openTutorial(automatic = false) {
  const tutorial = element('tutorial');
  if (tutorial.open) return;
  tutorialReturnFocus = automatic ? element('primary-action') : element('open-tutorial');
  tutorial.showModal();
}

function maybeShowTutorial(status) {
  const canExplainSetup = ['setup_required', 'stopped', 'failed'].includes(status.state);
  if (status.initialized || !canExplainSetup || tutorialOpenedAutomatically) return;
  try {
    if (localStorage.getItem(tutorialStorageKey) === 'seen') return;
  } catch {
    // The per-page guard still prevents polling from reopening the tutorial.
  }
  tutorialOpenedAutomatically = true;
  openTutorial(true);
}

element('open-tutorial').addEventListener('click', () => openTutorial());
element('close-tutorial').addEventListener('click', () => element('tutorial').close());
element('tutorial-done').addEventListener('click', () => element('tutorial').close());
element('tutorial-configuration').addEventListener('click', () => {
  tutorialReturnFocus = element('tab-configuration');
  element('tutorial').close();
  activateTab('configuration');
  element('tab-configuration').focus();
});
element('tutorial').addEventListener('close', () => {
  try {
    localStorage.setItem(tutorialStorageKey, 'seen');
  } catch {
    // Help remains available even when browser storage is disabled.
  }
  tutorialReturnFocus?.focus();
});

function setNotice(message) {
  clearTimeout(noticeTimer);
  const notice = element('notice');
  notice.textContent = message;
  if (message) {
    noticeTimer = setTimeout(() => {
      notice.textContent = '';
    }, 5000);
  }
}

function showError(error) {
  element('error').textContent = error?.message || String(error);
}

function clearError() {
  element('error').textContent = '';
}

async function request(path, method = 'GET', body = null) {
  const response = await fetch(path, {
    method,
    cache: 'no-store',
    ...(body === null ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
  });
  if (!response.ok) throw new Error(await response.text());
  return response.status === 202 ? null : response.json();
}

function activateTab(name, { store = true } = {}) {
  if (!panels[name]) name = 'overview';
  tabs.forEach((tab) => {
    const active = tab.dataset.tab === name;
    tab.classList.toggle('active', active);
    tab.setAttribute('aria-selected', String(active));
    tab.tabIndex = active ? 0 : -1;
  });
  Object.entries(panels).forEach(([key, panel]) => {
    panel.hidden = key !== name;
  });
  if (store) {
    try {
      localStorage.setItem(tabStorageKey, name);
    } catch {
      // Local storage can be unavailable in hardened browsers; tabs still work.
    }
  }
  if (name === 'overview') loadActivityLog();
  if (name === 'configuration' && !settingsDirty) loadConfiguration();
}

function initialTab() {
  const requested = new URLSearchParams(window.location.search).get('tab');
  if (panels[requested]) return requested;
  try {
    const name = localStorage.getItem(tabStorageKey);
    return panels[name] ? name : 'overview';
  } catch {
    return 'overview';
  }
}

function orderedServiceEntries(services) {
  return Object.entries(services || {}).sort(([left], [right]) => {
    const leftIndex = serviceOrder.indexOf(left);
    const rightIndex = serviceOrder.indexOf(right);
    return (leftIndex === -1 ? serviceOrder.length : leftIndex)
      - (rightIndex === -1 ? serviceOrder.length : rightIndex);
  });
}

function serviceRow(name, state) {
  const row = document.createElement('tr');
  row.dataset.state = state;

  const nameCell = document.createElement('td');
  nameCell.className = 'service-name-cell';
  nameCell.textContent = serviceNames[name] || name;

  const descriptionCell = document.createElement('td');
  descriptionCell.className = 'service-desc-cell';
  descriptionCell.textContent = serviceDescriptions[name] || '';

  const stateCell = document.createElement('td');
  stateCell.className = 'service-state-cell';
  const dot = document.createElement('span');
  dot.className = 'status-dot';
  dot.setAttribute('aria-hidden', 'true');
  const label = document.createElement('span');
  label.textContent = serviceStates[state] || state;
  stateCell.append(dot, label);

  row.append(nameCell, descriptionCell, stateCell);
  return row;
}

function serviceActionVisibility(status) {
  const canStart = status.initialized && ['stopped', 'failed'].includes(status.state);
  const canInitialize = !status.initialized
    && ['setup_required', 'stopped', 'failed'].includes(status.state);
  const canStop = ['ready', 'starting', 'initializing', 'failed'].includes(status.state);
  return { canStart, canInitialize, canStop };
}

function syncActions(status) {
  const { canStart, canInitialize, canStop } = serviceActionVisibility(status);
  const open = element('open');
  const primary = element('primary-action');

  open.hidden = status.state !== 'ready';
  let primaryAction = null;
  if (canStart) {
    primaryAction = { action: 'start', label: status.state === 'failed' ? '重试启动' : '启动服务' };
  } else if (canInitialize) {
    primaryAction = { action: 'initialize', label: '初始化数据' };
  }
  if (primaryAction) {
    primary.hidden = false;
    primary.dataset.action = primaryAction.action;
    primary.textContent = primaryAction.label;
  } else {
    primary.hidden = true;
  }
  element('stop-action').hidden = !canStop;

  element('info-state-row').dataset.state = status.state;
  element('info-state').textContent = states[status.state] || status.state;
  element('app-link').href = status.app_url;
  element('app-link').textContent = status.app_url;
  element('info-mcp').textContent = status.mcp_url;
}

function hideServiceActions() {
  element('open').hidden = true;
  element('primary-action').hidden = true;
  element('stop-action').hidden = true;
}

function syncMcpPreview(url) {
  element('mcp-preview').textContent = JSON.stringify({
    mcpServers: {
      'papers-manager': {
        type: 'http',
        url,
        headers: { Authorization: 'Bearer <访问令牌>' },
      },
    },
  }, null, 2);
}

async function refresh() {
  try {
    const status = await request('/status');
    const state = element('state');
    state.textContent = states[status.state] || status.state;
    state.dataset.state = status.state;
    element('state-copy').textContent = stateCopy[status.state] || '';
    if (status.progress_stage) {
      const elapsed = Math.max(0, Math.floor((Date.now() - Date.parse(status.progress_started_at)) / 1000));
      element('state-copy').textContent = `${status.progress_stage}；已等待 ${Number.isFinite(elapsed) ? elapsed : 0} 秒。`;
    }
    if (!panels.overview.hidden) void loadActivityLog();

    if (status.error && status.state !== 'setup_required') {
      showError(status.error);
    } else {
      clearError();
    }

    const entries = orderedServiceEntries(status.services);
    if (entries.length) {
      element('services').replaceChildren(
        ...entries.map(([name, value]) => serviceRow(name, value)),
      );
    } else {
      const row = document.createElement('tr');
      row.className = 'empty-row';
      const cell = document.createElement('td');
      cell.colSpan = 3;
      cell.textContent = '尚未启动服务';
      row.append(cell);
      element('services').replaceChildren(row);
    }

    element('open').href = status.app_url;
    element('mcp-address').textContent = status.mcp_url;
    syncMcpPreview(status.mcp_url);
    syncActions(status);
    setConfigurationEditable(status.can_configure);
    maybeShowTutorial(status);
  } catch (error) {
    const state = element('state');
    state.textContent = '无法读取状态';
    state.dataset.state = 'failed';
    element('state-copy').textContent = '无法连接本地服务，请确认 Launcher 仍在运行。';
    const infoRow = element('info-state-row');
    infoRow.dataset.state = 'failed';
    element('info-state').textContent = '无法读取状态';
    hideServiceActions();
    showError(error);
    setConfigurationEditable(false);
  }
}

async function loadActivityLog() {
  try {
    const result = await request('/logs?service=launcher');
    const text = (result.text || '').trimEnd();
    const lines = text ? text.split('\n').slice(-24) : [];
    element('activity-log').textContent = lines.length ? lines.join('\n') : '（暂无日志）';
  } catch {
    element('activity-log').textContent = '（暂无日志）';
  }
}

async function loadLogs(path) {
  try {
    const result = await request(path);
    element('logs').textContent = result.text || '（日志为空）';
    clearError();
  } catch (error) {
    showError(error);
  }
}

tabs.forEach((tab, index) => {
  tab.addEventListener('click', () => activateTab(tab.dataset.tab));
  tab.addEventListener('keydown', (event) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    let next = index;
    if (event.key === 'ArrowLeft') next = (index - 1 + tabs.length) % tabs.length;
    if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
    if (event.key === 'Home') next = 0;
    if (event.key === 'End') next = tabs.length - 1;
    activateTab(tabs[next].dataset.tab);
    tabs[next].focus();
  });
});

document.querySelectorAll('[data-action]').forEach((button) => {
  button.addEventListener('click', async () => {
    button.disabled = true;
    try {
      await request(`/${button.dataset.action}`, 'POST');
      clearError();
      setNotice('');
      await refresh();
    } catch (error) {
      showError(error);
    } finally {
      button.disabled = false;
    }
  });
});

element('refresh-status').addEventListener('click', refresh);

element('refresh-log').addEventListener('click', () => {
  const service = element('log-service').value;
  loadLogs(`/logs?service=${encodeURIComponent(service)}`);
});

element('initialization-log').addEventListener('click', () => {
  loadLogs('/initialization-log');
});

element('open-logs').addEventListener('click', () => {
  activateTab('logs');
  element('log-service').value = 'launcher';
  loadLogs('/logs?service=launcher');
});

element('copy-mcp').addEventListener('click', async () => {
  try {
    const config = await request('/mcp-config');
    await navigator.clipboard.writeText(JSON.stringify(config, null, 2));
    clearError();
    setNotice('MCP 配置已复制（包含访问令牌）。');
  } catch (error) {
    setNotice('');
    showError(error);
  }
});

function setConfigurationEditable(editable) {
  element('configuration-fields').disabled = !editable || !currentSettings;
  element('configuration-open').disabled = !editable;
  element('configuration-reload').disabled = !editable;
  element('configuration-state').textContent = editable
    ? 'Launcher 保持运行。保存后，下次初始化 / 启动使用新配置。'
    : '业务服务正在运行或处理操作。先停止服务，再修改配置。';
}

async function loadConfiguration() {
  try {
    const settings = await request('/configuration');
    currentSettings = settings;
    const form = element('configuration-form');
    for (const [name, value] of Object.entries(settings.ports)) {
      if (name !== 'manager') form.elements.namedItem(name).value = value;
    }
    for (const [name, value] of Object.entries(settings.concurrency)) {
      form.elements.namedItem(name).value = value;
    }
    form.elements.namedItem('extraction_engine').value = settings.extraction_engine;
    element('configuration-manager').textContent = settings.ports.manager;
    element('configuration-path').textContent = settings.file;
    settingsDirty = false;
    setConfigurationEditable(settings.editable);
  } catch (error) {
    element('configuration-message').textContent = error.message;
  }
}

element('configuration-form').addEventListener('input', () => { settingsDirty = true; });
element('configuration-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (!currentSettings) return;
  const form = event.currentTarget;
  const ports = { manager: currentSettings.ports.manager };
  const concurrency = {};
  for (const name of ['api', 'mcp', 'parser', 'postgres', 'redis']) {
    ports[name] = Number(form.elements.namedItem(name).value);
  }
  for (const name of ['tasks', 'recognition', 'extraction', 'render', 'rpcs']) {
    concurrency[name] = Number(form.elements.namedItem(name).value);
  }
  try {
    await request('/configuration', 'PUT', {
      ports, concurrency, extraction_engine: form.elements.namedItem('extraction_engine').value,
    });
    settingsDirty = false;
    await loadConfiguration();
    element('configuration-message').textContent = '已保存，下次初始化 / 启动生效。';
    await refresh();
  } catch (error) {
    element('configuration-message').textContent = error.message;
  }
});

element('configuration-open').addEventListener('click', async () => {
  try {
    await request('/configuration/open', 'POST');
    element('configuration-message').textContent = '已请求打开本机编辑器；未打开时，请手动打开上方文件路径。';
  } catch (error) {
    element('configuration-message').textContent = error.message;
  }
});

element('configuration-reload').addEventListener('click', async () => {
  try {
    await request('/configuration/reload', 'POST');
    settingsDirty = false;
    await loadConfiguration();
    element('configuration-message').textContent = '已读取 JSON，下一次初始化 / 启动使用新配置。';
    await refresh();
  } catch (error) {
    element('configuration-message').textContent = error.message;
  }
});

activateTab(initialTab(), { store: false });
refresh();
setInterval(refresh, 1500);
