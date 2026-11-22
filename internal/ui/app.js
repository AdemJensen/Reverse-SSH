'use strict';

const byId = (id) => document.getElementById(id);
const form = byId('profileForm');
const field = (name) => form.elements.namedItem(name);
const appTabs = byId('appTabs');
const tabs = Array.from(appTabs.querySelectorAll('[role="tab"]'));
const sshServerFields = ['host', 'sshPort', 'user'];
const profileFields = [...sshServerFields, 'auth', 'keyPath', 'reconnect', 'autoConnect', 'rememberPassword'];
const forwardFields = ['remoteBind', 'remotePort', 'localHost', 'localPort'];
const defaultForward = { remoteBind: '127.0.0.1', remotePort: 8080, localHost: '127.0.0.1', localPort: 3000 };
const forwardLimit = 64;
const forwardList = byId('forwardList');
const routeList = byId('routeList');
const detachedTests = byId('detachedTests');
const routeContexts = new WeakMap();
const ruleChecks = new WeakMap();
const pendingTests = new Map();
const forwardRows = () => Array.from(forwardList.children);
const forwardField = (row, name) => row.querySelector(`[data-forward-field="${name}"]`);
const statusLabels = {
  stopped: '未连接',
  connecting: '连接中',
  connected: '已连接',
  reconnecting: '正在重试',
  hostkey: '等待验证主机',
  error: '连接失败',
};
const activeStates = new Set(['connecting', 'connected', 'reconnecting', 'hostkey']);
const statusMessages = {
  stopped: '连接后可从远端访问本地服务。',
  connecting: '正在与 SSH 服务器建立连接。',
  connected: '隧道正在转发。',
  reconnecting: '连接已中断，正在自动重试。',
  hostkey: '请核对 SSH 主机指纹。',
  error: '请检查连接配置后重试。',
};

let state;
let edited = false;
let action = '';
let startupPending = false;
let keyPickerPending = false;
let routeActionPending = false;
let refreshing = false;
let stateVersion = 0;
let currentTrust;
let previousLogs = '';
let startupNotice = '';
let previousRoutes = '';

function selectTab(tab, moveFocus = false) {
  const changed = tab.getAttribute('aria-selected') !== 'true';
  for (const item of tabs) {
    const selected = item === tab;
    item.setAttribute('aria-selected', String(selected));
    item.tabIndex = selected ? 0 : -1;
    byId(item.getAttribute('aria-controls')).hidden = !selected;
  }
  if (changed) window.scrollTo(0, 0);
  if (moveFocus) tab.focus({ preventScroll: true });
}

appTabs.addEventListener('click', (event) => {
  const tab = event.target.closest('[role="tab"]');
  if (tab) selectTab(tab);
});

appTabs.addEventListener('keydown', (event) => {
  const index = tabs.indexOf(event.target);
  if (index === -1) return;
  let next;
  switch (event.key) {
    case 'ArrowLeft': next = (index + tabs.length - 1) % tabs.length; break;
    case 'ArrowRight': next = (index + 1) % tabs.length; break;
    case 'Home': next = 0; break;
    case 'End': next = tabs.length - 1; break;
    default: return;
  }
  event.preventDefault();
  selectTab(tabs[next], true);
});

async function api(path, method = 'GET', data, signal) {
  const options = { method, signal, cache: 'no-store', mode: 'same-origin' };
  if (method !== 'GET') {
    options.headers = { 'Content-Type': 'application/json', 'X-Reverse-SSH': 'desktop' };
    options.body = JSON.stringify(data ?? {});
  }
  const response = await fetch(`./api/${path}`, options);
  const json = response.headers.get('Content-Type')?.includes('application/json') ? await response.json() : undefined;
  if (!response.ok) throw new Error(json?.error || `应用请求失败（HTTP ${response.status}）`);
  if (!json) throw new Error('应用返回了无法读取的响应。');
  return json;
}

function announce(target, message = '', error = false) {
  const element = typeof target === 'string' ? byId(target) : target;
  if (element.textContent !== message) element.textContent = message;
  element.hidden = !message;
  element.dataset.error = String(error);
}

const aboutActions = byId('aboutActions');
const aboutButtons = Array.from(aboutActions.querySelectorAll('[data-about-page]'));
aboutActions.addEventListener('click', async (event) => {
  const button = event.target.closest('[data-about-page]');
  if (!button || button.disabled) return;
  const restoreFocus = document.activeElement === button;
  for (const item of aboutButtons) item.disabled = true;
  announce('aboutMessage');
  try {
    await api(`about/${button.dataset.aboutPage}`, 'POST', {});
  } catch (error) {
    announce('aboutMessage', error.message, true);
  } finally {
    for (const item of aboutButtons) item.disabled = false;
    if (restoreFocus && document.hasFocus() && document.activeElement === document.body) {
      button.focus({ preventScroll: true });
    }
  }
});

