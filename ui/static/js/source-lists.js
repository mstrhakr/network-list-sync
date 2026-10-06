let sourceLists = [];

async function loadSourceLists() {
    const response = await fetch(API + '/source-lists');
    if (!response.ok) throw new Error('Failed to load reusable sources');
    sourceLists = await response.json();
}

function selectedSourceIDs(containerID) {
    return Array.from(document.getElementById(containerID).querySelectorAll('input:checked'))
        .map(function(input) { return Number(input.value); });
}

function sourceReaches(sourceID, targetID, visited) {
    if (sourceID === targetID) return true;
    visited = visited || new Set();
    if (visited.has(sourceID)) return false;
    visited.add(sourceID);
    const source = sourceLists.find(function(item) { return item.id === sourceID; });
    return !!source && (source.included_list_ids || []).some(function(id) {
        return sourceReaches(id, targetID, visited);
    });
}

function renderSourcePicker(containerID, selectedIDs, editingID) {
    const selected = new Set(selectedIDs || []);
    const container = document.getElementById(containerID);
    const available = sourceLists.filter(function(source) {
        return !editingID || !sourceReaches(source.id, editingID);
    });
    container.innerHTML = available.map(function(source) {
        return '<label class="source-choice"><input type="checkbox" value="' + source.id + '"' +
            (selected.has(source.id) ? ' checked' : '') + '> <span>' + escapeHtml(source.name) +
            '<small>' + (source.included_list_ids || []).length + ' nested sources</small></span></label>';
    }).join('');
    // Never silently drop references if another admin removes a source mid-edit.
    selected.forEach(function(id) {
        if (!sourceLists.some(function(source) { return source.id === id; })) {
            const label = document.createElement('label');
            label.className = 'source-choice text-error';
            const input = document.createElement('input');
            input.type = 'checkbox';
            input.value = id;
            input.checked = true;
            label.append(input, document.createTextNode(' Missing source #' + id + ' — deselect to remove'));
            container.appendChild(label);
        }
    });
    if (!container.children.length) {
        container.innerHTML = '<p class="hint">No sources available. Create a source or use the RFC1918 preset.</p>';
    }
}

async function showSourceListsModal() {
    if (!ensureAdminAction()) return;
    try {
        await loadSourceLists();
        renderSourceListCards();
        cancelSourceListEdit();
        document.getElementById('sourceListsModal').classList.remove('hidden');
    } catch (err) {
        showToast(err.message, 'error');
    }
}

function hideSourceListsModal() {
    document.getElementById('sourceListsModal').classList.add('hidden');
}

function renderSourceListCards() {
    document.getElementById('sourceListsContent').innerHTML = sourceLists.length ? sourceLists.map(function(source) {
        const nestedNames = (source.included_list_ids || []).map(function(id) {
            const child = sourceLists.find(function(item) { return item.id === id; });
            return child ? child.name : 'Missing #' + id;
        });
        return '<div class="source-list-card"><div><strong>' + escapeHtml(source.name) + '</strong>' +
            (nestedNames.length ? '<p class="hint">Includes: ' + escapeHtml(nestedNames.join(', ')) + '</p>' : '') +
            '<pre class="source-entry-summary">' + escapeHtml(source.hostnames || '(Nested sources only)') + '</pre></div>' +
            '<div class="source-manager-actions"><button class="btn btn-small btn-secondary" type="button" onclick="editSourceList(' + source.id + ')">Edit</button>' +
            '<button class="btn btn-small btn-danger" type="button" onclick="deleteSourceList(' + source.id + ')">Delete</button></div></div>';
    }).join('') : '<p class="empty-text">No reusable sources yet. Start with RFC1918 or create your own.</p>';
}

function editSourceList(id, preset) {
    if (!ensureAdminAction()) return;
    const source = sourceLists.find(function(item) { return item.id === id; });
    document.getElementById('sourceListId').value = source ? source.id : '';
    document.getElementById('sourceListName').value = source ? source.name : (preset ? 'RFC1918' : '');
    document.getElementById('sourceListHostnames').value = source ? source.hostnames :
        (preset ? '10.0.0.0/8\n172.16.0.0/12\n192.168.0.0/16' : '');
    document.getElementById('sourceEditorTitle').textContent = source ? 'Edit Source' : 'New Source';
    renderSourcePicker('nestedSourceLists', source ? source.included_list_ids : [], source ? source.id : null);
    document.getElementById('sourceListForm').classList.remove('hidden');
    document.getElementById('sourceListName').focus();
}

function cancelSourceListEdit() {
    document.getElementById('sourceListForm').classList.add('hidden');
    document.getElementById('sourceListForm').reset();
}

async function refreshSourceListUI() {
    const jobSelected = selectedSourceIDs('jobSourceLists');
    await loadSourceLists();
    renderSourceListCards();
    renderSourcePicker('jobSourceLists', jobSelected);
    document.getElementById('resolvePreview').classList.add('hidden');
}

async function saveSourceList(event) {
    event.preventDefault();
    if (!ensureAdminAction()) return;
    const id = document.getElementById('sourceListId').value;
    const data = {
        name: document.getElementById('sourceListName').value.trim(),
        hostnames: document.getElementById('sourceListHostnames').value,
        included_list_ids: selectedSourceIDs('nestedSourceLists'),
    };
    if (!data.hostnames.trim() && !data.included_list_ids.length) {
        showToast('Add entries or include another source', 'error');
        return;
    }
    try {
        const response = await fetch(API + '/source-lists' + (id ? '/' + id : ''), {
            method: id ? 'PUT' : 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(data),
        });
        if (!response.ok) {
            const error = await response.json();
            throw new Error(error.error || 'Save failed');
        }
        cancelSourceListEdit();
        await refreshSourceListUI();
        showToast('Source saved. Dependent jobs use changes on their next run.', 'success');
    } catch (err) {
        showToast(err.message, 'error');
    }
}

async function deleteSourceList(id) {
    if (!ensureAdminAction()) return;
    if (!confirm('Delete this reusable source? Sources used by jobs or other sources cannot be deleted.')) return;
    try {
        const response = await fetch(API + '/source-lists/' + id, { method: 'DELETE' });
        if (!response.ok) {
            const error = await response.json();
            throw new Error(error.error || 'Delete failed');
        }
        if (Number(document.getElementById('sourceListId').value) === id) cancelSourceListEdit();
        await refreshSourceListUI();
        if (!document.getElementById('sourceListForm').classList.contains('hidden')) {
            renderSourcePicker('nestedSourceLists', selectedSourceIDs('nestedSourceLists'), Number(document.getElementById('sourceListId').value));
        }
        showToast('Source deleted', 'success');
    } catch (err) {
        showToast(err.message, 'error');
    }
}