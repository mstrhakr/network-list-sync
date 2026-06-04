// Package logging provides a configurable log manager that supports
// multiple output targets: stdout, file, and remote syslog (UDP/TCP).
// It configures slog as the default logger and can be reconfigured at runtime.
//
// Internally a single bounded ring buffer stores log events. Per-sink worker
// goroutines consume from the ring via independent cursors, format events
// according to their sink type, and write output. When the ring is full the
// oldest event is overwritten (drop_oldest) and per-sink missed counters are
// incremented for any sink that had not yet consumed the overwritten slot.
package logging

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config holds logging configuration
type Config struct {
	DebugEnabled   bool   `json:"debugEnabled"`   // Enable debug-level log messages
	VerboseEnabled bool   `json:"verboseEnabled"` // Enable verbose-level log messages (e.g. HTTP request logs)
	LogToStdout    bool   `json:"logToStdout"`    // Write logs to stdout (default: true)
	LogToFile      bool   `json:"logToFile"`      // Write logs to a file
	FilePath       string `json:"filePath"`       // Log file path (relative to data dir or absolute)
	FileMaxSizeMB  int    `json:"fileMaxSizeMB"`  // Max log file size in MB before rotation (0 = no limit)
	FileMaxFiles   int    `json:"fileMaxFiles"`   // Max number of rotated log files to keep (0 = unlimited)
	SyslogEnabled  bool   `json:"syslogEnabled"`  // Send logs to a remote syslog server
	SyslogProtocol string `json:"syslogProtocol"` // "udp" or "tcp"
	SyslogHost     string `json:"syslogHost"`     // Syslog server address (host:port)
	SyslogTag      string `json:"syslogTag"`      // Application tag in syslog messages
}

// DefaultConfig returns sensible default settings
func DefaultConfig() Config {
	return Config{
		DebugEnabled:   false,
		VerboseEnabled: false,
		LogToStdout:    true,
		LogToFile:      false,
		FilePath:       "./sync.log",
		FileMaxSizeMB:  50,
		FileMaxFiles:   10,
		SyslogEnabled:  false,
		SyslogProtocol: "udp",
		SyslogHost:     "",
		SyslogTag:      "network-list-sync",
	}
}

// LevelVerbose is an application-specific level lower than DEBUG.
const LevelVerbose = slog.Level(-8)

const (
	fieldKeyVerbosePrefix = "verbose."
	ringCapacity          = 5000
	rotationCheckInterval = 15 * time.Second
	missedWarnInterval    = 30 * time.Second // throttle missed-event warnings
)

// Sink index constants for bitmask operations.
const (
	sinkConsole = 0
	sinkFile    = 1
	sinkSyslog  = 2
)

// VerboseField marks a field as verbose-only for console output.
// Structured sinks still receive this field.
func VerboseField(key string, value interface{}) slog.Attr {
	return slog.Any(fieldKeyVerbosePrefix+key, value)
}

// -------------------------------------------------------------------------
// Ring buffer event model
// -------------------------------------------------------------------------

// logEvent is a single captured log event stored in the ring buffer.
type logEvent struct {
	seq   uint64
	time  time.Time
	level slog.Level
	msg   string
	pc    uintptr
	attrs []slog.Attr // pre-resolved attributes
}

// eventRing is a fixed-size circular buffer of logEvent slots. The ring is
// indexed by seq % capacity. head is the sequence number of the next slot to
// write. tail is the oldest live sequence. All access is protected by the
// Manager mutex externally.
type eventRing struct {
	buf  []logEvent
	cap  uint64
	head uint64 // next write sequence (monotonically increasing)
	tail uint64 // oldest live sequence
}

func newEventRing(capacity int) *eventRing {
	return &eventRing{
		buf: make([]logEvent, capacity),
		cap: uint64(capacity),
	}
}

// push appends an event to the ring, returning the sequence number assigned.
// If the ring is full the oldest slot is overwritten and evicted is true
// with the overwritten event's sequence returned in evictedSeq.
func (r *eventRing) push(e logEvent) (seq uint64, evicted bool, evictedSeq uint64) {
	seq = r.head
	e.seq = seq
	idx := seq % r.cap
	evicted = (r.head - r.tail) >= r.cap
	if evicted {
		evictedSeq = r.tail
		r.tail++
	}
	r.buf[idx] = e
	r.head++
	return seq, evicted, evictedSeq
}

// get returns the event at the given sequence number. ok is false if seq is
// outside the live [tail, head) window.
func (r *eventRing) get(seq uint64) (logEvent, bool) {
	if seq < r.tail || seq >= r.head {
		return logEvent{}, false
	}
	return r.buf[seq%r.cap], true
}

