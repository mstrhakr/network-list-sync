const API = '/api';
const browserFetch = window.fetch.bind(window);
let jobs = [];
let controllers = [];
const logDetailsMap = new Map();
const schedulePresets = {
    manual: '',
    hourly: '0 * * * *',
    daily: '0 0 * * *',
    weekly: '0 0 * * 0',
    monthly: '0 0 1 * *',
};
const scheduleLabels = {
    manual: 'Manual only',
    hourly: 'Hourly',
    daily: 'Daily',
    weekly: 'Weekly',
    monthly: 'Monthly',
    custom: 'Custom',
};
let additionalTargetRowSeq = 0;
let activeJobActionId = null;
let jobsCacheSignature = '';
let jobsHasRendered = false;
let jobsLoadSeq = 0;
let jobsViewMode = localStorage.getItem('nls_jobs_view_mode') === 'table' ? 'table' : 'cards';
let jobsTableCustomizer = null;
let networkListNameCache = Object.create(null);
let networkListsLoadedControllers = Object.create(null);
let networkListFetchPromises = Object.create(null);
let controllerProviderChangeBound = false;

window.fetch = async function(input, init) {
    const response = await browserFetch(input, init);
    const url = typeof input === 'string' ? input : (input && input.url ? input.url : '');
    if (response.status === 401 && url.indexOf(API + '/') === 0) {
        window.location.href = '/login';
    }
    return response;
};

const CONTROLLER_PROVIDER_UI = {
    unifi: {
        namePlaceholder: 'e.g., Primary UniFi Instance',
        nameHint: 'Friendly UniFi endpoint label shown in job target selectors.',
        urlPlaceholder: 'https://192.168.1.1',
        urlHint: 'Instance origin only (no path), e.g. https://192.168.1.1. Potential cloud endpoint: https://unifi.ui.com.',
        siteLabel: 'Site',
        sitePlaceholder: 'default',
        siteHint: 'UniFi site name (usually default).',
        apiLabel: 'API Key',
        apiHint: 'Paste the UniFi API key for this endpoint.',
    },
    npm: {
        namePlaceholder: 'e.g., Primary NPM Instance',
        nameHint: 'Friendly NPM endpoint label shown in job target selectors.',
        urlPlaceholder: 'http://192.168.1.50:81',
        urlHint: 'NPM origin only (no path), e.g. http://192.168.1.50:81.',
        siteLabel: 'User',
        sitePlaceholder: 'admin@example.com',
        siteHint: 'NPM login identity (email or username).',
        apiLabel: 'Password',
        apiHint: 'Use the NPM account password.',
    },
};

const JOB_COLUMN_DEFS = [
    { id: 'name', label: 'Name', sortKey: 'name', hideable: false, render: function(job) { return '<strong>' + escapeHtml(job.name) + '</strong>'; } },
    { id: 'primary_endpoint', label: 'Primary Endpoint', sortKey: 'instance_name', render: function(job) { return escapeHtml(job.instance_name || 'Unknown'); } },
    { id: 'primary_list', label: 'Primary List', sortKey: 'primary_list_name', render: function(job) { return renderPrimaryListValue(job); } },
    { id: 'endpoints', label: 'Endpoints', sortKey: 'target_count', render: function(job) { var count = Array.isArray(job.targets) && job.targets.length > 0 ? job.targets.length : 1; return String(count); } },
    { id: 'schedule', label: 'Schedule', sortKey: 'schedule', render: function(job) { return scheduleDisplayHtml(job.schedule, !!job.enabled); } },
    { id: 'retention', label: 'IP Retention', sortKey: 'observed_ip_ttl_hours', render: function(job) { return job.observed_ip_ttl_hours > 0 ? escapeHtml(String(job.observed_ip_ttl_hours)) + 'h' : '<em>Disabled</em>'; } },
    { id: 'last_run', label: 'Last Run', sortKey: 'last_run_at', render: function(job) { return job.last_run_at ? formatTime(job.last_run_at) : '<em>Never</em>'; } },
    { id: 'result', label: 'Result', sortKey: 'last_result', render: function(job) {
        var result = job.last_result || '';
        var cls = result.indexOf('success') === 0 ? 'text-success' : (result.indexOf('error') === 0 ? 'text-error' : '');
        return '<span class="' + cls + '">' + escapeHtml(result || '-') + '</span>';
    } },
    { id: 'actions', label: 'Actions', hideable: false, render: function(job) {
        return '<button class="btn btn-small btn-secondary" type="button" onclick="openJobActionsMenuForButton(event, ' + job.id + ')">Actions</button>';
    } },
];

function networkListCacheKey(controllerId, listId) {
    return String(controllerId || '') + '::' + String(listId || '');
}

function cacheNetworkListsForEndpoint(endpointId, lists) {
    var controllerId = String(endpointId || '');
    networkListsLoadedControllers[controllerId] = true;
    (lists || []).forEach(function(list) {
        if (!list || !list.id) return;
        networkListNameCache[networkListCacheKey(controllerId, list.id)] = list.name || list.id;
    });
}

function resolvePrimaryListName(job) {
    if (!job || !job.instance_id || !job.target_list_id) return job && job.target_list_id ? job.target_list_id : '';
    return networkListNameCache[networkListCacheKey(job.instance_id, job.target_list_id)] || job.target_list_id;
}

function renderPrimaryListValue(job) {
    var value = job && job.primary_list_name ? job.primary_list_name : resolvePrimaryListName(job);
    if (!value) return '-';
    if (job && job.target_list_id && value !== job.target_list_id) {
        return '<span title="' + escapeHtml(job.target_list_id) + '">' + escapeHtml(value) + '</span>';
    }
    return '<span class="mono">' + escapeHtml(value) + '</span>';
}

function decorateJobsWithPrimaryListNames(jobList) {
    return (jobList || []).map(function(job) {
        return Object.assign({}, job, {
            primary_list_name: resolvePrimaryListName(job),
        });
    });
}

async function ensureControllerNetworkListsCached(controllerId) {
    var cacheKey = String(controllerId || '');
    if (!cacheKey) return false;
    if (networkListsLoadedControllers[cacheKey]) return false;
    if (networkListFetchPromises[cacheKey]) return networkListFetchPromises[cacheKey];

    networkListFetchPromises[cacheKey] = fetchListsForEndpoint(controllerId)
        .then(function() {
            return true;
        })
        .catch(function(err) {
            console.error('Load target lists error:', err);
            return false;
        })
        .finally(function() {
            delete networkListFetchPromises[cacheKey];
        });

    return networkListFetchPromises[cacheKey];
}

