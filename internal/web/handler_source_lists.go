package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/mstrhakr/network-list-sync/internal/store"
)

func (h *Handler) listSourceLists(w http.ResponseWriter, r *http.Request) {
	lists, err := h.store.ListSourceLists()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if lists == nil {
		lists = []store.SourceList{}
	}
	writeJSON(w, http.StatusOK, lists)
}

func (h *Handler) getSourceList(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid source list ID")
		return
	}
	list, err := h.store.GetSourceList(id)
	if err != nil {
		writeSourceListError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) createSourceList(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	var list store.SourceList
	if err := json.NewDecoder(r.Body).Decode(&list); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	list.Name = strings.TrimSpace(list.Name)
	if list.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	id, err := h.store.CreateSourceList(&list)
	if err != nil {
		writeSourceListError(w, err)
		return
	}
	// Return persisted values, including normalized references and timestamps.
	saved, err := h.store.GetSourceList(id)
	if err != nil {
		writeSourceListError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

func (h *Handler) updateSourceList(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	id, err := parseID(r)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid source list ID")
		return
	}
	if _, err := h.store.GetSourceList(id); err != nil {
		writeSourceListError(w, err)
		return
	}
	var list store.SourceList
	if err := json.NewDecoder(r.Body).Decode(&list); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	list.ID = id
	list.Name = strings.TrimSpace(list.Name)
	if list.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := h.store.UpdateSourceList(&list); err != nil {
		writeSourceListError(w, err)
		return
	}
	saved, err := h.store.GetSourceList(id)
	if err != nil {
		writeSourceListError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (h *Handler) deleteSourceList(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	id, err := parseID(r)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid source list ID")
		return
	}
	if _, err := h.store.GetSourceList(id); err != nil {
		writeSourceListError(w, err)
		return
	}
	if err := h.store.DeleteSourceList(id); err != nil {
		writeSourceListError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeSourceEntriesError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var validationErr *store.SourceValidationError
	if errors.As(err, &validationErr) {
		status = http.StatusBadRequest
	}
	writeError(w, status, err.Error())
}

func writeSourceListError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "source list not found")
		return
	}
	var validationErr *store.SourceValidationError
	if errors.As(err, &validationErr) {
		writeSourceEntriesError(w, err)
		return
	}
	// Reference conflicts still use descriptive errors. Inspect the root cause
	// so nested-list names cannot affect classification.
	cause := err
	for errors.Unwrap(cause) != nil {
		cause = errors.Unwrap(cause)
	}
	message := cause.Error()
	status := http.StatusInternalServerError
	switch {
	case strings.HasPrefix(message, "source list ") &&
		(strings.Contains(message, " is referenced by source list ") || strings.Contains(message, " is referenced by job ")):
		status = http.StatusConflict
	}
	writeError(w, status, err.Error())
}