// -------------------------------------------------------------------------
// Sink worker
// -------------------------------------------------------------------------

// sinkWorker is a goroutine consuming events from the shared ring for one
// output sink. It blocks on a condition variable signal, reads from its cursor
// position, formats the event, and writes to the underlying io.Writer.
type sinkWorker struct {
	name    string       // "console", "file", "syslog"
	index   int          // sinkConsole / sinkFile / sinkSyslog
	nextSeq uint64       // next sequence to consume
	missed  uint64       // events that were overwritten before this worker read them
	handler slog.Handler // per-sink formatter
	cond    *sync.Cond
	stopped chan struct{}
	ring    *eventRing // shared, accessed under Manager.mu
	mgr     *Manager
}

func (sw *sinkWorker) run() {
	defer close(sw.stopped)
	for {
		sw.mgr.mu.Lock()
		// Wait until there is work or we are shutting down.
		for sw.nextSeq >= sw.ring.head && !sw.mgr.workersDone {
			sw.cond.Wait()
		}
		if sw.mgr.workersDone && sw.nextSeq >= sw.ring.head {
			sw.mgr.mu.Unlock()
			return
		}
		// Snap cursor against tail — if we fell behind, count misses.
		if sw.nextSeq < sw.ring.tail {
			sw.missed += sw.ring.tail - sw.nextSeq
			sw.nextSeq = sw.ring.tail
		}
		// Collect a batch of events while holding the lock.
		var batch []logEvent
		for sw.nextSeq < sw.ring.head {
			ev, ok := sw.ring.get(sw.nextSeq)
			if !ok {
				// Slot was evicted between check and get — skip.
				sw.missed++
				sw.nextSeq++
				continue
			}
			batch = append(batch, ev)
			sw.nextSeq++
		}
		sw.mgr.mu.Unlock()

		// Format and write outside the lock.
		ctx := context.Background()
		for i := range batch {
			record := slog.NewRecord(batch[i].time, batch[i].level, batch[i].msg, batch[i].pc)
			record.Add(attrsToAny(batch[i].attrs)...)
			_ = sw.handler.Handle(ctx, record)
		}
	}
}

// attrsToAny converts a slice of slog.Attr to []any for record.Add.
func attrsToAny(attrs []slog.Attr) []any {
	out := make([]any, len(attrs))
	for i, a := range attrs {
		out[i] = a
	}
	return out
}

// -------------------------------------------------------------------------
// Manager
// -------------------------------------------------------------------------

// Manager manages log output targets and debug state
type Manager struct {
	mu           sync.Mutex
	config       Config
	logFile      *os.File
	sysConn      net.Conn
	logger       *slog.Logger
	ring         *eventRing
	workers      []*sinkWorker
	workerCond   *sync.Cond
	workersDone  bool
	rotationStop chan struct{}

	// Per-sink missed counters (atomic for diagnostics reads without lock).
	missedConsole atomic.Uint64
	missedFile    atomic.Uint64
	missedSyslog  atomic.Uint64

	// Throttle for missed-event warnings.
	lastMissedWarn time.Time
	inMissedWarn   bool // guard against warning recursion
}

// Global singleton
var (
	globalManager *Manager
	once          sync.Once
)

func init() {
	// Set a sensible default logger early so package-init log.Printf calls
	// are consistently formatted before main() config overrides it.
	_ = GetManager().Configure(DefaultConfig())
}

// GetManager returns the global log manager, creating it with defaults if needed
func GetManager() *Manager {
	once.Do(func() {
		m := &Manager{
			config: DefaultConfig(),
			ring:   newEventRing(ringCapacity),
		}
		m.workerCond = sync.NewCond(&m.mu)
		globalManager = m
	})
	return globalManager
}