async function ensureJobPrimaryListNames(jobList) {
    var controllerIds = [];
    var seen = Object.create(null);

    (jobList || []).forEach(function(job) {
        var controllerId = String(job && job.instance_id ? job.instance_id : '');
        if (!controllerId || !job.target_list_id || seen[controllerId] || networkListsLoadedControllers[controllerId]) return;
        seen[controllerId] = true;
        controllerIds.push(controllerId);
    });

    if (!controllerIds.length) return;
    await Promise.all(controllerIds.map(function(controllerId) {
        return ensureControllerNetworkListsCached(controllerId);
    }));
}

function applyTheme(dark) {
    document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
    var toggle = document.getElementById('theme-toggle-checkbox');
    if (toggle) {
        toggle.checked = !dark;
    }
}

function initThemeToggle() {
    var toggle = document.getElementById('theme-toggle-checkbox');
    if (!toggle) return;
    applyTheme(localStorage.getItem('theme') === 'dark');
    toggle.addEventListener('change', function() {
        var dark = !toggle.checked;
        localStorage.setItem('theme', dark ? 'dark' : 'light');
        applyTheme(dark);
    });
}

function initJobsTableCustomizer() {
    if (jobsTableCustomizer || typeof TableCustomizer !== 'function') return;
    jobsTableCustomizer = new TableCustomizer('jobs', {
        storageKey: 'nls_table_config_jobs',
        columnDefs: JOB_COLUMN_DEFS,
        onColumnChange: function() {
            renderJobs(false);
        },
        onSort: function() {
            renderJobs(false);
        },
    });
}

function updateJobsViewToggleUI() {
    var buttons = document.querySelectorAll('.view-toggle-btn[data-view]');
    for (var i = 0; i < buttons.length; i++) {
        var active = buttons[i].getAttribute('data-view') === jobsViewMode;
        buttons[i].classList.toggle('active', active);
    }
    var toolbar = document.getElementById('jobsTableCustomizerToolbar');
    if (toolbar) {
        toolbar.classList.toggle('hidden', jobsViewMode !== 'table');
    }
}

function setJobsViewMode(mode) {
    jobsViewMode = mode === 'table' ? 'table' : 'cards';
    localStorage.setItem('nls_jobs_view_mode', jobsViewMode);
    updateJobsViewToggleUI();
    renderJobs(false);
}

function getControllerProviderUI(provider) {
    var normalized = String(provider || 'unifi').toLowerCase();
    return CONTROLLER_PROVIDER_UI[normalized] || CONTROLLER_PROVIDER_UI.unifi;
}

function resolveControllerSiteValue(provider, rawSiteValue) {
    return String(rawSiteValue || '').trim();
}

function applyControllerProviderUI(options) {
    var opts = options || {};
    var providerEl = document.getElementById('ctrlProvider');
    var nameInput = document.getElementById('ctrlName');
    var nameHint = document.getElementById('ctrlNameHint');
    var urlInput = document.getElementById('ctrlUrl');
    var urlHint = document.getElementById('ctrlUrlHint');
    var siteLabel = document.getElementById('ctrlSiteLabel');
    var siteInput = document.getElementById('ctrlSite');
    var siteHint = document.getElementById('ctrlSiteHint');
    var apiLabel = document.getElementById('ctrlApiKeyLabel');
    var apiInput = document.getElementById('ctrlApiKey');
    var apiHint = document.getElementById('ctrlApiKeyHint');
    if (!providerEl || !nameInput || !nameHint || !urlInput || !urlHint || !siteLabel || !siteInput || !siteHint || !apiLabel || !apiInput || !apiHint) return;

    var ui = getControllerProviderUI(providerEl.value);
    nameInput.placeholder = ui.namePlaceholder;
    nameHint.textContent = ui.nameHint;
    urlInput.placeholder = ui.urlPlaceholder;
    urlHint.textContent = ui.urlHint;
    siteLabel.textContent = ui.siteLabel;
    siteInput.placeholder = ui.sitePlaceholder;
    siteHint.textContent = ui.siteHint;
    apiLabel.textContent = ui.apiLabel;
    apiHint.textContent = ui.apiHint;

    if (opts.editing) {
        apiInput.placeholder = 'Leave blank to keep existing';
    } else if (opts.clearApiPlaceholder) {
        apiInput.placeholder = '';
    }
}

function bindControllerProviderChange() {
    var providerEl = document.getElementById('ctrlProvider');
    if (!providerEl || controllerProviderChangeBound) return;
    providerEl.addEventListener('change', function() {
        var editing = !!document.getElementById('ctrlId').value;
        applyControllerProviderUI({
            editing: editing,
            clearApiPlaceholder: !editing,
        });
    });
    controllerProviderChangeBound = true;
}

async function init() {
    initThemeToggle();
    initJobsTableCustomizer();
    bindControllerProviderChange();
    applyControllerProviderUI({ clearApiPlaceholder: true });
    updateJobsViewToggleUI();
    await Promise.all([loadJobs(), loadControllers(), checkHealth()]);
    setInterval(loadJobs, 30000);
    setInterval(checkHealth, 30000);
}

async function logout() {
    try {
        await browserFetch('/logout', { method: 'POST' });
    } finally {
        window.location.href = '/login';
    }
}

async function checkHealth() {
    try {
        const resp = await fetch(API + '/health');
        const data = await resp.json();
        const banner = document.getElementById('dnsBanner');
        const msg = document.getElementById('dnsBannerMsg');
        if (data.ok) {
            banner.classList.add('hidden');
        } else {
            msg.textContent = data.message;
            banner.classList.remove('hidden');
        }
    } catch (err) {
        console.error('Health check error:', err);
    }
}

async function loadControllers() {
    try {
        const resp = await fetch(API + '/instances');
        if (!resp.ok) throw new Error('Failed to load instances');
        controllers = await resp.json();
    } catch (err) {
        console.error('Load instances error:', err);
    }
}

function showControllerModal() {
    loadControllers().then(function() {
        renderControllerTable();
        hideControllerForm();
        document.getElementById('controllerModal').classList.remove('hidden');
    });
}

function hideControllerModal() {
    document.getElementById('controllerModal').classList.add('hidden');
}

