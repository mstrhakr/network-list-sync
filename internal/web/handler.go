package web

import (
	"bytes"
	"encoding/json"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mstrhakr/network-list-sync/internal/auth"
	"github.com/mstrhakr/network-list-sync/internal/clients"
	"github.com/mstrhakr/network-list-sync/internal/scheduler"
	"github.com/mstrhakr/network-list-sync/internal/store"
	"github.com/mstrhakr/network-list-sync/internal/syncer"
)

// Handler wires HTTP routes to the store, syncer, and scheduler.
type Handler struct {
	store     *store.Store
	syncer    *syncer.Syncer
	scheduler *scheduler.Scheduler
	uiFS      fs.FS
	uiTmpl    *template.Template
	auth      *auth.Service
}

// NewHandler registers all API routes and the embedded UI file server.
func NewHandler(s *store.Store, syn *syncer.Syncer, sched *scheduler.Scheduler, uiFS fs.FS) http.Handler {
	uiTmpl := template.Must(template.New("index").ParseFS(uiFS, "templates/*.gohtml", "templates/partials/*.gohtml"))
	staticFS := mustSubFS(uiFS, "static")
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))

	h := &Handler{
		store:     s,
		syncer:    syn,
		scheduler: sched,
		uiFS:      uiFS,
		uiTmpl:    uiTmpl,
	}
	if s != nil {
		h.auth = auth.NewService(s)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", h.loginPage)
	mux.HandleFunc("POST /login", h.loginSubmit)
	mux.HandleFunc("POST /logout", h.logout)

	mux.HandleFunc("GET /api/instances", h.listControllers)
	mux.HandleFunc("POST /api/instances", h.createController)
	mux.HandleFunc("GET /api/instances/{id}", h.getController)
	mux.HandleFunc("PUT /api/instances/{id}", h.updateController)
	mux.HandleFunc("DELETE /api/instances/{id}", h.deleteController)
	mux.HandleFunc("GET /api/instances/{id}/target-lists", h.listNetworkLists)
	mux.HandleFunc("POST /api/instances/test", h.testController)

	mux.HandleFunc("GET /api/jobs", h.listJobs)
	mux.HandleFunc("POST /api/jobs", h.createJob)
	mux.HandleFunc("GET /api/jobs/{id}", h.getJob)
	mux.HandleFunc("PUT /api/jobs/{id}", h.updateJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", h.deleteJob)
	mux.HandleFunc("GET /api/jobs/{id}/target-list", h.getJobNetworkList)
	mux.HandleFunc("POST /api/jobs/{id}/run", h.runJob)
	mux.HandleFunc("GET /api/jobs/{id}/logs", h.getJobLogs)
	mux.HandleFunc("POST /api/resolve", h.resolveHostnames)
	mux.HandleFunc("GET /api/health", h.health)
	mux.HandleFunc("GET /api/dns-servers", h.listDNSServers)
	mux.HandleFunc("POST /api/dns-servers", h.createDNSServer)
	mux.HandleFunc("GET /api/dns-servers/{id}", h.getDNSServer)
	mux.HandleFunc("PUT /api/dns-servers/{id}", h.updateDNSServer)
	mux.HandleFunc("DELETE /api/dns-servers/{id}", h.deleteDNSServer)
	mux.HandleFunc("GET /{$}", h.index)
	mux.Handle("GET /static/", staticHandler)
	mux.HandleFunc("GET /logo.png", h.serveLogo)

	return loggingMiddleware(h.authMiddleware(mux))
}

func mustSubFS(root fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(root, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	principal := principalFromContext(r.Context())
	var buf bytes.Buffer
	if err := h.uiTmpl.ExecuteTemplate(&buf, "index", map[string]any{"Principal": principal}); err != nil {
		http.Error(w, "failed to render UI", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) serveLogo(w http.ResponseWriter, r *http.Request) {
	h.serveUIFile(w, r, "logo.png")
}

func (h *Handler) serveUIFile(w http.ResponseWriter, r *http.Request, name string) {
	file, err := h.uiFS.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		http.Error(w, "failed to read UI asset", http.StatusInternalServerError)
		return
	}

	if seeker, ok := file.(io.ReadSeeker); ok {
		http.ServeContent(w, r, info.Name(), info.ModTime(), seeker)
		return
	}

	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read UI asset", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), bytes.NewReader(data))
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	bytes   int
	preview []byte
}

const errorBodyPreviewMax = 512

func (rw *statusRecorder) WriteHeader(statusCode int) {
	rw.status = statusCode
	rw.ResponseWriter.WriteHeader(statusCode)
}

func (rw *statusRecorder) Write(b []byte) (int, error) {
	if rw.status == 0 {
		rw.status = http.StatusOK
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	if len(rw.preview) < errorBodyPreviewMax {
		remaining := errorBodyPreviewMax - len(rw.preview)
		if remaining > len(b) {
			remaining = len(b)
		}
		rw.preview = append(rw.preview, b[:remaining]...)
	}
	return n, err
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		}

		switch {
		case status >= 500:
			if len(rec.preview) > 0 && shouldLogResponsePreview(rec.Header().Get("Content-Type")) {
				attrs = append(attrs, "response_preview", strings.TrimSpace(string(rec.preview)))
			}
			slog.Error("HTTP request failed", attrs...)
		case status >= 400:
			if len(rec.preview) > 0 && shouldLogResponsePreview(rec.Header().Get("Content-Type")) {
				attrs = append(attrs, "response_preview", strings.TrimSpace(string(rec.preview)))
			}
			slog.Warn("HTTP request error", attrs...)
		default:
			slog.Info("HTTP request", attrs...)
		}
	})
}

func shouldLogResponsePreview(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if strings.Contains(contentType, "text/html") {
		return false
	}
	return true
}

// ---------- Instance Handlers ----------

func (h *Handler) listControllers(w http.ResponseWriter, r *http.Request) {
	ctrls, err := h.store.ListControllers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range ctrls {
		if ctrls[i].APIKey != "" {
			ctrls[i].APIKey = "••••••••"
		}
	}
	if ctrls == nil {
		ctrls = []store.Controller{}
	}
	writeJSON(w, http.StatusOK, ctrls)
}

func (h *Handler) createController(w http.ResponseWriter, r *http.Request) {
	var c store.Controller
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if c.Name == "" || c.URL == "" || c.APIKey == "" {
		writeError(w, http.StatusBadRequest, "missing required fields")
		return
	}
	if c.Provider == "" {
		c.Provider = "unifi"
	}
	id, err := h.store.CreateController(&c)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	c.ID = id
	c.APIKey = "••••••••"
	writeJSON(w, http.StatusCreated, c)
}

func (h *Handler) getController(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}
	c, err := h.store.GetController(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "endpoint not found")
		return
	}
	if c.APIKey != "" {
		c.APIKey = "••••••••"
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) updateController(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}
	var c store.Controller
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// If API key is blank or the redacted placeholder, keep existing
	if c.APIKey == "" || c.APIKey == "••••••••" {
		existing, err := h.store.GetController(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "endpoint not found")
			return
		}
		c.APIKey = existing.APIKey
		if c.Provider == "" {
			c.Provider = existing.Provider
		}
	}
	if c.Provider == "" {
		c.Provider = "unifi"
	}
	c.ID = id
	if err := h.store.UpdateController(&c); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	c.APIKey = "••••••••"
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) deleteController(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}
	if err := h.store.DeleteController(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) testController(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ControllerID  int64  `json:"instance_id"`
		Provider      string `json:"provider"`
		URL           string `json:"url"`
		Site          string `json:"site"`
		APIKey        string `json:"api_key"`
		SkipTLSVerify bool   `json:"skip_tls_verify"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.APIKey == "" {
		if req.ControllerID == 0 {
			writeError(w, http.StatusBadRequest, "api_key is required for new endpoints")
			return
		}
		existing, err := h.store.GetController(req.ControllerID)
		if err != nil {
			writeError(w, http.StatusNotFound, "endpoint not found")
			return
		}
		if existing.APIKey == "" {
			writeError(w, http.StatusBadRequest, "no saved api_key for endpoint")
			return
		}
		req.APIKey = existing.APIKey
		if req.URL == "" {
			req.URL = existing.URL
		}
		if req.Site == "" {
			req.Site = existing.Site
		}
		if req.Provider == "" {
			req.Provider = existing.Provider
		}
	}

	if req.URL == "" || req.APIKey == "" {
		writeError(w, http.StatusBadRequest, "url and api_key are required")
		return
	}
	ctrl := &store.Controller{
		Provider:      req.Provider,
		URL:           req.URL,
		Site:          req.Site,
		APIKey:        req.APIKey,
		SkipTLSVerify: req.SkipTLSVerify,
	}
	p, err := clients.New(ctrl)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	lists, err := p.ListNetworkLists()
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":           true,
		"target_lists": len(lists),
	})
}

func (h *Handler) listNetworkLists(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}
	ctrl, err := h.store.GetController(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "endpoint not found")
		return
	}
	p, err := clients.New(ctrl)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	lists, err := p.ListNetworkLists()
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, lists)
}

// ---------- Job Handlers ----------

func (h *Handler) listJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.store.ListJobs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if jobs == nil {
		jobs = []store.SyncJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (h *Handler) createJob(w http.ResponseWriter, r *http.Request) {
	var job store.SyncJob
	if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if job.Name == "" || job.Hostnames == "" {
		writeError(w, http.StatusBadRequest, "missing required fields")
		return
	}
	if len(job.Targets) == 0 {
		if job.ControllerID == 0 || job.NetworkListID == "" {
			writeError(w, http.StatusBadRequest, "missing required target fields")
			return
		}
		job.Targets = []store.JobTarget{{ControllerID: job.ControllerID, NetworkListID: job.NetworkListID}}
	}
	if job.ObservedIPTTLHours < 0 {
		job.ObservedIPTTLHours = store.DefaultObservedIPTTLHours
	}

	id, err := h.store.CreateJob(&job)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.scheduler.Reload(id)

	job.ID = id
	writeJSON(w, http.StatusCreated, job)
}

func (h *Handler) getJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job ID")
		return
	}

	job, err := h.store.GetJob(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) updateJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job ID")
		return
	}

	var job store.SyncJob
	if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if len(job.Targets) == 0 {
		if job.ControllerID == 0 || job.NetworkListID == "" {
			writeError(w, http.StatusBadRequest, "missing required target fields")
			return
		}
		job.Targets = []store.JobTarget{{ControllerID: job.ControllerID, NetworkListID: job.NetworkListID}}
	}
	if job.ObservedIPTTLHours < 0 {
		job.ObservedIPTTLHours = store.DefaultObservedIPTTLHours
	}

	job.ID = id
	if err := h.store.UpdateJob(&job); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.scheduler.Reload(id)

	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) deleteJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job ID")
		return
	}

	h.scheduler.Remove(id)

	if err := h.store.DeleteJob(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getJobNetworkList(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job ID")
		return
	}

	job, err := h.store.GetJob(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	targets := job.Targets
	if len(targets) == 0 {
		targets = []store.JobTarget{{ControllerID: job.ControllerID, NetworkListID: job.NetworkListID}}
	}
	selectedTarget := targets[0]
	if targetID := r.URL.Query().Get("target_id"); targetID != "" {
		if parsedTargetID, parseErr := strconv.ParseInt(targetID, 10, 64); parseErr == nil {
			for _, t := range targets {
				if t.ID == parsedTargetID {
					selectedTarget = t
					break
				}
			}
		}
	}

	ctrl, err := h.store.GetController(selectedTarget.ControllerID)
	if err != nil {
		writeError(w, http.StatusNotFound, "endpoint not found")
		return
	}
	p, err := clients.New(ctrl)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	networkList, err := p.GetNetworkList(selectedTarget.NetworkListID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, networkList)
}

func (h *Handler) runJob(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job ID")
		return
	}

	if _, err := h.store.GetJob(id); err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	go h.syncer.Run(h.store, id)

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (h *Handler) getJobLogs(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job ID")
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}

	logs, err := h.store.GetRunLogs(id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if logs == nil {
		logs = []store.RunLog{}
	}
	writeJSON(w, http.StatusOK, logs)
}

func (h *Handler) resolveHostnames(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Hostnames string `json:"hostnames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	extraServers, _ := h.store.ListEnabledDNSServerAddresses()
	if len(extraServers) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "no DNS servers configured: add at least one enabled DNS server")
		return
	}
	hostIPs, err := syncer.ResolveHostnames(input.Hostnames, extraServers)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	ips := syncer.SortedIPs(hostIPs)
	type resolvedIP struct {
		IP       string `json:"ip"`
		Hostname string `json:"hostname"`
	}
	result := make([]resolvedIP, 0, len(ips))
	for _, ip := range ips {
		result = append(result, resolvedIP{IP: ip, Hostname: hostIPs[ip]})
	}
	writeJSON(w, http.StatusOK, result)
}