// Configure applies a new logging configuration at runtime.
// It closes existing file/syslog connections and opens new ones as needed.
func (m *Manager) Configure(cfg Config) error {
	m.mu.Lock()

	// Stop background helpers tied to prior outputs.
	m.stopRotationWatcherLocked()
	m.stopWorkersLocked()

	// Close existing outputs
	m.closeOutputs()

	m.config = cfg
	var warnings []string

	// Ensure ring exists (first call from init).
	if m.ring == nil {
		m.ring = newEventRing(ringCapacity)
		m.workerCond = sync.NewCond(&m.mu)
	}

	// --- Open outputs ---

	hasConsole := false
	if cfg.LogToStdout {
		hasConsole = true
	}

	hasFile := false
	if cfg.LogToFile && cfg.FilePath != "" {
		if err := m.openLogFile(cfg.FilePath); err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to open log file %s: %v", cfg.FilePath, err))
		} else {
			hasFile = true
		}
	}

	hasSyslog := false
	var sysTag string
	if cfg.SyslogEnabled && cfg.SyslogHost != "" {
		if err := m.openSyslog(cfg.SyslogProtocol, cfg.SyslogHost); err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to connect to syslog %s://%s: %v",
				cfg.SyslogProtocol, cfg.SyslogHost, err))
		} else {
			hasSyslog = true
			sysTag = cfg.SyslogTag
			if sysTag == "" {
				sysTag = "network-list-sync"
			}
		}
	}

	// If no output, fall back to console.
	if !hasConsole && !hasFile && !hasSyslog {
		hasConsole = true
	}

	// --- Build per-sink handlers and workers ---
	m.workersDone = false
	startSeq := m.ring.head // new workers start from current head

	if hasConsole {
		h := newPrettyConsoleHandler(os.Stdout, cfg.VerboseEnabled, shouldColorize(os.Stdout))
		sw := &sinkWorker{
			name:    "console",
			index:   sinkConsole,
			nextSeq: startSeq,
			handler: h,
			cond:    m.workerCond,
			stopped: make(chan struct{}),
			ring:    m.ring,
			mgr:     m,
		}
		m.workers = append(m.workers, sw)
		go sw.run()
	}

	if hasFile {
		h := newStructuredHandler(m.logFile, cfg.VerboseEnabled)
		sw := &sinkWorker{
			name:    "file",
			index:   sinkFile,
			nextSeq: startSeq,
			handler: h,
			cond:    m.workerCond,
			stopped: make(chan struct{}),
			ring:    m.ring,
			mgr:     m,
		}
		m.workers = append(m.workers, sw)
		go sw.run()
	}

	if hasSyslog {
		h := newStructuredHandler(&syslogWriter{conn: m.sysConn, tag: sysTag}, cfg.VerboseEnabled)
		sw := &sinkWorker{
			name:    "syslog",
			index:   sinkSyslog,
			nextSeq: startSeq,
			handler: h,
			cond:    m.workerCond,
			stopped: make(chan struct{}),
			ring:    m.ring,
			mgr:     m,
		}
		m.workers = append(m.workers, sw)
		go sw.run()
	}

	// Build the slog.Logger used by callers. It goes through level filtering
	// then directly enqueues into the ring (no async byte channels).
	m.rebuildLoggerLocked()
	m.startRotationWatcherLocked()
	logger := m.logger
	m.mu.Unlock()

	// Emit warnings (now that workers are running).
	for _, warning := range warnings {
		if logger != nil {
			logger.Warn("Logging configuration warning", "category", "logging.config", "detail", warning)
		}
	}

	return nil
}

// GetConfig returns a copy of the current config
func (m *Manager) GetConfig() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config
}

// IsDebug returns whether debug logging is enabled
func (m *Manager) IsDebug() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config.DebugEnabled
}

// Logger returns the active slog logger.
func (m *Manager) Logger() *slog.Logger {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.logger != nil {
		return m.logger
	}
	return slog.Default()
}

// Info logs an informational message.
func (m *Manager) Info(msg string, args ...interface{}) {
	m.log(slog.LevelInfo, msg, args...)
}

// Warn logs a warning message.
func (m *Manager) Warn(msg string, args ...interface{}) {
	m.log(slog.LevelWarn, msg, args...)
}

// Error logs an error message.
func (m *Manager) Error(msg string, args ...interface{}) {
	m.log(slog.LevelError, msg, args...)
}

// Debug logs a debug-level message (only if debug is enabled).
func (m *Manager) Debug(msg string, args ...interface{}) {
	if !m.IsDebug() {
		return
	}
	m.log(slog.LevelDebug, msg, args...)
}

// Debugf logs a debug-level message (only if debug is enabled)
func (m *Manager) Debugf(format string, v ...interface{}) {
	m.Debug(fmt.Sprintf(format, v...))
}

// IsVerbose returns whether verbose logging is enabled
func (m *Manager) IsVerbose() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config.VerboseEnabled
}

// Verbose logs a verbose-level message (only if verbose is enabled).
func (m *Manager) Verbose(msg string, args ...interface{}) {
	if !m.IsVerbose() {
		return
	}
	m.log(LevelVerbose, msg, args...)
}

// Verbosef logs a verbose-level message (only if verbose is enabled)
func (m *Manager) Verbosef(format string, v ...interface{}) {
	m.Verbose(fmt.Sprintf(format, v...))
}