function readInput(input) {
  if (input.type === 'checkbox') return input.checked;
  if (input.type === 'number') return Number.isFinite(input.valueAsNumber) ? input.valueAsNumber : 0;
  return input.value.trim();
}

function readForward(row) {
  return Object.fromEntries(forwardFields.map((name) => [name, readInput(forwardField(row, name))]));
}

function readSSHServer() {
  return Object.fromEntries(sshServerFields.map((name) => [name, readInput(field(name))]));
}

function sshIdentity({ host, sshPort, user }) {
  const address = host.trim();
  const normalized = normalizeIPv6(address) || address.toLowerCase();
  return JSON.stringify([normalized, sshPort, user.trim()]);
}

function matchesSavedSSHServer(server) {
  const saved = state?.profile;
  return saved?.auth === 'password' && sshIdentity(server) === sshIdentity(saved);
}

function hasSavedPassword(server) {
  return Boolean(state?.hasSavedPassword) && !state.savedPasswordUnavailable && matchesSavedSSHServer(server);
}

function passwordAvailable(server, remembering) {
  return remembering && (field('password').value !== '' || hasSavedPassword(server));
}

function readProfile() {
  const profile = Object.fromEntries(profileFields.map((name) => [name, readInput(field(name))]));
  profile.rememberPassword = profile.auth === 'password' && profile.rememberPassword;
  if (profile.auth === 'password' && !passwordAvailable(profile, profile.rememberPassword)) profile.autoConnect = false;
  return { ...profile, forwards: forwardRows().map(readForward) };
}

function createForward(forward) {
  const row = byId('forwardTemplate').content.firstElementChild.cloneNode(true);
  for (const name of forwardFields) forwardField(row, name).value = forward[name] ?? '';
  return row;
}

function updateForwardRowLabels() {
  const rows = forwardRows();
  const single = rows.length === 1;
  rows.forEach((row, index) => {
    const name = single ? '端口转发' : `转发 ${index + 1}`;
    row.setAttribute('aria-label', name);
    row.querySelector('.forward-entry-head').hidden = single;
    row.querySelector('.forward-name').textContent = name;
    row.querySelector('[data-remove-forward]').setAttribute('aria-label', `移除${name}`);
  });
  const limited = rows.length >= forwardLimit;
  byId('forwardLimit').hidden = !limited;
}

function loadProfile(profile) {
  if (!profile) return;
  for (const name of profileFields) {
    if (profile[name] === undefined) continue;
    const input = field(name);
    if (input.type === 'checkbox') input.checked = Boolean(profile[name]);
    else if (input.value !== String(profile[name])) input.value = profile[name];
  }

  const forwards = profile.forwards?.length ? profile.forwards : [defaultForward];
  const rows = forwardRows();
  const current = rows.map(readForward);
  if (current.length === forwards.length && current.every((forward, index) =>
    forwardFields.every((name) => forward[name] === forwards[index][name]))) return;
  rows.forEach(discardRuleCheck);
  forwardList.replaceChildren(...forwards.map(createForward));
  updateForwardRowLabels();
}

function endpoint(host, port) {
  if (!host || !port) return '—';
  const address = host.includes(':') && !host.startsWith('[') ? `[${host}]` : host;
  return `${address}:${port}`;
}

function normalizeIPv6(host) {
  const address = host.replace(/^\[(.*)\]$/, '$1');
  if (!address.includes(':')) return undefined;
  const zoneIndex = address.indexOf('%');
  const ip = zoneIndex === -1 ? address : address.slice(0, zoneIndex);
  const zone = zoneIndex === -1 ? '' : address.slice(zoneIndex);
  try {
    return new URL(`http://[${ip}]/`).hostname.slice(1, -1) + zone;
  } catch (_) {
    return undefined;
  }
}

function testKey(host, port) {
  const raw = host.trim().replace(/^\[(.*)\]$/, '$1');
  let address = normalizeIPv6(raw) || raw.toLowerCase();
  if (['', 'localhost', 'localhost.', '0.0.0.0', '0.0.0.0.'].includes(address)) address = '127.0.0.1';
  if (address === '::') address = '::1';
  return endpoint(address, port);
}