function renderControllerTable() {
    const tbody = document.getElementById('controllerTableBody');
    const noMsg = document.getElementById('noControllersMsg');
    if (controllers.length === 0) {
        tbody.innerHTML = '';
        noMsg.classList.remove('hidden');
        return;
    }
    noMsg.classList.add('hidden');
    tbody.innerHTML = controllers.map(function(c) {
        var provider = (c.provider || 'unifi').toLowerCase();
        return '<tr>' +
            '<td>' + escapeHtml(c.name) + '</td>' +
            '<td><span class="badge badge-warning">' + escapeHtml(provider.toUpperCase()) + '</span></td>' +
            '<td class="mono">' + escapeHtml(c.url) + '</td>' +
            '<td>' + escapeHtml(c.site) + '</td>' +
            '<td class="mono">' + (c.api_key ? '••••••••' : '<em>not set</em>') + '</td>' +
            '<td>' + (c.skip_tls_verify ? '<span class="badge badge-warning">Yes</span>' : 'No') + '</td>' +
            '<td style="white-space:nowrap">' +
                '<button class="btn btn-small btn-secondary" onclick="editController(' + c.id + ')">Edit</button> ' +
                '<button class="btn btn-small btn-danger" onclick="deleteController(' + c.id + ')">Delete</button>' +
            '</td></tr>';
    }).join('');
}

function showControllerForm(ctrl) {
    document.getElementById('controllerFormWrap').classList.remove('hidden');
    if (ctrl) {
        document.getElementById('ctrlId').value = ctrl.id;
        document.getElementById('ctrlProvider').value = (ctrl.provider || 'unifi').toLowerCase();
        document.getElementById('ctrlName').value = ctrl.name;
        document.getElementById('ctrlUrl').value = ctrl.url;
        document.getElementById('ctrlSite').value = ctrl.site || '';
        document.getElementById('ctrlApiKey').value = '';
        document.getElementById('ctrlApiKey').placeholder = 'Leave blank to keep existing';
        document.getElementById('ctrlApiKey').required = false;
        document.getElementById('ctrlSkipTls').checked = !!ctrl.skip_tls_verify;
        applyControllerProviderUI({ editing: true });
    } else {
        document.getElementById('controllerForm').reset();
        document.getElementById('ctrlId').value = '';
        document.getElementById('ctrlProvider').value = 'unifi';
        document.getElementById('ctrlSite').value = '';
        document.getElementById('ctrlApiKey').placeholder = '';
        document.getElementById('ctrlApiKey').required = true;
        document.getElementById('ctrlSkipTls').checked = false;
        applyControllerProviderUI({ clearApiPlaceholder: true });
    }
}

function hideControllerForm() {
    document.getElementById('controllerFormWrap').classList.add('hidden');
}

function editController(id) {
    var controller = controllers.find(function(item) { return item.id === id; });
    if (controller) showControllerForm(controller);
}

async function saveController(event) {
    event.preventDefault();
    var id = document.getElementById('ctrlId').value;
    var provider = document.getElementById('ctrlProvider').value || 'unifi';
    var providerUI = getControllerProviderUI(provider);
    var data = {
        provider: provider,
        name: document.getElementById('ctrlName').value,
        url: document.getElementById('ctrlUrl').value,
        site: resolveControllerSiteValue(provider, document.getElementById('ctrlSite').value),
        api_key: document.getElementById('ctrlApiKey').value,
        skip_tls_verify: document.getElementById('ctrlSkipTls').checked,
    };
    if (!id && !data.api_key) {
        showToast(providerUI.apiLabel + ' is required for new endpoints', 'error');
        return;
    }
    var url = id ? API + '/instances/' + id : API + '/instances';
    var method = id ? 'PUT' : 'POST';
    try {
        var resp = await fetch(url, {
            method: method,
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(data),
        });
        if (!resp.ok) {
            var err = await resp.json();
            throw new Error(err.error || 'Request failed');
        }
        await loadControllers();
        renderControllerTable();
        hideControllerForm();
        showToast(id ? 'Endpoint updated' : 'Endpoint added', 'success');
    } catch (err) {
        showToast(err.message, 'error');
    }
}

async function testController() {
    var id = document.getElementById('ctrlId').value;
    var provider = document.getElementById('ctrlProvider').value || 'unifi';
    var providerUI = getControllerProviderUI(provider);
    var apiKey = document.getElementById('ctrlApiKey').value;
    if (!apiKey && !id) {
        showToast('Enter ' + providerUI.apiLabel + ' to test a new endpoint', 'error');
        return;
    }
    var data = {
        instance_id: id ? parseInt(id, 10) : 0,
        provider: provider,
        url: document.getElementById('ctrlUrl').value,
        site: resolveControllerSiteValue(provider, document.getElementById('ctrlSite').value),
        api_key: apiKey,
        skip_tls_verify: document.getElementById('ctrlSkipTls').checked,
    };
    if (!data.url) { showToast('Enter a URL to test', 'error'); return; }
    showToast('Testing connection…', 'success');
    try {
        var resp = await fetch(API + '/instances/test', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(data),
        });
        var body = await resp.json();
        if (!resp.ok) throw new Error(body.error || 'Connection failed');
        showToast('Connection OK — found ' + body.target_lists + ' target list(s)', 'success');
    } catch (err) {
        showToast('Test failed: ' + err.message, 'error');
    }
}

async function deleteController(id) {
    if (!confirm('Delete this endpoint? Jobs using it will stop working.')) return;
    try {
        var resp = await fetch(API + '/instances/' + id, { method: 'DELETE' });
        if (!resp.ok) throw new Error('Delete failed');
        await loadControllers();
        renderControllerTable();
        showToast('Endpoint deleted', 'success');
    } catch (err) {
        showToast(err.message, 'error');
    }
}

function populateControllerDropdown(selectedId) {
    var sel = document.getElementById('controllerId');
    sel.innerHTML = '<option value="">Select an endpoint...</option>' +
        controllers.map(function(c) {
            var selected = c.id == selectedId ? ' selected' : '';
            var provider = (c.provider || 'unifi').toUpperCase();
            return '<option value="' + c.id + '"' + selected + '>' + escapeHtml(c.name) + ' [' + escapeHtml(provider) + '] (' + escapeHtml(c.url) + ')</option>';
        }).join('');
}

function formatListOptionLabel(item) {
    var typeLabel = item.type === 'IPV4_ADDRESSES' ? 'IPv4' : item.type === 'IPV6_ADDRESSES' ? 'IPv6' : item.type === 'PORTS' ? 'Ports' : item.type;
    var itemCount = item.items ? item.items.length : 0;
    return escapeHtml(item.name) + ' (' + typeLabel + ', ' + itemCount + ' entries)';
}

async function fetchListsForEndpoint(endpointId) {
    var resp = await fetch(API + '/instances/' + endpointId + '/target-lists');
    var body = await resp.json();
    if (!resp.ok) {
        throw new Error((body && body.error) ? body.error : 'Failed to fetch target lists');
    }
    cacheNetworkListsForEndpoint(endpointId, body);
    return body;
}