// Close shuts down all log outputs cleanly
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopRotationWatcherLocked()
	m.stopWorkersLocked()
	m.closeOutputs()
	m.rebuildLoggerLocked()
}

// RotateFileIfNeeded checks if the log file exceeds max size and rotates it
func (m *Manager) RotateFileIfNeeded() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.logFile == nil || m.config.FileMaxSizeMB <= 0 {
		return
	}

	info, err := m.logFile.Stat()
	if err != nil {
		return
	}

	maxBytes := int64(m.config.FileMaxSizeMB) * 1024 * 1024
	if info.Size() < maxBytes {
		return
	}

	// Rotate: close current, rename, open new
	oldPath := m.config.FilePath
	m.logFile.Close()
	m.logFile = nil

	rotatedName := oldPath + "." + time.Now().Format("20060102-150405")
	os.Rename(oldPath, rotatedName)

	if err := m.openLogFile(oldPath); err != nil {
		log.Printf("Failed to reopen log file after rotation: %v", err)
		return
	}

	// Update the file sink worker's handler to point at the new file.
	for _, sw := range m.workers {
		if sw.index == sinkFile {
			sw.handler = newStructuredHandler(m.logFile, m.config.VerboseEnabled)
		}
	}

	if m.config.FileMaxFiles > 0 {
		cleanupRotatedFiles(oldPath, m.config.FileMaxFiles)
	}
}

// SinkStats holds per-sink diagnostic counters.
type SinkStats struct {
	Name    string `json:"name"`
	Missed  uint64 `json:"missed"`
	NextSeq uint64 `json:"nextSeq"`
	Lag     uint64 `json:"lag"`
}

// DiagStats holds ring buffer and sink diagnostics.
type DiagStats struct {
	RingHead uint64      `json:"ringHead"`
	RingTail uint64      `json:"ringTail"`
	RingUsed uint64      `json:"ringUsed"`
	RingCap  uint64      `json:"ringCap"`
	Sinks    []SinkStats `json:"sinks"`
}

// Diagnostics returns current ring and sink worker statistics.
func (m *Manager) Diagnostics() DiagStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := DiagStats{
		RingCap:  m.ring.cap,
		RingHead: m.ring.head,
		RingTail: m.ring.tail,
		RingUsed: m.ring.head - m.ring.tail,
	}
	for _, sw := range m.workers {
		lag := uint64(0)
		if m.ring.head > sw.nextSeq {
			lag = m.ring.head - sw.nextSeq
		}
		d.Sinks = append(d.Sinks, SinkStats{
			Name:    sw.name,
			Missed:  sw.missed,
			NextSeq: sw.nextSeq,
			Lag:     lag,
		})
	}
	return d
}

// cleanupRotatedFiles removes the oldest rotated log files if there are more
// than maxFiles. Rotated files match the pattern <basePath>.<timestamp>.
func cleanupRotatedFiles(basePath string, maxFiles int) {
	dir := filepath.Dir(basePath)
	base := filepath.Base(basePath)
	prefix := base + "."

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	var rotated []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, prefix) && name != base {
			rotated = append(rotated, name)
		}
	}

	if len(rotated) <= maxFiles {
		return
	}

	sort.Strings(rotated)
	toRemove := rotated[:len(rotated)-maxFiles]
	for _, name := range toRemove {
		fullPath := filepath.Join(dir, name)
		if err := os.Remove(fullPath); err != nil {
			log.Printf("Warning: failed to remove old log file %s: %v", fullPath, err)
		} else {
			log.Printf("Removed old rotated log file: %s", name)
		}
	}
}

// -------------------------------------------------------------------------
// Internal helpers
// -------------------------------------------------------------------------

func (m *Manager) stopWorkersLocked() {
	if len(m.workers) == 0 {
		return
	}
	m.workersDone = true
	m.workerCond.Broadcast()

	// Collect stopped channels while holding lock, then release to let
	// workers exit.
	stopped := make([]chan struct{}, len(m.workers))
	for i, sw := range m.workers {
		stopped[i] = sw.stopped
	}

	// Snapshot missed counters.
	for _, sw := range m.workers {
		switch sw.index {
		case sinkConsole:
			m.missedConsole.Add(sw.missed)
		case sinkFile:
			m.missedFile.Add(sw.missed)
		case sinkSyslog:
			m.missedSyslog.Add(sw.missed)
		}
	}

	m.workers = nil
	m.mu.Unlock()
	for _, ch := range stopped {
		<-ch
	}
	m.mu.Lock()
	m.workersDone = false
}