function validEndpoint(host, port) {
  return Boolean(host) && Number.isInteger(port) && port >= 1 && port <= 65535;
}

function checkTarget(row) {
  const payload = { ...readForward(row), sshHost: readInput(field('host')) };
  const key = JSON.stringify([payload, readInput(field('sshPort')), readInput(field('user'))]);
  return { payload, key };
}

function discardRuleCheck(row) {
  ruleChecks.get(row)?.controller.abort();
  ruleChecks.delete(row);
}

function renderCheckLeg(leg, result) {
  const status = Number.isInteger(result.status) && result.status > 0 ? result.status : 0;
  const reason = result.statusText?.trim();
  let summary = result.error || (result.ok ? '连接成功' : '检测失败');
  if (status) {
    const response = reason?.startsWith(`${status} `) ? reason : [status, reason].filter(Boolean).join(' ');
    summary = `HTTP ${response}`;
  }
  leg.dataset.ok = String(result.ok);
  const value = leg.querySelector('[data-check-value]');
  if (value.textContent !== summary) value.textContent = summary;
  const latency = Number.isFinite(result.latencyMs) && result.latencyMs >= 0 ? `${result.latencyMs} ms` : '';
  announce(leg.querySelector('[data-check-latency]'), latency);
  const path = leg.querySelector('[data-check-via]');
  if (path) path.hidden = !result.viaSSH;
}

function showRuleCheck(row) {
  const { payload, key } = checkTarget(row);
  let check = ruleChecks.get(row);
  if (check && check.key !== key) {
    discardRuleCheck(row);
    check = undefined;
  }

  const button = row.querySelector('[data-check-action]');
  const valid = validEndpoint(payload.localHost, payload.localPort) || validEndpoint(payload.remoteBind, payload.remotePort);
  const description = valid ? '检测本地和远端的 HTTP 连接' : '填写本地或远端地址和端口后可检测连接';
  button.disabled = !state || Boolean(action) || !valid || Boolean(check?.pending);
  const label = check?.pending ? '检测中…' : '检测连接';
  if (button.textContent !== label) button.textContent = label;
  button.title = description;
  const name = row.getAttribute('aria-label');
  button.setAttribute('aria-label', `${label}（${name}）`);
  button.setAttribute('aria-busy', String(Boolean(check?.pending)));

  announce(row.querySelector('[data-check-feedback]'), check?.error, Boolean(check?.error));
  const results = row.querySelector('[data-check-results]');
  results.hidden = !check?.result;
  if (check?.result) {
    renderCheckLeg(results.querySelector('[data-check-side="local"]'), check.result.local);
    renderCheckLeg(results.querySelector('[data-check-side="remote"]'), check.result.remote);
  }
}

function showRoutes(status) {
  const addresses = status.remoteAddresses || [];
  const sshHost = field('host').value.trim();
  const available = Boolean(state) && !action && !routeActionPending && !startupPending && !keyPickerPending;
  const routes = forwardRows().map((row, index) => {
    const forward = readForward(row);
    return {
      ...forward,
      sshHost,
      remote: addresses[index] || endpoint(forward.remoteBind, forward.remotePort),
      local: endpoint(forward.localHost, forward.localPort),
    };
  });
  const key = JSON.stringify([routes, available]);
  if (key === previousRoutes) return;
  previousRoutes = key;

  routes.forEach((route, index) => {
    let item = routeList.children[index];
    if (!item) {
      item = byId('routeTemplate').content.firstElementChild.cloneNode(true);
      routeList.append(item);
    }
    const previous = routeContexts.get(item);
    if (previous && (previous.local !== route.local || previous.remote !== route.remote || previous.sshHost !== sshHost)) {
      announce(item.querySelector('[data-route-feedback]'));
    }
    routeContexts.set(item, route);
    item.querySelector('[data-remote-endpoint]').textContent = route.remote;
    item.querySelector('[data-local-endpoint]').textContent = route.local;

    const remote = item.querySelector('[data-route-action="remote"]');
    const local = item.querySelector('[data-route-action="local"]');
    remote.disabled = !available || !validEndpoint(route.remoteBind, route.remotePort);
    local.disabled = !available || !validEndpoint(route.localHost, route.localPort);
    remote.setAttribute('aria-label', `在浏览器打开远端 HTTP 地址 ${route.remote}`);
    local.setAttribute('aria-label', `在浏览器打开本地 HTTP 地址 ${route.local}`);
  });
  while (routeList.children.length > routes.length) routeList.lastElementChild.remove();
  routeList.tabIndex = routes.length > 1 ? 0 : -1;
  byId('routeCount').textContent = `${routes.length} 条`;
}