async function populateAdditionalTargetListSelect(endpointSel, listSel, selectedListID) {
    var endpointId = endpointSel.value;
    if (!endpointId) {
        listSel.innerHTML = '<option value="">Select an endpoint first...</option>';
        return;
    }
    listSel.innerHTML = '<option value="">Loading lists...</option>';
    try {
        var lists = await fetchListsForEndpoint(endpointId);
        if (!lists.length) {
            listSel.innerHTML = '<option value="">No lists found</option>';
            return;
        }
        listSel.innerHTML = '<option value="">Select a target list...</option>' +
            lists.map(function(nl) { return '<option value="' + nl.id + '">' + formatListOptionLabel(nl) + '</option>'; }).join('');
        if (selectedListID) {
            listSel.value = selectedListID;
        }
    } catch (err) {
        listSel.innerHTML = '<option value="">Error loading lists</option>';
        showToast(err.message, 'error');
    }
}

function addAdditionalTargetRow(target) {
    var container = document.getElementById('additionalTargetsRows');
    var rowId = 'additionalTargetRow' + (++additionalTargetRowSeq);
    var div = document.createElement('div');
    div.className = 'form-row';
    div.style.marginBottom = '0.5rem';
    div.id = rowId;
    div.innerHTML =
        '<div class="form-group">' +
            '<label>Endpoint</label>' +
            '<select class="additionalTargetController"><option value="">Select an endpoint...</option></select>' +
        '</div>' +
        '<div class="form-group">' +
            '<label>Target List</label>' +
            '<select class="additionalTargetList"><option value="">Select an endpoint first...</option></select>' +
        '</div>' +
        '<div class="form-group" style="display:flex;align-items:flex-end;">' +
            '<button type="button" class="btn btn-small btn-danger" onclick="removeAdditionalTargetRow(\'' + rowId + '\')">Remove</button>' +
        '</div>';
    container.appendChild(div);

    var endpointSel = div.querySelector('.additionalTargetController');
    var listSel = div.querySelector('.additionalTargetList');

    endpointSel.innerHTML = '<option value="">Select an endpoint...</option>' +
        controllers.map(function(c) {
            var provider = (c.provider || 'unifi').toUpperCase();
            return '<option value="' + c.id + '">' + escapeHtml(c.name) + ' [' + escapeHtml(provider) + ']</option>';
        }).join('');

    endpointSel.addEventListener('change', function() {
        populateAdditionalTargetListSelect(endpointSel, listSel, '');
    });

    if (target && target.instance_id) {
        endpointSel.value = String(target.instance_id);
        populateAdditionalTargetListSelect(endpointSel, listSel, target.target_list_id || '');
    }
}

function removeAdditionalTargetRow(rowId) {
    var row = document.getElementById(rowId);
    if (row) row.remove();
}

function collectAdditionalTargets() {
    var rows = document.querySelectorAll('#additionalTargetsRows .form-row');
    var out = [];
    for (var i = 0; i < rows.length; i++) {
        var endpointSel = rows[i].querySelector('.additionalTargetController');
        var listSel = rows[i].querySelector('.additionalTargetList');
        var endpointId = endpointSel ? endpointSel.value : '';
        var listID = listSel ? listSel.value : '';
        if (!endpointId && !listID) continue;
        if (!endpointId || !listID) {
            throw new Error('Each additional target row must include both endpoint and list.');
        }
        out.push({ instance_id: parseInt(endpointId, 10), target_list_id: listID });
    }
    return out;
}

async function onControllerChange() {
    var ctrlId = document.getElementById('controllerId').value;
    var sel = document.getElementById('networkListId');
    var hint = document.getElementById('networkListHint');
    if (!ctrlId) {
        sel.innerHTML = '<option value="">Select an endpoint first...</option>';
        hint.textContent = 'Lists are fetched live from the selected endpoint';
        return;
    }
    sel.innerHTML = '<option value="">Loading target lists...</option>';
    hint.textContent = '';
    try {
        var lists = await fetchListsForEndpoint(ctrlId);
        if (lists.length === 0) {
            sel.innerHTML = '<option value="">No target lists found</option>';
            return;
        }
        sel.innerHTML = '<option value="">Select a target list...</option>' +
            lists.map(function(nl) {
                return '<option value="' + nl.id + '">' + formatListOptionLabel(nl) + '</option>';
            }).join('');
    } catch (err) {
        sel.innerHTML = '<option value="">Error: ' + escapeHtml(err.message) + '</option>';
        hint.textContent = 'Check endpoint credentials';
    }
}

function resolveSchedulePreset(schedule) {
    var normalized = (schedule || '').trim();
    if (!normalized) return 'manual';
    for (var key in schedulePresets) {
        if (Object.prototype.hasOwnProperty.call(schedulePresets, key) && schedulePresets[key] === normalized) {
            return key;
        }
    }
    return 'custom';
}

function onSchedulePresetChange() {
    var preset = document.getElementById('schedulePreset').value;
    var customWrap = document.getElementById('customScheduleWrap');
    var scheduleEnabledWrap = document.getElementById('scheduleEnabledWrap');
    var scheduleInput = document.getElementById('schedule');
    var enabledInput = document.getElementById('enabled');
    var isCustom = preset === 'custom';
    var isManual = preset === 'manual';

    customWrap.classList.toggle('hidden', !isCustom);
    scheduleEnabledWrap.classList.toggle('hidden', isManual);
    if (!isCustom) {
        scheduleInput.value = schedulePresets[preset] || '';
    }
    if (isManual) {
        enabledInput.checked = true;
    }
}

function getScheduleValueFromForm() {
    var preset = document.getElementById('schedulePreset').value;
    if (preset === 'custom') {
        return document.getElementById('schedule').value.trim();
    }
    return schedulePresets[preset] || '';
}

function scheduleDisplayHtml(schedule, scheduleEnabled) {
    var preset = resolveSchedulePreset(schedule);
    if (preset === 'manual') {
        return '<em>Manual only</em>';
    }

    if (!scheduleEnabled) {
        return '<em>Disabled</em>';
    }

    if (preset === 'custom') {
        return '<span class="mono">' + escapeHtml(schedule) + '</span>';
    }

    return escapeHtml(scheduleLabels[preset]) + ' <span class="mono">(' + escapeHtml(schedulePresets[preset]) + ')</span>';
}