func (m *Manager) startRotationWatcherLocked() {
	if m.logFile == nil || m.config.FileMaxSizeMB <= 0 {
		return
	}
	if m.rotationStop != nil {
		return
	}

	stop := make(chan struct{})
	m.rotationStop = stop

	go func() {
		ticker := time.NewTicker(rotationCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.RotateFileIfNeeded()
			case <-stop:
				return
			}
		}
	}()
}

func (m *Manager) stopRotationWatcherLocked() {
	if m.rotationStop == nil {
		return
	}
	close(m.rotationStop)
	m.rotationStop = nil
}

func (m *Manager) closeOutputs() {
	if m.logFile != nil {
		m.logFile.Close()
		m.logFile = nil
	}
	if m.sysConn != nil {
		m.sysConn.Close()
		m.sysConn = nil
	}
}

func (m *Manager) openLogFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	m.logFile = f
	return nil
}

func (m *Manager) openSyslog(protocol, host string) error {
	protocol = strings.ToLower(protocol)
	if protocol != "udp" && protocol != "tcp" {
		protocol = "udp"
	}

	conn, err := net.DialTimeout(protocol, host, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect to syslog: %w", err)
	}
	m.sysConn = conn
	return nil
}

// enqueue adds a logEvent to the ring and wakes workers. Must NOT be called
// with m.mu held — it acquires the lock internally.
func (m *Manager) enqueue(e logEvent) {
	m.mu.Lock()
	_, evicted, evictedSeq := m.ring.push(e)
	if evicted {
		m.accountEvictionLocked(evictedSeq)
	}
	m.workerCond.Broadcast()
	m.mu.Unlock()
}

// accountEvictionLocked increments missed counters for any worker whose cursor
// points at or before the evicted sequence. Must be called with m.mu held.
func (m *Manager) accountEvictionLocked(evictedSeq uint64) {
	for _, sw := range m.workers {
		if sw.nextSeq <= evictedSeq {
			sw.missed++
		}
	}

	// Throttled warning to stderr (not through the ring to avoid recursion).
	if !m.inMissedWarn && time.Since(m.lastMissedWarn) > missedWarnInterval {
		m.inMissedWarn = true
		m.lastMissedWarn = time.Now()
		var parts []string
		for _, sw := range m.workers {
			if sw.missed > 0 {
				parts = append(parts, fmt.Sprintf("%s=%d", sw.name, sw.missed))
			}
		}
		if len(parts) > 0 {
			fmt.Fprintf(os.Stderr, "[logging] ring buffer overflow — missed events: %s\n", strings.Join(parts, ", "))
		}
		m.inMissedWarn = false
	}
}

// log is the internal entry point for all convenience methods.
func (m *Manager) log(level slog.Level, msg string, args ...interface{}) {
	m.mu.Lock()
	cfg := m.config
	logger := m.logger
	m.mu.Unlock()

	if logger == nil {
		return
	}

	ctx := context.Background()
	if !logger.Enabled(ctx, level) {
		return
	}

	pc := uintptr(0)
	if cfg.VerboseEnabled {
		pc = firstExternalCallerPC()
	}

	// Resolve args into slog.Attr slice so they are captured by value.
	attrs := argsToAttrs(args)

	m.enqueue(logEvent{
		time:  time.Now(),
		level: level,
		msg:   msg,
		pc:    pc,
		attrs: attrs,
	})
}

// argsToAttrs converts variadic args (as accepted by slog) into a concrete
// slice of slog.Attr, mimicking slog.Record.Add behaviour.
func argsToAttrs(args []any) []slog.Attr {
	var attrs []slog.Attr
	for len(args) > 0 {
		switch x := args[0].(type) {
		case string:
			if len(args) < 2 {
				attrs = append(attrs, slog.String("!BADKEY", x))
				args = args[1:]
			} else {
				attrs = append(attrs, slog.Any(x, args[1]))
				args = args[2:]
			}
		case slog.Attr:
			attrs = append(attrs, x)
			args = args[1:]
		default:
			attrs = append(attrs, slog.Any("!BADKEY", x))
			args = args[1:]
		}
	}
	return attrs
}

func firstExternalCallerPC() uintptr {
	const maxDepth = 32
	pcs := make([]uintptr, maxDepth)
	n := runtime.Callers(2, pcs) // skip runtime.Callers + firstExternalCallerPC
	if n == 0 {
		return 0
	}

	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		file := strings.ReplaceAll(frame.File, "\\", "/")
		if !strings.HasSuffix(file, "/internal/logging/logging.go") {
			return frame.PC
		}
		if !more {
			break
		}
	}

	return pcs[0]
}