function showTestControl(target, host, port, running, available) {
  const key = testKey(host, port);
  if (target.dataset.testKey !== key) announce(target.querySelector('[data-test-feedback]'));
  Object.assign(target.dataset, { testKey: key, testHost: host, testPort: port, testRunning: running });

  const button = target.querySelector('[data-test-action]');
  const pending = pendingTests.get(key);
  const label = running ? '停止 HTTP 测试' : '启动 HTTP 测试';
  const text = pending === 'start' ? '正在启动…' : pending === 'stop' ? '正在停止…' : label;
  const description = validEndpoint(host, port) ? `${label}（${endpoint(host, port)}）` : '填写本地地址和端口后可启动 HTTP 测试';
  button.disabled = !available || !validEndpoint(host, port) || Boolean(pending);
  if (button.textContent !== text) button.textContent = text;
  button.title = description;
  button.setAttribute('aria-label', description);
  button.setAttribute('aria-busy', String(Boolean(pending)));

  const status = target.querySelector('[data-test-status]');
  if (status) announce(status, running ? `端口 ${port} 的 HTTP 测试运行中` : '');
}

function showPortTests() {
  const tests = state?.tests || [];
  const attached = new Set();
  const running = new Set(tests.map((test) => testKey(test.host, test.port)));
  const available = Boolean(state) && !action && !startupPending && !keyPickerPending;
  for (const row of forwardRows()) {
    const { localHost, localPort } = readForward(row);
    const key = testKey(localHost, localPort);
    const valid = validEndpoint(localHost, localPort);
    if (valid) attached.add(key);
    showTestControl(row, localHost, localPort, valid && running.has(key), available);
  }
  showDetachedTests(tests.filter((test) => !attached.has(testKey(test.host, test.port))), available);
}

function showDetachedTests(tests, available) {
  const existing = Array.from(detachedTests.children);
  const byAddress = new Map(existing.map((item) => [item.dataset.address, item]));
  const items = tests.map(({ host, port }) => {
    const address = endpoint(host, port);
    const item = byAddress.get(address) || byId('detachedTestTemplate').content.firstElementChild.cloneNode(true);
    item.dataset.address = address;
    item.querySelector('[data-local-endpoint]').textContent = address;
    showTestControl(item, host, port, true, available);
    return item;
  });
  if (existing.length !== items.length || items.some((item, index) => item !== existing[index])) {
    detachedTests.replaceChildren(...items);
  }
  detachedTests.tabIndex = items.length > 3 ? 0 : -1;
  byId('detachedTestPanel').hidden = !items.length;
}

function showControls() {
  const current = state?.status?.state || 'stopped';
  const active = activeStates.has(current);
  const editable = Boolean(state) && !active && !action && !routeActionPending;
  const auth = field('auth').value;
  const keys = auth === 'key';
  const passwords = auth === 'password';
  const server = readSSHServer();
  const remembering = passwords && field('rememberPassword').checked;
  const savedPassword = passwords && hasSavedPassword(server);
  const savedUnavailable = passwords && Boolean(state?.savedPasswordUnavailable) && matchesSavedSSHServer(server);
  const canAutoConnect = !passwords || passwordAvailable(server, remembering);

  byId('serverSection').disabled = !editable;
  const rows = forwardRows();
  byId('addForward').disabled = !editable || rows.length >= forwardLimit;
  for (const row of rows) {
    for (const input of row.querySelectorAll('[data-forward-field], [data-remove-forward]')) input.disabled = !editable;
    showRuleCheck(row);
  }
  byId('keyFields').hidden = !keys;
  byId('passwordFields').hidden = !passwords;
  field('keyPath').disabled = !keys;
  field('passphrase').disabled = !keys;
  field('password').disabled = !passwords;
  field('rememberPassword').disabled = !passwords;
  field('password').placeholder = savedPassword && remembering ? '可留空，使用已保存的密码' : '';
  let storageMessage = savedPassword && !remembering ? '保存配置后将删除已保存的密码。' : '';
  if (savedUnavailable) {
    storageMessage = remembering ? '已保存的密码无法解密，请重新输入后保存。' : '保存配置后将移除无法解密的密码。';
  }
  announce('passwordStorageStatus', storageMessage, savedUnavailable && remembering);
  byId('pickKey').disabled = !keys || keyPickerPending;
  byId('pickKey').textContent = keyPickerPending ? '选择中' : '浏览';
  byId('reconnect').disabled = !editable;
  byId('autoConnect').disabled = !editable || !canAutoConnect;
  byId('autoConnectNote').textContent = canAutoConnect ? '在后台连接已保存的端口' : '保存 SSH 密码后可在后台连接';
  byId('autoStart').disabled = !state || startupPending || routeActionPending;
  byId('saveButton').disabled = !editable || !edited;
  const fileAvailable = Boolean(state) && !action && !routeActionPending && !keyPickerPending && !startupPending;
  byId('importCommandButton').disabled = !fileAvailable || active;
  byId('importButton').disabled = !fileAvailable || active;
  byId('exportButton').disabled = !fileAvailable;
  byId('importButton').textContent = action === 'import' ? '导入中' : '导入 JSON';
  byId('exportButton').textContent = action === 'export' ? '导出中' : '导出 JSON';

  const connect = byId('connectButton');
  connect.disabled = !state || Boolean(action) || routeActionPending;
  connect.classList.toggle('button-primary', !active);
  connect.classList.toggle('button-stop', active);
  const labels = { connect: '连接中', disconnect: '正在断开' };
  byId('connectText').textContent = labels[action] || (active ? (current === 'connected' ? '断开连接' : '取消连接') : '连接隧道');

  showRoutes(active ? state.status : {});
  showPortTests();
}