function onObservedIpRetentionToggle() {
    var enabled = document.getElementById('observedIpRetentionEnabled').checked;
    var wrap = document.getElementById('retentionHoursWrap');
    var input = document.getElementById('observedIpTtlHours');

    input.disabled = !enabled;
    wrap.classList.toggle('is-disabled', !enabled);
}

async function loadJobs() {
    try {
        const loadSeq = ++jobsLoadSeq;
        const resp = await fetch(API + '/jobs');
        if (!resp.ok) throw new Error('Failed to load jobs');
        const rawJobs = await resp.json();
        const initialJobs = decorateJobsWithPrimaryListNames(rawJobs);
        const initialSignature = JSON.stringify(initialJobs);

        jobs = initialJobs;
        if (initialSignature !== jobsCacheSignature) {
            renderJobs(!jobsHasRendered);
            jobsHasRendered = true;
            jobsCacheSignature = initialSignature;
        }

        ensureJobPrimaryListNames(rawJobs)
            .then(function() {
                if (loadSeq !== jobsLoadSeq) return;

                const hydratedJobs = decorateJobsWithPrimaryListNames(rawJobs);
                const hydratedSignature = JSON.stringify(hydratedJobs);
                if (hydratedSignature === jobsCacheSignature) return;

                jobs = hydratedJobs;
                renderJobs(false);
                jobsHasRendered = true;
                jobsCacheSignature = hydratedSignature;
            })
            .catch(function(err) {
                console.error('Hydrate primary list names error:', err);
            });
    } catch (err) {
        console.error('Load jobs error:', err);
    }
}

async function saveJob(event) {
    event.preventDefault();
    const id = document.getElementById('jobId').value;
    const selectedSchedulePreset = document.getElementById('schedulePreset').value;
    const scheduleEnabled = selectedSchedulePreset === 'manual' ? true : document.getElementById('enabled').checked;
    const retentionEnabled = document.getElementById('observedIpRetentionEnabled').checked;
    const observedTTLHours = retentionEnabled ? parseInt(document.getElementById('observedIpTtlHours').value, 10) : 0;
    const primaryControllerID = parseInt(document.getElementById('controllerId').value, 10);
    const primaryNetworkListID = document.getElementById('networkListId').value;
    let additionalTargets = [];
    try {
        additionalTargets = collectAdditionalTargets();
    } catch (err) {
        showToast(err.message, 'error');
        return;
    }

    const targets = [];
    if (primaryControllerID && primaryNetworkListID) {
        targets.push({ instance_id: primaryControllerID, target_list_id: primaryNetworkListID });
    }
    targets.push.apply(targets, additionalTargets);

    const data = {
        name: document.getElementById('jobName').value,
        instance_id: primaryControllerID,
        target_list_id: primaryNetworkListID,
        targets: targets,
        hostnames: document.getElementById('hostnames').value,
        schedule: getScheduleValueFromForm(),
        observed_ip_ttl_hours: observedTTLHours,
        enabled: scheduleEnabled,
    };

    if (!targets.length) {
        showToast('Add at least one endpoint target', 'error');
        return;
    }
    if (selectedSchedulePreset === 'custom' && !data.schedule) {
        showToast('Custom cron schedule cannot be blank', 'error');
        return;
    }
    if (retentionEnabled && (!data.observed_ip_ttl_hours || data.observed_ip_ttl_hours < 1)) {
        showToast('Observed IP retention must be at least 1 hour', 'error');
        return;
    }

    const url = id ? API + '/jobs/' + id : API + '/jobs';
    const method = id ? 'PUT' : 'POST';

    try {
        const resp = await fetch(url, {
            method: method,
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(data),
        });
        if (!resp.ok) {
            const err = await resp.json();
            throw new Error(err.error || 'Request failed');
        }
        hideJobModal();
        await loadJobs();
        showToast(id ? 'Job updated' : 'Job created', 'success');
    } catch (err) {
        showToast(err.message, 'error');
    }
}

async function deleteJob(id) {
    if (!confirm('Delete this sync job and its run history?')) return;
    try {
        const resp = await fetch(API + '/jobs/' + id, { method: 'DELETE' });
        if (!resp.ok) throw new Error('Delete failed');
        await loadJobs();
        showToast('Job deleted', 'success');
    } catch (err) {
        showToast(err.message, 'error');
    }
}

async function runJob(id) {
    try {
        const resp = await fetch(API + '/jobs/' + id + '/run', { method: 'POST' });
        if (!resp.ok) throw new Error('Failed to start job');
        showToast('Sync job started', 'success');
        setTimeout(loadJobs, 3000);
    } catch (err) {
        showToast(err.message, 'error');
    }
}

async function showLogs(id) {
    const job = jobs.find(function(item) { return item.id === id; });
    document.getElementById('logsModalTitle').textContent = 'Run History: ' + (job ? job.name : 'Unknown');

    const content = document.getElementById('logsContent');
    content.innerHTML = '<p class="empty-text">Loading...</p>';
    document.getElementById('logsModal').classList.remove('hidden');

    let limitWrap = document.getElementById('logLimitWrap');
    if (!limitWrap) {
        limitWrap = document.createElement('div');
        limitWrap.id = 'logLimitWrap';
        limitWrap.className = 'log-limit-bar';
        limitWrap.innerHTML = '<label for="logLimit">Show last</label>' +
            '<select id="logLimit" class="inline-select">' +
            [50, 100, 150, 200].map(function(n) { return '<option value="' + n + '">' + n + '</option>'; }).join('') + '</select> logs';
        content.parentElement.insertBefore(limitWrap, content);
        document.getElementById('logLimit').addEventListener('change', function() {
            showLogs(id);
        });
    }
    const limit = document.getElementById('logLimit') ? document.getElementById('logLimit').value : 50;
    try {
        const resp = await fetch(API + '/jobs/' + id + '/logs?limit=' + limit);
        const logs = await resp.json();
        renderLogs(logs);
    } catch (err) {
        content.innerHTML = '<p class="text-error">Failed to load logs.</p>';
    }
}

async function showNetworkList(id) {
    const job = jobs.find(function(item) { return item.id === id; });
    document.getElementById('networkListModalTitle').textContent = 'Current Target List: ' + (job ? job.name : 'Unknown');

    const content = document.getElementById('networkListContent');
    content.innerHTML = '<p class="empty-text">Loading...</p>';
    document.getElementById('networkListModal').classList.remove('hidden');

    try {
        const resp = await fetch(API + '/jobs/' + id + '/target-list');
        const data = await resp.json();
        if (!resp.ok) {
            throw new Error(data.error || 'Failed to load target list');
        }
        renderNetworkList(data);
    } catch (err) {
        content.innerHTML = '<p class="text-error">' + escapeHtml(err.message) + '</p>';
    }
}