// rebuildLoggerLocked builds a slog.Logger whose handler enqueues into the ring
// buffer through level filtering. Must be called with m.mu held.
func (m *Manager) rebuildLoggerLocked() {
	ringHandler := &ringEnqueueHandler{mgr: m, cfg: m.config}
	filteredHandler := &levelFilterHandler{next: ringHandler, cfg: m.config}
	logger := slog.New(filteredHandler)
	m.logger = logger
	slog.SetDefault(logger)

	// Bridge legacy log.Printf calls into slog while preserving message text.
	stdLogger := slog.NewLogLogger(logger.Handler(), slog.LevelInfo)
	stdLogger.SetPrefix("")
	stdLogger.SetFlags(0)
	log.SetPrefix("")
	log.SetFlags(0)
	log.SetOutput(stdLogger.Writer())
}

// -------------------------------------------------------------------------
// ringEnqueueHandler — slog.Handler that pushes events onto the ring
// -------------------------------------------------------------------------

type ringEnqueueHandler struct {
	mgr    *Manager
	cfg    Config
	groups []string
	attrs  []slog.Attr
}

func (h *ringEnqueueHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true // level filtering done by levelFilterHandler wrapper
}

func (h *ringEnqueueHandler) Handle(_ context.Context, record slog.Record) error {
	// Collect pre-bound attrs + record attrs into one slice.
	attrs := make([]slog.Attr, 0, len(h.attrs)+record.NumAttrs())
	attrs = append(attrs, h.attrs...)
	record.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})

	h.mgr.enqueue(logEvent{
		time:  record.Time,
		level: record.Level,
		msg:   record.Message,
		pc:    record.PC,
		attrs: attrs,
	})
	return nil
}

func (h *ringEnqueueHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs), len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	for _, a := range attrs {
		if len(h.groups) > 0 {
			// Wrap in nested groups.
			wrapped := a
			for i := len(h.groups) - 1; i >= 0; i-- {
				wrapped = slog.Group(h.groups[i], wrapped)
			}
			newAttrs = append(newAttrs, wrapped)
		} else {
			newAttrs = append(newAttrs, a)
		}
	}
	return &ringEnqueueHandler{mgr: h.mgr, cfg: h.cfg, groups: h.groups, attrs: newAttrs}
}

func (h *ringEnqueueHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	newGroups := make([]string, len(h.groups)+1)
	copy(newGroups, h.groups)
	newGroups[len(h.groups)] = name
	return &ringEnqueueHandler{mgr: h.mgr, cfg: h.cfg, groups: newGroups, attrs: h.attrs}
}

// -------------------------------------------------------------------------
// Structured handler (file / syslog)
// -------------------------------------------------------------------------

func newStructuredHandler(w io.Writer, addSource bool) slog.Handler {
	return slog.NewTextHandler(w, &slog.HandlerOptions{
		AddSource:   addSource,
		Level:       LevelVerbose,
		ReplaceAttr: replaceSlogAttr,
	})
}

