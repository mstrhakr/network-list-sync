/*
 * Adapted from PrintMaster table customizer patterns.
 * Provides column visibility, sort, and persisted table config.
 */
(function () {
    'use strict';

    function escapeHtml(str) {
        if (str === null || str === undefined) return '';
        const div = document.createElement('div');
        div.textContent = String(str);
        return div.innerHTML;
    }

    class TableCustomizer {
        constructor(tableId, options) {
            this.tableId = tableId;
            this.storageKey = (options && options.storageKey) || ('nls_table_config_' + tableId);
            this.options = {
                columnDefs: (options && options.columnDefs) || [],
                persistConfig: !(options && options.persistConfig === false),
                onColumnChange: (options && options.onColumnChange) || null,
                onSort: (options && options.onSort) || null,
            };

            this.columns = [];
            this.sortState = { key: null, dir: 'asc' };
            this._loadConfig();
        }

        _loadConfig() {
            let savedConfig = null;
            if (this.options.persistConfig) {
                try {
                    const raw = localStorage.getItem(this.storageKey);
                    if (raw) savedConfig = JSON.parse(raw);
                } catch (e) {
                    // ignore bad localStorage payloads
                }
            }

            this.columns = this.options.columnDefs.map((def, index) => {
                const saved = savedConfig && Array.isArray(savedConfig.columns)
                    ? savedConfig.columns.find((c) => c.id === def.id)
                    : null;
                return {
                    id: def.id,
                    label: def.label,
                    sortKey: def.sortKey || '',
                    hideable: def.hideable !== false,
                    visible: saved ? !!saved.visible : !def.defaultHidden,
                    order: saved && Number.isInteger(saved.order) ? saved.order : index,
                    render: def.render,
                };
            });

            this.columns.sort((a, b) => a.order - b.order);
            if (savedConfig && savedConfig.sortState) {
                this.sortState = {
                    key: savedConfig.sortState.key || null,
                    dir: savedConfig.sortState.dir === 'desc' ? 'desc' : 'asc',
                };
            }
        }

        _saveConfig() {
            if (!this.options.persistConfig) return;
            try {
                const payload = {
                    columns: this.columns.map((col, index) => ({
                        id: col.id,
                        visible: !!col.visible,
                        order: index,
                    })),
                    sortState: this.sortState,
                };
                localStorage.setItem(this.storageKey, JSON.stringify(payload));
            } catch (e) {
                // ignore localStorage errors
            }
        }

        getVisibleColumns() {
            return this.columns.filter((c) => c.visible);
        }

        toggleColumn(columnId, visible) {
            const col = this.columns.find((c) => c.id === columnId);
            if (!col || !col.hideable) return;
            col.visible = visible;
            this._saveConfig();
            if (this.options.onColumnChange) this.options.onColumnChange(this.columns);
        }

        resetToDefaults() {
            try {
                localStorage.removeItem(this.storageKey);
            } catch (e) {
                // ignore localStorage errors
            }
            this._loadConfig();
            if (this.options.onColumnChange) this.options.onColumnChange(this.columns);
        }

        setSort(columnId) {
            const col = this.columns.find((c) => c.id === columnId);
            if (!col || !col.sortKey) return;
            if (this.sortState.key === col.sortKey) {
                this.sortState.dir = this.sortState.dir === 'asc' ? 'desc' : 'asc';
            } else {
                this.sortState.key = col.sortKey;
                this.sortState.dir = 'asc';
            }
            this._saveConfig();
            if (this.options.onSort) this.options.onSort(this.sortState);
        }

        sortData(data) {
            const out = Array.isArray(data) ? data.slice() : [];
            if (!this.sortState.key) return out;
            const key = this.sortState.key;
            const dir = this.sortState.dir;

            out.sort((a, b) => {
                const left = this._valueForSort(a, key);
                const right = this._valueForSort(b, key);

                if (left === right) return 0;
                if (left === null || left === undefined || left === '') return 1;
                if (right === null || right === undefined || right === '') return -1;

                if (typeof left === 'number' && typeof right === 'number') {
                    return dir === 'asc' ? left - right : right - left;
                }

                const leftText = String(left).toLowerCase();
                const rightText = String(right).toLowerCase();
                if (leftText < rightText) return dir === 'asc' ? -1 : 1;
                if (leftText > rightText) return dir === 'asc' ? 1 : -1;
                return 0;
            });

            return out;
        }

        _valueForSort(item, sortKey) {
            if (sortKey === 'target_count') {
                const targets = Array.isArray(item.targets) ? item.targets.length : 0;
                return targets > 0 ? targets : 1;
            }
            if (sortKey === 'last_run_at') {
                return item.last_run_at ? Date.parse(item.last_run_at) || 0 : 0;
            }
            return item[sortKey];
        }

        renderToolbar() {
            const visibleCount = this.getVisibleColumns().length;
            return '' +
                '<div class="table-customizer-toolbar">' +
                    '<div class="table-customizer-left">' +
                        '<button class="btn btn-small btn-secondary table-column-toggle-btn" type="button">Columns <span class="column-count-badge">' + visibleCount + '</span></button>' +
                        '<button class="btn btn-small btn-secondary table-reset-btn" type="button">Reset</button>' +
                    '</div>' +
                '</div>';
        }

        renderColumnPicker() {
            const visible = this.columns.filter((c) => c.visible);
            const hidden = this.columns.filter((c) => !c.visible);
            const renderItems = (items) => items.map((col) => {
                const checked = col.visible ? ' checked' : '';
                const disabled = col.hideable ? '' : ' disabled';
                return '' +
                    '<label class="column-picker-item" data-column-id="' + escapeHtml(col.id) + '">' +
                        '<input type="checkbox" data-column-id="' + escapeHtml(col.id) + '"' + checked + disabled + '>' +
                        '<span>' + escapeHtml(col.label) + '</span>' +
                    '</label>';
            }).join('');

            return '' +
                '<div class="column-picker-dropdown">' +
                    '<div class="column-picker-header">' +
                        '<span>Customize Columns</span>' +
                        '<button class="column-picker-close" type="button">x</button>' +
                    '</div>' +
                    '<div class="column-picker-body">' +
                        '<div class="column-picker-group">' +
                            '<div class="column-picker-group-title">Visible</div>' +
                            '<div class="column-picker-list" data-group="visible">' + renderItems(visible) + '</div>' +
                        '</div>' +
                        '<div class="column-picker-group">' +
                            '<div class="column-picker-group-title">Available</div>' +
                            '<div class="column-picker-list" data-group="available">' + renderItems(hidden) + '</div>' +
                        '</div>' +
                    '</div>' +
                '</div>';
        }

        renderHeader() {
            return this.getVisibleColumns().map((col) => {
                const isSorted = col.sortKey && this.sortState.key === col.sortKey;
                const sortableClass = col.sortKey ? 'sortable' : '';
                const sortIndicator = isSorted
                    ? '<span class="sort-indicator ' + this.sortState.dir + '">' + (this.sortState.dir === 'asc' ? '↑' : '↓') + '</span>'
                    : '';
                return '' +
                    '<th data-column-id="' + escapeHtml(col.id) + '" data-sort-key="' + escapeHtml(col.sortKey || '') + '" class="' + sortableClass + (isSorted ? ' sorted' : '') + '">' +
                        '<div class="th-content"><span class="th-label">' + escapeHtml(col.label) + '</span>' + sortIndicator + '</div>' +
                    '</th>';
            }).join('');
        }

        renderRow(item, meta) {
            return this.getVisibleColumns().map((col) => {
                let content = '';
                try {
                    content = col.render ? col.render(item, meta || {}) : '';
                } catch (e) {
                    content = '-';
                }
                return '<td data-column-id="' + escapeHtml(col.id) + '">' + content + '</td>';
            }).join('');
        }

        bindToolbarEvents(toolbarElement) {
            if (!toolbarElement) return;
            const toggleBtn = toolbarElement.querySelector('.table-column-toggle-btn');
            const resetBtn = toolbarElement.querySelector('.table-reset-btn');

            if (toggleBtn) {
                toggleBtn.addEventListener('click', (e) => {
                    e.stopPropagation();
                    this._showColumnPicker(toggleBtn);
                });
            }
            if (resetBtn) {
                resetBtn.addEventListener('click', () => {
                    if (confirm('Reset table columns to defaults?')) {
                        this.resetToDefaults();
                    }
                });
            }
        }

        bindHeaderEvents(theadElement) {
            if (!theadElement) return;
            theadElement.querySelectorAll('th.sortable').forEach((th) => {
                th.addEventListener('click', () => {
                    const columnId = th.getAttribute('data-column-id');
                    this.setSort(columnId);
                });
            });
        }

        _showColumnPicker(anchorElement) {
            const existing = document.querySelector('.column-picker-dropdown');
            if (existing) {
                existing.remove();
                return;
            }

            const wrapper = document.createElement('div');
            wrapper.innerHTML = this.renderColumnPicker();
            const dropdown = wrapper.firstElementChild;
            document.body.appendChild(dropdown);

            const rect = anchorElement.getBoundingClientRect();
            const width = 320;
            const pad = 8;
            let left = rect.left;
            let top = rect.bottom + 6;

            if (left + width + pad > window.innerWidth) {
                left = window.innerWidth - width - pad;
            }
            if (left < pad) left = pad;
            if (top + 480 > window.innerHeight) top = Math.max(pad, rect.top - 360);

            dropdown.style.left = left + 'px';
            dropdown.style.top = top + 'px';

            dropdown.querySelector('.column-picker-close').addEventListener('click', () => dropdown.remove());
            dropdown.querySelectorAll('input[type="checkbox"]').forEach((cb) => {
                cb.addEventListener('change', () => {
                    const columnId = cb.getAttribute('data-column-id');
                    this.toggleColumn(columnId, cb.checked);
                });
            });

            const closeOnOutside = (ev) => {
                if (!dropdown.contains(ev.target) && ev.target !== anchorElement) {
                    dropdown.remove();
                    document.removeEventListener('mousedown', closeOnOutside);
                }
            };
            document.addEventListener('mousedown', closeOnOutside);
        }
    }

    window.TableCustomizer = TableCustomizer;
})();