function hideNetworkListModal() {
    document.getElementById('networkListModal').classList.add('hidden');
}

function renderNetworkList(networkList) {
    const content = document.getElementById('networkListContent');
    const items = Array.isArray(networkList.items) ? networkList.items : [];
    const typeLabel = networkList.type === 'IPV4_ADDRESSES' ? 'IPv4' : networkList.type === 'IPV6_ADDRESSES' ? 'IPv6' : networkList.type === 'PORTS' ? 'Ports' : (networkList.type || 'Unknown');

    const rows = items.map(function(item) {
        const value = item.value || ((item.start || '') + (item.stop ? ' - ' + item.stop : ''));
        return '<tr>' +
            '<td>' + escapeHtml(item.type || '') + '</td>' +
            '<td class="mono">' + escapeHtml(value) + '</td>' +
        '</tr>';
    }).join('');

    content.innerHTML =
        '<div class="detail-row"><span class="detail-label">List Name</span><span class="detail-value">' + escapeHtml(networkList.name || '-') + '</span></div>' +
        '<div class="detail-row"><span class="detail-label">List ID</span><span class="detail-value mono">' + escapeHtml(networkList.id || '-') + '</span></div>' +
        '<div class="detail-row"><span class="detail-label">Type</span><span class="detail-value">' + escapeHtml(typeLabel) + '</span></div>' +
        '<div class="detail-row"><span class="detail-label">Entries</span><span class="detail-value">' + items.length + '</span></div>' +
        (items.length === 0
            ? '<p class="empty-text">No entries in this target list.</p>'
            : '<table class="logs-table" style="margin-top:0.75rem;"><thead><tr><th>Entry Type</th><th>Value</th></tr></thead><tbody>' + rows + '</tbody></table>');
}

async function previewResolve() {
    const hostnames = document.getElementById('hostnames').value;
    const preview = document.getElementById('resolvePreview');
    if (!hostnames.trim()) {
        preview.classList.add('hidden');
        return;
    }

    preview.textContent = 'Resolving...';
    preview.classList.remove('hidden');

    try {
        const resp = await fetch(API + '/resolve', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({ hostnames: hostnames }),
        });
        const data = await resp.json();
        if (resp.ok) {
            preview.innerHTML = '<strong>' + data.length + ' IPs resolved:</strong><br>' +
                data.map(function(d) { return escapeHtml(d.ip) + ' &larr; ' + escapeHtml(d.hostname); }).join('<br>');
        } else {
            preview.innerHTML = '<span class="text-error">' + escapeHtml(data.error) + '</span>';
        }
    } catch (err) {
        preview.innerHTML = '<span class="text-error">' + escapeHtml(err.message) + '</span>';
    }
}

function renderJobCards(animateCards) {
    const list = document.getElementById('jobList');
    const cardClass = animateCards ? 'job-card job-card-enter' : 'job-card';

    list.classList.remove('hidden');
    document.getElementById('jobsTableWrap').classList.add('hidden');

    list.innerHTML = jobs.map(function(job) {
        const lastResult = job.last_result || '';
        const isSuccess = lastResult.startsWith('success');
        const isError = lastResult.startsWith('error');
        const resultClass = isSuccess ? 'text-success' : isError ? 'text-error' : '';
        const schedulePreset = resolveSchedulePreset(job.schedule);
        const scheduleStatusText = schedulePreset === 'manual'
            ? 'Manual'
            : (job.enabled ? 'Scheduled' : 'Schedule Off');
        const scheduleStatusClass = schedulePreset === 'manual'
            ? 'badge-warning'
            : (job.enabled ? 'badge-success' : 'badge-disabled');
        const targets = Array.isArray(job.targets) ? job.targets : [];
        const endpointCount = targets.length > 0 ? targets.length : 1;

        return '<div class="' + cardClass + '" oncontextmenu="return openJobActionsMenu(event, ' + job.id + ')">' +
            '<div class="job-header">' +
                '<h3>' + escapeHtml(job.name) + '</h3>' +
                '<span class="badge ' + scheduleStatusClass + '">' +
                    scheduleStatusText +
                '</span>' +
            '</div>' +
            '<div class="job-details">' +
                '<div class="detail-row"><span class="detail-label">Primary Endpoint</span><span class="detail-value">' + escapeHtml(job.instance_name || 'Unknown') + '</span></div>' +
                '<div class="detail-row"><span class="detail-label">Primary List</span><span class="detail-value">' + renderPrimaryListValue(job) + '</span></div>' +
                '<div class="detail-row"><span class="detail-label">Endpoints</span><span class="detail-value">' + endpointCount + '</span></div>' +
                '<div class="detail-row"><span class="detail-label">Schedule</span><span class="detail-value">' + scheduleDisplayHtml(job.schedule, !!job.enabled) + '</span></div>' +
                '<div class="detail-row"><span class="detail-label">IP Retention</span><span class="detail-value">' +
                    (job.observed_ip_ttl_hours > 0 ? (escapeHtml(String(job.observed_ip_ttl_hours)) + ' hours') : '<em>Disabled</em>') +
                '</span></div>' +
                (job.last_run_at ?
                    '<div class="detail-row"><span class="detail-label">Last Run</span><span class="detail-value">' + formatTime(job.last_run_at) + '</span></div>' +
                    '<div class="detail-row"><span class="detail-label">Result</span><span class="detail-value ' + resultClass + '">' + escapeHtml(lastResult) + '</span></div>'
                : '<div class="detail-row"><span class="detail-label">Last Run</span><span class="detail-value"><em>Never</em></span></div>') +
            '</div>' +
            '<div class="job-actions">' +
                '<div class="job-actions-desktop">' +
                    '<button class="btn btn-small btn-primary" onclick="runJob(' + job.id + ')">&#9654; Run Now</button>' +
                    '<button class="btn btn-small btn-secondary" onclick="editJob(' + job.id + ')">Edit</button>' +
                    '<button class="btn btn-small btn-secondary" onclick="showNetworkList(' + job.id + ')">View</button>' +
                    '<button class="btn btn-small btn-secondary" onclick="showLogs(' + job.id + ')">Logs</button>' +
                    '<button class="btn btn-small btn-danger" onclick="deleteJob(' + job.id + ')">Delete</button>' +
                '</div>' +
                '<button class="btn btn-small btn-secondary job-actions-menu-btn" onclick="openJobActionsMenuForButton(event, ' + job.id + ')">Actions</button>' +
            '</div>' +
        '</div>';
    }).join('');
}