func shouldColorize(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func replaceSlogAttr(_ []string, attr slog.Attr) slog.Attr {
	if strings.HasPrefix(attr.Key, fieldKeyVerbosePrefix) {
		attr.Key = strings.TrimPrefix(attr.Key, fieldKeyVerbosePrefix)
	}

	switch attr.Key {
	case slog.TimeKey:
		if t, ok := attr.Value.Any().(time.Time); ok {
			return slog.String(slog.TimeKey, t.Format(time.RFC3339Nano))
		}
	case slog.LevelKey:
		if level, ok := attr.Value.Any().(slog.Level); ok {
			return slog.String(slog.LevelKey, formatLevel(level))
		}
	case slog.SourceKey:
		if source, ok := attr.Value.Any().(*slog.Source); ok && source != nil {
			return slog.String(slog.SourceKey, fmt.Sprintf("%s:%d", filepath.Base(source.File), source.Line))
		}
	}
	return attr
}

func formatLevel(level slog.Level) string {
	switch {
	case level == LevelVerbose:
		return "VERBOSE"
	case level <= slog.LevelDebug:
		return "DEBUG"
	case level < slog.LevelWarn:
		return "INFO"
	case level < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}

func formatConsoleLevel(level slog.Level) string {
	switch {
	case level == LevelVerbose:
		return "VRB"
	case level <= slog.LevelDebug:
		return "DBG"
	case level < slog.LevelWarn:
		return "INF"
	case level < slog.LevelError:
		return "WRN"
	default:
		return "ERR"
	}
}

// -------------------------------------------------------------------------
// Level filter handler
// -------------------------------------------------------------------------

type levelFilterHandler struct {
	next slog.Handler
	cfg  Config
}

func (h *levelFilterHandler) Enabled(ctx context.Context, level slog.Level) bool {
	switch {
	case level == LevelVerbose:
		return h.cfg.VerboseEnabled
	case level == slog.LevelDebug:
		return h.cfg.DebugEnabled
	default:
		return h.next.Enabled(ctx, level)
	}
}

func (h *levelFilterHandler) Handle(ctx context.Context, record slog.Record) error {
	return h.next.Handle(ctx, record)
}

func (h *levelFilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelFilterHandler{next: h.next.WithAttrs(attrs), cfg: h.cfg}
}

func (h *levelFilterHandler) WithGroup(name string) slog.Handler {
	return &levelFilterHandler{next: h.next.WithGroup(name), cfg: h.cfg}
}

// -------------------------------------------------------------------------
// Pretty console handler (unchanged formatting)
// -------------------------------------------------------------------------

type scopedAttr struct {
	groups []string
	attr   slog.Attr
}

type prettyConsoleHandler struct {
	w         io.Writer
	mu        *sync.Mutex
	addSource bool
	verbose   bool
	colorize  bool
	groups    []string
	attrs     []scopedAttr
}

func newPrettyConsoleHandler(w io.Writer, addSource, colorize bool) slog.Handler {
	return &prettyConsoleHandler{
		w:         w,
		mu:        &sync.Mutex{},
		addSource: addSource,
		verbose:   addSource,
		colorize:  colorize,
	}
}

func (h *prettyConsoleHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (h *prettyConsoleHandler) Handle(_ context.Context, record slog.Record) error {
	var b strings.Builder

	ts := record.Time
	if ts.IsZero() {
		ts = time.Now()
	}

	levelLabel := formatConsoleLevel(record.Level)
	if h.colorize {
		levelLabel = colorizeLevel(levelLabel)
	}

	const (
		dimOn  = "\x1b[2m"
		dimOff = "\x1b[0m"
		boldOn = "\x1b[1m"
	)

	// Datetime — dimmed so it recedes into the background
	if h.colorize {
		b.WriteString(dimOn)
	}
	b.WriteString("[")
	b.WriteString(ts.Format("2006-01-02 15:04:05.000"))
	b.WriteString("]")
	if h.colorize {
		b.WriteString(dimOff)
	}

	// Level — brackets dim, text coloured by level
	b.WriteString(" ")
	if h.colorize {
		b.WriteString(dimOn + "[" + dimOff)
	} else {
		b.WriteString("[")
	}
	b.WriteString(levelLabel)
	if h.colorize {
		b.WriteString(dimOn + "]" + dimOff)
	} else {
		b.WriteString("]")
	}
	b.WriteString(" ")

	// Source (verbose mode) — dimmed
	if h.addSource {
		if source := sourceFromPC(record.PC); source != "" {
			if h.colorize {
				b.WriteString(dimOn)
				b.WriteString(source)
				b.WriteString(dimOff)
			} else {
				b.WriteString(source)
			}
			b.WriteString(": ")
		}
	}

	category := ""
	fields := make([]string, 0, 8)

	for _, scoped := range h.attrs {
		collectPrettyField(scoped.attr, scoped.groups, &category, &fields, h.colorize, h.verbose)
	}

	record.Attrs(func(attr slog.Attr) bool {
		collectPrettyField(attr, h.groups, &category, &fields, h.colorize, h.verbose)
		return true
	})

	// Category — bold so it's the first thing you see
	if category != "" {
		if h.colorize {
			b.WriteString(boldOn)
			b.WriteString(category)
			b.WriteString(dimOff)
		} else {
			b.WriteString(category)
		}
		b.WriteString(": ")
	}

	b.WriteString(record.Message)

	if len(fields) > 0 {
		if h.verbose {
			for _, field := range fields {
				b.WriteString("\n    ")
				b.WriteString(field)
			}
		} else {
			if h.colorize {
				b.WriteString(dimOn + " (" + dimOff)
			} else {
				b.WriteString(" (")
			}
			b.WriteString(strings.Join(fields, " - "))
			if h.colorize {
				b.WriteString(dimOn + ")" + dimOff)
			} else {
				b.WriteString(")")
			}
		}
	}
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *prettyConsoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := h.clone()
	for _, attr := range attrs {
		clone.attrs = append(clone.attrs, scopedAttr{groups: cloneStrings(h.groups), attr: attr})
	}
	return clone
}

func (h *prettyConsoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := h.clone()
	clone.groups = append(clone.groups, name)
	return clone
}

func (h *prettyConsoleHandler) clone() *prettyConsoleHandler {
	attrs := make([]scopedAttr, len(h.attrs))
	copy(attrs, h.attrs)
	return &prettyConsoleHandler{
		w:         h.w,
		mu:        h.mu,
		addSource: h.addSource,
		verbose:   h.verbose,
		colorize:  h.colorize,
		groups:    cloneStrings(h.groups),
		attrs:     attrs,
	}
}

func cloneStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func collectPrettyField(attr slog.Attr, groups []string, category *string, fields *[]string, colorize, verbose bool) {
	value := attr.Value.Resolve()

	if value.Kind() == slog.KindGroup {
		nextGroups := cloneStrings(groups)
		if attr.Key != "" {
			nextGroups = append(nextGroups, attr.Key)
		}
		for _, nested := range value.Group() {
			collectPrettyField(nested, nextGroups, category, fields, colorize, verbose)
		}
		return
	}

	key := attr.Key
	isVerboseOnly := false
	if strings.HasPrefix(key, fieldKeyVerbosePrefix) {
		key = strings.TrimPrefix(key, fieldKeyVerbosePrefix)
		isVerboseOnly = true
	}
	if isVerboseOnly && !verbose {
		return
	}

	if len(groups) > 0 {
		key = strings.Join(append(cloneStrings(groups), key), ".")
	}

	valueText := formatPrettyValue(value)
	if key == "category" && *category == "" {
		*category = valueText
		return
	}

	if colorize {
		*fields = append(*fields, fmt.Sprintf("\x1b[2m%s\x1b[0m: %s", key, valueText))
	} else {
		*fields = append(*fields, fmt.Sprintf("%s: %s", key, valueText))
	}
}

func formatPrettyValue(v slog.Value) string {
	v = v.Resolve()
	switch v.Kind() {
	case slog.KindTime:
		return v.Time().Format(time.RFC3339Nano)
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindString:
		return v.String()
	case slog.KindBool:
		if v.Bool() {
			return "true"
		}
		return "false"
	case slog.KindAny:
		return fmt.Sprintf("%v", v.Any())
	default:
		return v.String()
	}
}

func sourceFromPC(pc uintptr) string {
	if pc == 0 {
		return ""
	}
	frames := runtime.CallersFrames([]uintptr{pc})
	frame, _ := frames.Next()
	if frame.File == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", filepath.Base(frame.File), frame.Line)
}

func colorizeLevel(level string) string {
	const (
		reset  = "\x1b[0m"
		gray   = "\x1b[90m"
		blue   = "\x1b[36m"
		yellow = "\x1b[33m"
		red    = "\x1b[31m"
	)

	color := ""
	switch level {
	case "VERBOSE", "VRB":
		color = gray
	case "DEBUG", "DBG":
		color = blue
	case "WARN", "WRN":
		color = yellow
	case "ERROR", "ERR":
		color = red
	}

	if color == "" {
		return level
	}
	return color + level + reset
}

// -------------------------------------------------------------------------
// Syslog writer (unchanged)
// -------------------------------------------------------------------------

// syslogWriter implements io.Writer and sends messages to a remote syslog server
// using BSD syslog (RFC 3164) format which is widely compatible.
type syslogWriter struct {
	conn net.Conn
	tag  string
}

func (w *syslogWriter) Write(p []byte) (int, error) {
	// RFC 3164 format: <priority>timestamp hostname tag: message
	// Priority 14 = facility(user=1) * 8 + severity(info=6)
	msg := fmt.Sprintf("<%d>%s %s: %s",
		14,
		time.Now().Format(time.Stamp),
		w.tag,
		string(p),
	)
	_, err := fmt.Fprint(w.conn, msg)
	if err != nil {
		// Don't propagate syslog errors to prevent log loops
		return len(p), nil
	}
	return len(p), nil
}

// TestSyslogConnection tests connectivity to a syslog server
func TestSyslogConnection(protocol, host string) error {
	protocol = strings.ToLower(protocol)
	if protocol != "udp" && protocol != "tcp" {
		return fmt.Errorf("unsupported protocol: %s (use udp or tcp)", protocol)
	}
	if host == "" {
		return fmt.Errorf("syslog host is empty")
	}

	conn, err := net.DialTimeout(protocol, host, 5*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer conn.Close()

	// Send a test message
	testMsg := fmt.Sprintf("<%d>%s %s: test connection from network-list-sync\n",
		14, time.Now().Format(time.Stamp), "network-list-sync")
	_, err = fmt.Fprint(conn, testMsg)
	return err
}