// ---------- DNS Server Handlers ----------

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	type healthResponse struct {
		OK      bool   `json:"ok"`
		Message string `json:"message,omitempty"`
	}
	servers, err := h.store.ListEnabledDNSServerAddresses()
	if err != nil {
		writeJSON(w, http.StatusOK, healthResponse{OK: false, Message: "failed to query DNS servers: " + err.Error()})
		return
	}
	if len(servers) == 0 {
		writeJSON(w, http.StatusOK, healthResponse{OK: false, Message: "No enabled DNS servers configured. Scheduled syncs are disabled until at least one DNS server is enabled."})
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{OK: true})
}

func (h *Handler) listDNSServers(w http.ResponseWriter, r *http.Request) {
	servers, err := h.store.ListDNSServers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if servers == nil {
		servers = []store.DNSServer{}
	}
	writeJSON(w, http.StatusOK, servers)
}

func (h *Handler) createDNSServer(w http.ResponseWriter, r *http.Request) {
	var s store.DNSServer
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if s.Name == "" || s.Address == "" {
		writeError(w, http.StatusBadRequest, "name and address are required")
		return
	}
	id, err := h.store.CreateDNSServer(&s)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.ID = id
	writeJSON(w, http.StatusCreated, s)
}

func (h *Handler) getDNSServer(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid DNS server ID")
		return
	}
	s, err := h.store.GetDNSServer(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "DNS server not found")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) updateDNSServer(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid DNS server ID")
		return
	}
	var s store.DNSServer
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	s.ID = id
	if err := h.store.UpdateDNSServer(&s); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) deleteDNSServer(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid DNS server ID")
		return
	}
	if err := h.store.DeleteDNSServer(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