function renderJobTable() {
    initJobsTableCustomizer();
    var cardList = document.getElementById('jobList');
    var tableWrap = document.getElementById('jobsTableWrap');
    var toolbar = document.getElementById('jobsTableCustomizerToolbar');
    var theadRow = document.getElementById('jobsTableHead');
    var tbody = document.getElementById('jobsTableBody');

    cardList.classList.add('hidden');
    tableWrap.classList.remove('hidden');

    if (!jobsTableCustomizer) return;

    toolbar.innerHTML = jobsTableCustomizer.renderToolbar();
    jobsTableCustomizer.bindToolbarEvents(toolbar);

    theadRow.innerHTML = jobsTableCustomizer.renderHeader();
    jobsTableCustomizer.bindHeaderEvents(theadRow);

    var sortedJobs = jobsTableCustomizer.sortData(jobs);
    tbody.innerHTML = sortedJobs.map(function(job) {
        return '<tr oncontextmenu="return openJobActionsMenu(event, ' + job.id + ')">' + jobsTableCustomizer.renderRow(job, {}) + '</tr>';
    }).join('');
}

function renderJobs(animateCards) {
    const empty = document.getElementById('emptyState');
    if (jobs.length === 0) {
        document.getElementById('jobList').classList.add('hidden');
        document.getElementById('jobsTableWrap').classList.add('hidden');
        empty.classList.remove('hidden');
        return;
    }

    empty.classList.add('hidden');
    if (jobsViewMode === 'table') {
        renderJobTable();
    } else {
        renderJobCards(animateCards);
    }
}

function openJobActionsMenuAt(x, y, jobId) {
    var menu = document.getElementById('jobContextMenu');
    activeJobActionId = jobId;
    menu.classList.remove('hidden');
    menu.style.left = '0px';
    menu.style.top = '0px';

    var rect = menu.getBoundingClientRect();
    var pad = 8;
    var left = x;
    var top = y;

    if (left + rect.width + pad > window.innerWidth) left = window.innerWidth - rect.width - pad;
    if (top + rect.height + pad > window.innerHeight) top = window.innerHeight - rect.height - pad;
    if (left < pad) left = pad;
    if (top < pad) top = pad;

    menu.style.left = left + 'px';
    menu.style.top = top + 'px';
}

function openJobActionsMenu(event, jobId) {
    event.preventDefault();
    event.stopPropagation();
    openJobActionsMenuAt(event.clientX, event.clientY, jobId);
    return false;
}

function openJobActionsMenuForButton(event, jobId) {
    event.preventDefault();
    event.stopPropagation();
    var rect = event.currentTarget.getBoundingClientRect();
    openJobActionsMenuAt(rect.right - 180, rect.bottom + 6, jobId);
}

function hideJobActionsMenu() {
    var menu = document.getElementById('jobContextMenu');
    menu.classList.add('hidden');
    activeJobActionId = null;
}

function toggleHeaderMobileMenu(event) {
    event.preventDefault();
    event.stopPropagation();
    var menu = document.getElementById('headerMobileMenu');
    if (!menu.classList.contains('hidden')) {
        hideHeaderMobileMenu();
        return;
    }

    var btn = event.currentTarget;
    var rect = btn.getBoundingClientRect();
    menu.classList.remove('hidden');
    menu.style.left = '0px';
    menu.style.top = '0px';

    var menuRect = menu.getBoundingClientRect();
    var pad = 8;
    var left = rect.right - menuRect.width;
    var top = rect.bottom + 6;

    if (left + menuRect.width + pad > window.innerWidth) left = window.innerWidth - menuRect.width - pad;
    if (top + menuRect.height + pad > window.innerHeight) top = window.innerHeight - menuRect.height - pad;
    if (left < pad) left = pad;
    if (top < pad) top = pad;

    menu.style.left = left + 'px';
    menu.style.top = top + 'px';
}

function hideHeaderMobileMenu() {
    document.getElementById('headerMobileMenu').classList.add('hidden');
}

function triggerJobActionFromMenu(action) {
    var jobId = activeJobActionId;
    hideJobActionsMenu();
    if (!jobId) return;

    if (action === 'run') {
        runJob(jobId);
    } else if (action === 'edit') {
        editJob(jobId);
    } else if (action === 'view') {
        showNetworkList(jobId);
    } else if (action === 'logs') {
        showLogs(jobId);
    } else if (action === 'delete') {
        deleteJob(jobId);
    }
}

function renderLogs(logs) {
    const content = document.getElementById('logsContent');
    logDetailsMap.clear();

    if (logs.length === 0) {
        content.innerHTML = '<p class="empty-text">No run history yet. Click "Run Now" to trigger a sync.</p>';
        return;
    }

    logs.forEach(function(log) {
        if (log.details) {
            logDetailsMap.set(log.id, log.details);
        }
    });

    content.innerHTML =
        '<table class="logs-table"><thead><tr>' +
        '<th>Started</th><th>Status</th><th>Changes</th><th>Message</th><th>Details</th>' +
        '</tr></thead><tbody>' +
        logs.map(function(log) {
            const badgeClass = log.status === 'success' ? 'badge-success' : log.status === 'running' ? 'badge-warning' : 'badge-error';
            return '<tr>' +
                '<td>' + formatTime(log.started_at) + '</td>' +
                '<td><span class="badge ' + badgeClass + '">' + escapeHtml(log.status) + '</span></td>' +
                '<td>' + log.changes_made + '</td>' +
                '<td>' + escapeHtml(log.message) + '</td>' +
                '<td>' + (log.details ?
                    '<button class="btn btn-small btn-secondary" onclick="toggleDetails(' + log.id + ', this)">Show</button>'
                    : '-') +
                '</td></tr>';
        }).join('') +
        '</tbody></table>';
}

function toggleDetails(logId, btn) {
    const details = logDetailsMap.get(logId);
    if (!details) return;
    const pre = document.createElement('pre');
    pre.className = 'details-block';
    pre.textContent = details;
    btn.parentElement.appendChild(pre);
    btn.remove();
}