function showTrust(hostKey) {
  const dialog = byId('trustDialog');
  if (!hostKey?.fingerprint) {
    if (dialog.open) dialog.close();
    currentTrust = undefined;
    return;
  }

  byId('trustHost').textContent = hostKey.host || field('host').value;
  byId('trustAlgorithm').textContent = hostKey.algorithm || '';
  byId('trustAlgorithmRow').hidden = !hostKey.algorithm;
  byId('trustFingerprint').textContent = hostKey.fingerprint;
  if (currentTrust !== hostKey.fingerprint) announce('trustError');
  currentTrust = hostKey.fingerprint;
  if (!dialog.open) dialog.showModal();
}

function showLogs(logs) {
  const entries = Array.isArray(logs) ? logs : [];
  const key = JSON.stringify(entries);
  if (key === previousLogs) return;
  previousLogs = key;

  const items = entries.slice().reverse().map((log) => {
    const item = document.createElement('li');
    const time = document.createElement('time');
    const message = document.createElement('span');
    const timestamp = new Date(log.time);
    if (log.time && Number.isFinite(timestamp.getTime())) {
      time.dateTime = timestamp.toISOString();
      time.textContent = timestamp.toLocaleTimeString('zh-CN', { hour12: false });
    }
    message.textContent = log.message;
    item.dataset.error = String(log.level === 'error');
    item.append(time, message);
    return item;
  });
  const list = byId('activityList');
  list.replaceChildren(...items);
  list.hidden = items.length === 0;
  byId('activityEmpty').hidden = !list.hidden;
}

function showState(next) {
  state = next;
  if (!edited) loadProfile(state.profile);
  if (!startupPending) {
    byId('autoStart').checked = Boolean(state.autoStart);
    if (!startupNotice) announce('settingsMessage', state.autoStartError, Boolean(state.autoStartError));
  }

  const status = state.status || {};
  const current = status.state || 'stopped';
  const label = statusLabels[current] || statusLabels.stopped;
  const message = status.message || statusMessages[current] || statusMessages.stopped;
  if (byId('statusLabel').textContent !== label) byId('statusLabel').textContent = label;
  if (byId('statusMessage').textContent !== message) byId('statusMessage').textContent = message;
  byId('statusBadge').dataset.state = current;
  byId('routeTitle').closest('.panel').dataset.state = current;
  showControls();
  showTrust(current === 'hostkey' ? status.hostKey : undefined);
  showLogs(state.logs);
}

async function refreshState(force = false) {
  if (!force && (document.hidden || refreshing || action || startupPending || routeActionPending)) return;
  const version = ++stateVersion;
  refreshing = true;
  try {
    const next = await api('state');
    if (version !== stateVersion) return;
    if (!state) announce('formMessage');
    showState(next);
  } catch (_) {
    if (version !== stateVersion) return;
    if (!state) announce('formMessage', '暂时无法读取应用状态，正在重试。', true);
  } finally {
    if (version === stateVersion) refreshing = false;
  }
}

