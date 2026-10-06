const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

function harness() {
    const elements = new Map();
    const document = {
        getElementById(id) {
            if (!elements.has(id)) {
                const hidden = new Set(['hidden']);
                elements.set(id, {
                    value: '', html: '', textContent: '', children: [], inputs: [],
                    get innerHTML() { return this.html; },
                    set innerHTML(value) {
                        this.html = value;
                        this.children = Array.from(value.matchAll(/<label\b/g), () => ({}));
                    },
                    classList: {
                        add: (name) => hidden.add(name), remove: (name) => hidden.delete(name),
                        contains: (name) => hidden.has(name),
                    },
                    querySelectorAll() { return this.inputs.filter((input) => input.checked); },
                    appendChild(child) { this.children.push(child); }, focus() {}, reset() {},
                });
            }
            return elements.get(id);
        },
        createElement() { return { append(...children) { this.children = children; } }; },
        createTextNode(text) { return text; },
    };
    const calls = [];
    const context = vm.createContext({
        document, API: '/api', ensureAdminAction: () => true,
        escapeHtml: (value) => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;'),
        showToast: (message, type) => calls.push({message, type}),
        fetch: async () => ({ok: true, json: async () => []}), confirm: () => true,
    });
    vm.runInContext(fs.readFileSync(path.join(__dirname, 'source-lists.js'), 'utf8'), context);
    return {context, document, calls, run: (code) => vm.runInContext(code, context)};
}

test('picker excludes self and indirect ancestors, retains descendants', () => {
    const h = harness();
    h.run(`sourceLists = [
        {id:1,name:'RFC1918',included_list_ids:[]},
        {id:2,name:'API',included_list_ids:[1]},
        {id:3,name:'Combined',included_list_ids:[2]},
        {id:4,name:'Other',included_list_ids:[]}
    ]; renderSourcePicker('nestedSourceLists', [1], 2);`);
    const html = h.document.getElementById('nestedSourceLists').innerHTML;
    assert.match(html, /value="1" checked/);
    assert.match(html, /value="4"/);
    assert.doesNotMatch(html, /value="[23]"/);
});

test('RFC1918 preset supplies exactly three private IPv4 ranges', () => {
    const h = harness();
    h.run('editSourceList(null, true)');
    assert.equal(h.document.getElementById('sourceListName').value, 'RFC1918');
    assert.equal(h.document.getElementById('sourceListHostnames').value, '10.0.0.0/8\n172.16.0.0/12\n192.168.0.0/16');
    assert.equal(h.document.getElementById('sourceListForm').classList.contains('hidden'), false);
});

test('source names and entries are escaped in rendered cards and picker', () => {
    const h = harness();
    h.run(`sourceLists = [{id:1,name:'<img src=x>',hostnames:'<script>bad</script>',included_list_ids:[]}];
        renderSourceListCards(); renderSourcePicker('jobSourceLists', []);`);
    assert.match(h.document.getElementById('sourceListsContent').innerHTML, /&lt;script&gt;/);
    assert.doesNotMatch(h.document.getElementById('sourceListsContent').innerHTML, /<img|<script/);
    assert.doesNotMatch(h.document.getElementById('jobSourceLists').innerHTML, /<img/);
});

test('missing selected references remain selected until explicitly removed', () => {
    const h = harness();
    h.run('renderSourcePicker("jobSourceLists", [99])');
    const missing = h.document.getElementById('jobSourceLists').children[0];
    assert.equal(missing.children[0].value, 99);
    assert.equal(missing.children[0].checked, true);
});

test('source save supports nested-only input and sends numeric IDs', async () => {
    const h = harness();
    h.document.getElementById('sourceListName').value = 'Combined';
    h.document.getElementById('nestedSourceLists').inputs = [{value: '1', checked: true}];
    let sent;
    h.context.fetch = async (url, options) => {
        if (options) sent = {url, ...JSON.parse(options.body)};
        return {ok: true, json: async () => [{id: 1, name: 'RFC1918', included_list_ids: []}]};
    };
    await h.run('saveSourceList({preventDefault(){}})');
    assert.deepEqual(sent, {url: '/api/source-lists', name: 'Combined', hostnames: '', included_list_ids: [1]});
    assert.equal(h.calls.at(-1).type, 'success');
});

test('empty sources fail validation and API errors preserve editor input', async () => {
    const h = harness();
    await h.run('saveSourceList({preventDefault(){}})');
    assert.equal(h.calls.at(-1).message, 'Add entries or include another source');
    h.document.getElementById('sourceListName').value = 'Office';
    h.document.getElementById('sourceListHostnames').value = '10.0.0.0/8';
    h.context.fetch = async () => ({ok: false, json: async () => ({error: 'source list cycle detected'})});
    await h.run('saveSourceList({preventDefault(){}})');
    assert.equal(h.calls.at(-1).message, 'source list cycle detected');
    assert.equal(h.document.getElementById('sourceListHostnames').value, '10.0.0.0/8');
});

test('job preview resolves selected sources even without inline entries', async () => {
    const h = harness();
    const app = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
    const previewFunction = app.slice(app.indexOf('async function previewResolve()'), app.indexOf('function renderJobCards('));
    h.context.isAdminUser = true;
    h.context.console = console;
    vm.runInContext(previewFunction, h.context);
    h.document.getElementById('jobSourceLists').inputs = [{value: '7', checked: true}];
    let payload;
    h.context.fetch = async (_url, options) => {
        payload = JSON.parse(options.body);
        return {ok: true, json: async () => [{ip: '10.0.0.0/8', hostname: '10.0.0.0/8'}]};
    };
    await h.run('previewResolve()');
    assert.deepEqual(payload, {hostnames: '', included_list_ids: [7]});
    assert.match(h.document.getElementById('resolvePreview').innerHTML, /10\.0\.0\.0\/8/);
});