function showJobModal(job) {
    const modal = document.getElementById('jobModal');
    const title = document.getElementById('jobModalTitle');

    loadControllers().then(function() {
        populateControllerDropdown(job ? job.instance_id : '');
        document.getElementById('additionalTargetsRows').innerHTML = '';

        if (job) {
            title.textContent = 'Edit Sync Job';
            document.getElementById('jobId').value = job.id;
            document.getElementById('jobName').value = job.name;
            document.getElementById('hostnames').value = job.hostnames;
            document.getElementById('schedule').value = job.schedule || '';
            document.getElementById('schedulePreset').value = resolveSchedulePreset(job.schedule);
            document.getElementById('observedIpRetentionEnabled').checked = job.observed_ip_ttl_hours > 0;
            document.getElementById('observedIpTtlHours').value = job.observed_ip_ttl_hours > 0 ? job.observed_ip_ttl_hours : 168;
            document.getElementById('enabled').checked = job.enabled;
            var targets = Array.isArray(job.targets) ? job.targets : [];
            var additional = [];
            if (targets.length > 0) {
                for (var i = 1; i < targets.length; i++) {
                    additional.push(targets[i]);
                }
            }
            for (var a = 0; a < additional.length; a++) {
                addAdditionalTargetRow(additional[a]);
            }
            onControllerChange().then(function() {
                document.getElementById('networkListId').value = job.target_list_id;
            });
        } else {
            title.textContent = 'New Sync Job';
            document.getElementById('jobForm').reset();
            document.getElementById('jobId').value = '';
            document.getElementById('schedulePreset').value = 'manual';
            document.getElementById('schedule').value = '';
            document.getElementById('observedIpRetentionEnabled').checked = true;
            document.getElementById('observedIpTtlHours').value = 168;
            document.getElementById('enabled').checked = true;
            document.getElementById('networkListId').innerHTML = '<option value="">Select an endpoint first...</option>';
        }

        onSchedulePresetChange();
        onObservedIpRetentionToggle();
        document.getElementById('resolvePreview').classList.add('hidden');
        modal.classList.remove('hidden');
    });
}

function hideJobModal() {
    document.getElementById('jobModal').classList.add('hidden');
}

function hideLogsModal() {
    document.getElementById('logsModal').classList.add('hidden');
}

function editJob(id) {
    const job = jobs.find(function(item) { return item.id === id; });
    if (job) showJobModal(job);
}

function escapeHtml(str) {
    if (!str) return '';
    const div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

function formatTime(isoStr) {
    if (!isoStr) return '-';
    try {
        return new Date(isoStr).toLocaleString();
    } catch (e) {
        return isoStr;
    }
}

let toastTimer;
function showToast(message, type) {
    const toast = document.getElementById('toast');
    toast.textContent = message;
    toast.className = 'toast toast-' + type;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function() {
        toast.className = 'toast hidden';
    }, 3500);
}

let dnsServers = [];

async function loadDNSServers() {
    try {
        const resp = await fetch(API + '/dns-servers');
        if (!resp.ok) throw new Error('Failed to load DNS servers');
        dnsServers = await resp.json();
    } catch (err) {
        console.error('Load DNS servers error:', err);
    }
}

function showDNSModal() {
    loadDNSServers().then(function() {
        renderDNSTable();
        hideDNSServerForm();
        document.getElementById('dnsModal').classList.remove('hidden');
    });
}

function hideDNSModal() {
    document.getElementById('dnsModal').classList.add('hidden');
}

function renderDNSTable() {
    const tbody = document.getElementById('dnsTableBody');
    const noMsg = document.getElementById('noDNSMsg');
    if (dnsServers.length === 0) {
        tbody.innerHTML = '';
        noMsg.classList.remove('hidden');
        return;
    }
    noMsg.classList.add('hidden');
    tbody.innerHTML = dnsServers.map(function(s) {
        return '<tr>' +
            '<td>' + escapeHtml(s.name) + '</td>' +
            '<td class="mono">' + escapeHtml(s.address) + '</td>' +
            '<td>' + (s.enabled ? '<span class="badge badge-success">Enabled</span>' : '<span class="badge badge-disabled">Disabled</span>') + '</td>' +
            '<td style="white-space:nowrap">' +
                '<button class="btn btn-small btn-secondary" onclick="editDNSServer(' + s.id + ')">Edit</button> ' +
                '<button class="btn btn-small btn-danger" onclick="deleteDNSServer(' + s.id + ')">Delete</button>' +
            '</td></tr>';
    }).join('');
}

function showDNSServerForm(srv) {
    document.getElementById('dnsFormWrap').classList.remove('hidden');
    if (srv) {
        document.getElementById('dnsId').value = srv.id;
        document.getElementById('dnsName').value = srv.name;
        document.getElementById('dnsAddress').value = srv.address;
        document.getElementById('dnsEnabled').checked = !!srv.enabled;
    } else {
        document.getElementById('dnsForm').reset();
        document.getElementById('dnsId').value = '';
        document.getElementById('dnsEnabled').checked = true;
    }
}

function hideDNSServerForm() {
    document.getElementById('dnsFormWrap').classList.add('hidden');
}

function editDNSServer(id) {
    var dnsServer = dnsServers.find(function(item) { return item.id === id; });
    if (dnsServer) showDNSServerForm(dnsServer);
}

async function saveDNSServer(event) {
    event.preventDefault();
    var id = document.getElementById('dnsId').value;
    var data = {
        name: document.getElementById('dnsName').value,
        address: document.getElementById('dnsAddress').value,
        enabled: document.getElementById('dnsEnabled').checked,
    };
    var url = id ? API + '/dns-servers/' + id : API + '/dns-servers';
    var method = id ? 'PUT' : 'POST';
    try {
        var resp = await fetch(url, {
            method: method,
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(data),
        });
        if (!resp.ok) {
            var err = await resp.json();
            throw new Error(err.error || 'Request failed');
        }
        await loadDNSServers();
        renderDNSTable();
        hideDNSServerForm();
        showToast(id ? 'DNS server updated' : 'DNS server added', 'success');
        checkHealth();
    } catch (err) {
        showToast(err.message, 'error');
    }
}

async function deleteDNSServer(id) {
    if (!confirm('Delete this DNS server?')) return;
    try {
        var resp = await fetch(API + '/dns-servers/' + id, { method: 'DELETE' });
        if (!resp.ok) throw new Error('Delete failed');
        await loadDNSServers();
        renderDNSTable();
        showToast('DNS server deleted', 'success');
        checkHealth();
    } catch (err) {
        showToast(err.message, 'error');
    }
}

document.addEventListener('click', function() {
    hideJobActionsMenu();
    hideHeaderMobileMenu();
});

window.addEventListener('resize', function() {
    hideJobActionsMenu();
    hideHeaderMobileMenu();
});

window.addEventListener('scroll', function() {
    hideJobActionsMenu();
    hideHeaderMobileMenu();
}, true);

document.addEventListener('DOMContentLoaded', init);