async function handleRouteAction(event) {
  const button = event.target.closest('[data-route-action]');
  if (!button || button.disabled || !state || action || routeActionPending) return;
  const item = button.closest('[data-route]');
  const route = routeContexts.get(item);
  if (!route) return;

  const side = button.dataset.routeAction;
  const remote = side === 'remote';
  const payload = {
    side,
    host: remote ? route.remoteBind : route.localHost,
    port: remote ? route.remotePort : route.localPort,
  };
  if (remote) payload.sshHost = route.sshHost;
  const feedback = item.querySelector('[data-route-feedback]');
  const restoreFocus = document.activeElement === button;

  routeActionPending = true;
  stateVersion++;
  announce(feedback, '正在打开浏览器…');
  showControls();
  try {
    const result = await api('open', 'POST', payload);
    announce(feedback, result.viaSSH ? '已将 SSH 预览链接交给浏览器。' : '已将 HTTP 地址交给浏览器。');
  } catch (error) {
    announce(feedback, error.message, true);
  } finally {
    routeActionPending = false;
    showControls();
    if (restoreFocus && button.isConnected && !button.disabled && document.hasFocus()) button.focus({ preventScroll: true });
    await refreshState(true);
  }
}

routeList.addEventListener('click', handleRouteAction);

async function handlePortTest(event) {
  const button = event.target.closest('[data-test-action]');
  if (!button || button.disabled || !state) return;
  const target = button.closest('[data-test-key]');
  const { testKey: key, testHost: host, testPort, testRunning } = target.dataset;
  if (pendingTests.has(key)) return;
  const port = Number(testPort);
  const operation = testRunning === 'true' ? 'stop' : 'start';
  const feedback = target.querySelector('[data-test-feedback]');
  const restoreFocus = document.activeElement === button;
  const samePort = () => target.isConnected && target.dataset.testKey === key;

  pendingTests.set(key, operation);
  announce(feedback);
  showPortTests();
  try {
    const result = await api(`test/${operation}`, 'POST', { host, port });
    // The response can predate another port's mutation. Update just this test.
    const reported = result.tests?.find((test) => testKey(test.host, test.port) === key);
    const tests = (state.tests || []).filter((test) => testKey(test.host, test.port) !== key);
    if (reported) tests.push(reported);
    state = { ...state, tests };
    stateVersion++;
    if (operation === 'stop' && samePort()) announce(feedback, `端口 ${port} 的 HTTP 测试已停止。`);
  } catch (error) {
    if (samePort()) announce(feedback, error.message, true);
    else announce('formMessage', `${endpoint(host, port)} ${error.message}`, true);
  } finally {
    pendingTests.delete(key);
    showPortTests();
    if (restoreFocus && document.hasFocus() && document.activeElement === document.body) {
      const destination = button.isConnected && !button.disabled ? button : forwardList.querySelector('[data-test-action]:enabled');
      destination?.focus({ preventScroll: true });
    }
    await refreshState(true);
  }
}

forwardList.addEventListener('click', handlePortTest);
detachedTests.addEventListener('click', handlePortTest);

forwardList.addEventListener('click', async (event) => {
  const button = event.target.closest('[data-check-action]');
  if (!button || button.disabled) return;
  const row = button.closest('[data-forward]');
  const { payload, key } = checkTarget(row);
  const check = { key, pending: true, controller: new AbortController() };
  const restoreFocus = document.activeElement === button;

  ruleChecks.set(row, check);
  showRuleCheck(row);
  try {
    const result = await api('check', 'POST', payload, check.controller.signal);
    if (!result.local || !result.remote) throw new Error('应用返回了不完整的检测结果。');
    check.result = result;
  } catch (error) {
    if (!check.controller.signal.aborted) check.error = error.message;
  } finally {
    if (row.isConnected && ruleChecks.get(row) === check) {
      check.pending = false;
      showRuleCheck(row);
      if (restoreFocus && !button.disabled && document.hasFocus() && document.activeElement === document.body) {
        button.focus({ preventScroll: true });
      }
    }
  }
});

function onEdit(event) {
  const name = event.target.name;
  if (name === 'auth' && event.target.value !== 'password') field('rememberPassword').checked = false;
  const remembering = field('rememberPassword').checked;
  if (field('auth').value === 'password' && !passwordAvailable(readSSHServer(), remembering)) field('autoConnect').checked = false;
  if (profileFields.includes(name) || event.target.hasAttribute('data-forward-field') || (name === 'password' && remembering)) edited = true;
  announce('formMessage');
  announce('configMessage');
  showControls();
}

function clearCredentials() {
  field('password').value = '';
  field('passphrase').value = '';
}

