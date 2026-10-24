const element = (id) => document.getElementById(id);
const states = {
  setup_required: '需要首次配置',
  initializing: '正在初始化',
  starting: '正在启动',
  ready: '服务已就绪',
  stopping: '正在停止',
  stopped: '服务已停止',
  failed: '启动或运行失败',
};

async function request(path, method = 'GET') {
  const response = await fetch(path, {
    method,
    cache: 'no-store',
  });
  if (!response.ok) throw new Error(await response.text());
  return response.status === 202 ? null : response.json();
}

function showError(error) {
  element('error').textContent = error.message;
}

async function refresh() {
  try {
    const status = await request('/status');
    element('state').textContent = states[status.state] || status.state;
    element('error').textContent = status.error || '';
    element('services').replaceChildren(...Object.entries(status.services).map(
      ([name, state]) => {
        const item = document.createElement('li');
        item.textContent = `${name} · ${state}`;
        return item;
      },
    ));
    element('open').hidden = status.state !== 'ready';
    element('open').href = status.app_url;
    element('mcp-address').textContent = status.mcp_url;
    document.querySelector('[data-action="start"]').disabled =
      !status.initialized || !['stopped', 'failed'].includes(status.state);
    document.querySelector('[data-action="initialize"]').hidden = status.initialized;
    document.querySelector('[data-action="initialize"]').disabled =
      !['setup_required', 'stopped', 'failed'].includes(status.state);
  } catch (error) {
    showError(error);
  }
}

document.querySelectorAll('[data-action]').forEach((button) => {
  button.addEventListener('click', async () => {
    try {
      await request(`/${button.dataset.action}`, 'POST');
      await refresh();
    } catch (error) {
      showError(error);
    }
  });
});

element('refresh-log').addEventListener('click', async () => {
  try {
    const service = element('log-service').value;
    const result = await request(`/logs?service=${encodeURIComponent(service)}`);
    element('logs').textContent = result.text;
  } catch (error) {
    showError(error);
  }
});

element('copy-mcp').addEventListener('click', async () => {
  try {
    const config = await request('/mcp-config');
    await navigator.clipboard.writeText(JSON.stringify(config, null, 2));
    element('error').textContent = 'MCP 配置已复制（包含访问令牌）。';
  } catch (error) {
    showError(error);
  }
});

setInterval(refresh, 1500);
refresh();

element('initialization-log').addEventListener('click', async () => {
  try {
    const result = await request('/initialization-log');
    element('logs').textContent = result.text;
  } catch (error) {
    showError(error);
  }
});
