const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function harness() {
    const elements = new Map();
    const document = {
        getElementById(id) {
            if (!elements.has(id)) {
                elements.set(id, {innerHTML: '', textContent: '', value: '50', scrollTop: 123,
                    classList: {remove() {}}, children: [], appendChild(child) { this.children.push(child); }});
            }
            return elements.get(id);
        },
        createElement() { return {className: '', textContent: ''}; },
    };
    const context = vm.createContext({
        document, API: '/api', jobs: [{id: 1, name: 'First'}, {id: 2, name: 'Second'}],
        activeLogsJobId: null, logsLoadSeq: 0,
        escapeHtml: value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;'),
        formatTime: value => value,
        fetch: async () => ({ok: true, json: async () => []}), showToast() {},
    });
    const app = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
    vm.runInContext(app.slice(app.indexOf('async function showLogs('), app.indexOf('async function showNetworkList(')), context);
    vm.runInContext(app.slice(app.indexOf('function renderLogs('), app.indexOf('function showJobModal(')), context);
    return {context, document, run: code => vm.runInContext(code, context)};
}

function sampleLog(extra = {}) {
    return {id: 4, job_id: 1, started_at: '2026-10-09', status: 'success', changes_made: 2471,
        message: 'Sync finished', stats: {input_entries: 13850, output_entries: 8066, entries_saved: 5784},
        details: 'Diagnostic output', targets: [{id: 9, label: 'UniFi target', entry_count: 8066}], ...extra};
}

test('history renders compact stats, responsive labels and native collapsible details', () => {
    const h = harness();
    h.context.logs = [sampleLog()];
    h.run('renderLogs(logs)');
    const html = h.document.getElementById('logsContent').innerHTML;
    assert.match(html, /run-history-table/);
    for (const label of ['Started', 'Status', 'Changes', 'Message', 'Optimization', 'Details / Sent Lists']) {
        assert.ok(html.includes('data-label="' + label + '"'));
    }
    assert.match(html, /<dl class="logs-stats">/);
    assert.match(html, /<details class="logs-details"><summary>Run details<\/summary>/);
    assert.match(html, /View sent list <span class="logs-target-count">/);
    assert.match(html, /viewRunTargetSnapshot\(1,4,9, this\)/);
    assert.equal(h.document.getElementById('logsSummary').textContent, '1 run · Newest first');
});

test('history escapes messages, target labels, status and diagnostics', () => {
    const h = harness();
    h.context.logs = [sampleLog({message: '<img src=x>', details: '</pre><script>bad</script>',
        status: '<svg>', targets: [{id: 9, label: '<img src=x>', entry_count: 0}]})];
    h.run('renderLogs(logs)');
    const html = h.document.getElementById('logsContent').innerHTML;
    assert.doesNotMatch(html, /<img|<script|<svg/);
    assert.match(html, /&lt;img src=x&gt;/);
    assert.match(html, /&lt;\/pre&gt;&lt;script&gt;/);
});

test('missing stats and actions remain readable; empty history shows guidance', () => {
    const h = harness();
    h.context.logs = [sampleLog({stats: null, details: '', targets: null})];
    h.run('renderLogs(logs)');
    assert.match(h.document.getElementById('logsContent').innerHTML, /No optimization stats/);
    assert.match(h.document.getElementById('logsContent').innerHTML, /No details available/);
    h.run('renderLogs([])');
    assert.match(h.document.getElementById('logsContent').innerHTML, /No run history yet/);
    assert.equal(h.document.getElementById('logsSummary').textContent, '0 runs · Newest first');
});

test('changing limit after switching jobs reloads the active job and resets scroll', async () => {
    const h = harness();
    const urls = [];
    h.context.fetch = async url => { urls.push(url); return {ok: true, json: async () => []}; };
    await h.run('showLogs(1)');
    await h.run('showLogs(2)');
    h.document.getElementById('logLimit').value = '100';
    await h.run('showLogs(activeLogsJobId)');
    assert.equal(urls.at(-1), '/api/jobs/2/logs?limit=100');
    assert.equal(h.document.getElementById('logsModalTitle').textContent, 'Run History: Second');
    assert.equal(h.document.getElementById('logsContent').scrollTop, 0);
});

test('late responses cannot replace the currently selected job', async () => {
    const h = harness();
    let resolveFirst;
    h.context.fetch = url => url.includes('/jobs/1/')
        ? new Promise(resolve => { resolveFirst = resolve; })
        : Promise.resolve({ok: true, json: async () => [sampleLog({message: 'Second job'})]});
    const first = h.run('showLogs(1)');
    await h.run('showLogs(2)');
    resolveFirst({ok: true, json: async () => [sampleLog({message: 'Old job'})]});
    await first;
    assert.match(h.document.getElementById('logsContent').innerHTML, /Second job/);
    assert.doesNotMatch(h.document.getElementById('logsContent').innerHTML, /Old job/);
});

test('failed HTTP responses show error state instead of attempting to render', async () => {
    const h = harness();
    h.context.fetch = async () => ({ok: false});
    await h.run('showLogs(1)');
    assert.equal(h.document.getElementById('logsSummary').textContent, 'Unable to load runs');
    assert.match(h.document.getElementById('logsContent').innerHTML, /Failed to load logs/);
});

test('sent snapshot remains attached to its own target with literal diagnostic text', async () => {
    const h = harness();
    const parent = {children: [], appendChild(child) { this.children.push(child); }};
    h.context.button = {parentElement: parent, disabled: false, textContent: 'View sent list'};
    h.context.fetch = async () => ({ok: true, json: async () => ({name: '<Target>', type: 'IPV4_ADDRESSES',
        items: [{type: 'IPV4_ADDRESS', value: '192.0.2.1'}]})});
    await h.run('viewRunTargetSnapshot(1, 4, 9, button)');
    assert.equal(parent.children[0].textContent, '<Target> (IPV4_ADDRESSES)\n\nIPV4_ADDRESS 192.0.2.1');
    assert.equal(h.context.button.textContent, 'Sent list shown');
});