async function changeConnection(kind, path, method, data, message = '', feedback = 'formMessage') {
  action = kind;
  stateVersion++;
  announce(feedback);
  showControls();
  try {
    await api(path, method, data);
    if (kind === 'connect' || kind === 'save') edited = false;
    if (kind === 'connect') clearCredentials();
    if (kind === 'save' && data.password !== undefined) field('password').value = '';
    announce(feedback, message);
  } catch (error) {
    announce(feedback, error.message, true);
  } finally {
    action = '';
    showControls();
    await refreshState(true);
  }
}

function markForwardsEdited() {
  edited = true;
  announce('formMessage');
  announce('configMessage');
  updateForwardRowLabels();
  showControls();
}

byId('addForward').addEventListener('click', () => {
  const current = forwardRows().map(readForward);
  if (current.length >= forwardLimit) return;
  const remotePorts = new Set(current.map((forward) => forward.remotePort));
  const last = current.at(-1);
  let port = last.remotePort > 0 && last.remotePort < 65535 ? last.remotePort + 1 : defaultForward.remotePort;
  while (remotePorts.has(port)) port = (port % 65535) + 1;
  const row = createForward({
    ...defaultForward,
    remoteBind: last.remoteBind || defaultForward.remoteBind,
    remotePort: port,
    localHost: last.localHost || defaultForward.localHost,
    localPort: last.localPort > 0 && last.localPort < 65535 ? last.localPort + 1 : defaultForward.localPort,
  });
  forwardList.append(row);
  markForwardsEdited();
  forwardField(row, 'remotePort').focus();
});

forwardList.addEventListener('click', (event) => {
  const remove = event.target.closest('[data-remove-forward]');
  const rows = forwardRows();
  if (!remove || rows.length <= 1) return;
  const row = remove.closest('[data-forward]');
  const index = rows.indexOf(row);
  discardRuleCheck(row);
  row.remove();
  markForwardsEdited();
  forwardField(rows[index + 1] || rows[index - 1], 'remotePort').focus();
});

form.addEventListener('input', onEdit);
byId('reconnect').addEventListener('input', onEdit);
byId('autoConnect').addEventListener('input', onEdit);

byId('connectButton').addEventListener('click', () => {
  if (!activeStates.has(state?.status?.state)) selectTab(byId('configTab'));
});

form.addEventListener('submit', async (event) => {
  event.preventDefault();
  if (action) return;
  if (activeStates.has(state?.status?.state)) {
    await changeConnection('disconnect', 'disconnect', 'POST', {});
    return;
  }
  const profile = readProfile();
  const password = profile.auth === 'password' ? field('password').value : '';
  const passphrase = profile.auth === 'key' ? field('passphrase').value : '';
  if (profile.auth === 'password' && !password && !(profile.rememberPassword && hasSavedPassword(profile))) {
    announce('formMessage', '请输入 SSH 密码。', true);
    field('password').focus();
    return;
  }
  if (profile.auth === 'key' && !profile.keyPath) {
    announce('formMessage', '请选择 SSH 私钥文件。', true);
    field('keyPath').focus();
    return;
  }
  await changeConnection('connect', 'connect', 'POST', { ...profile, password, passphrase });
});

byId('saveButton').addEventListener('click', async () => {
  if (action || !form.reportValidity()) return;
  const profile = readProfile();
  const password = field('password').value;
  if (profile.rememberPassword && password) profile.password = password;
  await changeConnection('save', 'profile', 'PUT', profile, '配置已保存。', 'configMessage');
});

async function transferProfile(kind) {
  if (action) return;
  action = kind;
  stateVersion++;
  announce('configMessage');
  showControls();
  try {
    const result = await api(kind, 'POST', kind === 'import' ? {} : readProfile());
    if (result.imported) {
      edited = false;
      clearCredentials();
      showState(result.state);
      announce('configMessage', '配置已导入并保存。');
    } else if (result.saved) {
      announce('configMessage', '配置已导出。');
    }
  } catch (error) {
    announce('configMessage', error.message, true);
  } finally {
    action = '';
    showControls();
    await refreshState(true);
  }
}

const importDialog = byId('importDialog');
byId('importButton').addEventListener('click', () => {
  if (edited) importDialog.showModal();
  else transferProfile('import');
});
byId('cancelImport').addEventListener('click', () => importDialog.close());
byId('confirmImport').addEventListener('click', () => {
  importDialog.close();
  transferProfile('import');
});
byId('exportButton').addEventListener('click', () => {
  if (form.reportValidity()) transferProfile('export');
});

const commandDialog = byId('commandDialog');
const sshCommand = byId('sshCommand');
let commandRequest;

byId('importCommandButton').addEventListener('click', () => commandDialog.showModal());
byId('cancelCommand').addEventListener('click', () => commandDialog.close());
commandDialog.addEventListener('close', () => {
  commandRequest?.abort();
  sshCommand.value = '';
  announce('commandError');
});
sshCommand.addEventListener('input', () => announce('commandError'));

byId('commandForm').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (action) return;
  const command = sshCommand.value.trim();
  if (!command) {
    announce('commandError', '请粘贴 SSH 命令。', true);
    sshCommand.focus();
    return;
  }

  const apply = byId('applyCommand');
  const request = new AbortController();
  commandRequest = request;
  action = 'import-command';
  stateVersion++;
  apply.disabled = true;
  apply.textContent = '解析中…';
  sshCommand.readOnly = true;
  announce('commandError');
  showControls();
  try {
    const { settings, warnings } = await api('import-command', 'POST', { command }, request.signal);
    if (!commandDialog.open) return;
    if (!settings || typeof settings.host !== 'string' || typeof settings.user !== 'string' ||
      !Number.isInteger(settings.sshPort) || !settings.forwards?.length) {
      throw new Error('应用返回了不完整的 SSH 配置。');
    }

    const { host, sshPort, user, forwards } = settings;
    const current = readProfile();
    const draft = { ...current, host, sshPort, user, forwards };
    if (sshIdentity(current) !== sshIdentity(draft)) {
      clearCredentials();
      draft.rememberPassword = false;
      if (draft.auth === 'password') draft.autoConnect = false;
    }
    loadProfile(draft);
    edited = true;
    const messages = Array.isArray(warnings) ? warnings.filter((message) => typeof message === 'string' && message.trim()) : [];
    announce('configMessage', ['已填入配置草稿。', ...messages].join(' '));
    announce('formMessage');
    commandDialog.close();
  } catch (error) {
    if (!request.signal.aborted) announce('commandError', error.message, true);
  } finally {
    commandRequest = undefined;
    action = '';
    sshCommand.readOnly = false;
    apply.disabled = false;
    apply.textContent = '解析并填入';
    if (commandDialog.open) sshCommand.focus();
    showControls();
    await refreshState(true);
  }
});

byId('autoStart').addEventListener('change', async (event) => {
  const enabled = event.target.checked;
  startupPending = true;
  stateVersion++;
  startupNotice = '';
  announce('settingsMessage');
  showControls();
  try {
    await api('autostart', 'POST', { enabled });
    startupNotice = enabled ? '开机启动已开启。' : '开机启动已关闭。';
    announce('settingsMessage', startupNotice);
  } catch (error) {
    event.target.checked = !enabled;
    startupNotice = error.message;
    announce('settingsMessage', startupNotice, true);
  } finally {
    startupPending = false;
    showControls();
    await refreshState(true);
  }
});

byId('pickKey').addEventListener('click', async () => {
  if (keyPickerPending) return;
  keyPickerPending = true;
  showControls();
  try {
    const { path } = await api('pick-key', 'POST', {});
    if (path) {
      field('keyPath').value = path;
      edited = true;
      announce('formMessage');
      announce('configMessage');
    }
  } catch (error) {
    announce('formMessage', error.message, true);
  } finally {
    keyPickerPending = false;
    showControls();
  }
});

async function resolveTrust(accepted) {
  if (action || !currentTrust) return;
  const buttons = [byId('acceptTrust'), byId('rejectTrust')];
  for (const button of buttons) button.disabled = true;
  if (accepted) await changeConnection('trust', 'trust', 'POST', { fingerprint: currentTrust }, '', 'trustError');
  else await changeConnection('disconnect', 'disconnect', 'POST', {}, '', 'trustError');
  for (const button of buttons) button.disabled = false;
}

byId('acceptTrust').addEventListener('click', () => resolveTrust(true));
byId('rejectTrust').addEventListener('click', () => resolveTrust(false));
byId('trustDialog').addEventListener('cancel', (event) => {
  event.preventDefault();
  resolveTrust(false);
});

const backgroundPlace = navigator.userAgent.includes('Macintosh') ? '菜单栏' : '系统托盘';
byId('backgroundNote').textContent = `关闭窗口后隧道继续运行。可从${backgroundPlace}重新打开窗口或退出。`;
forwardList.append(createForward(defaultForward));
updateForwardRowLabels();
showControls();
refreshState();
window.setInterval(refreshState, 1000);
document.addEventListener('visibilitychange', () => refreshState());
