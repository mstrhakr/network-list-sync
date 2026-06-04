package logging

import (
	"sync"
	"testing"
	"time"
)

func newTestManager() *Manager {
	m := &Manager{
		config: DefaultConfig(),
		ring:   newEventRing(ringCapacity),
	}
	m.workerCond = sync.NewCond(&m.mu)
	return m
}

func TestConfigureWithWarningsDoesNotDeadlock(t *testing.T) {
	m := newTestManager()
	t.Cleanup(m.Close)

	cfg := DefaultConfig()
	cfg.LogToStdout = false
	cfg.SyslogEnabled = true
	cfg.SyslogProtocol = "tcp"
	cfg.SyslogHost = "127.0.0.1:1" // connection refused => warning path

	done := make(chan struct{})
	go func() {
		_ = m.Configure(cfg)
		close(done)
	}()

	select {
	case <-done:
		return
	case <-time.After(2 * time.Second):
		t.Fatal("Configure appears to deadlock when warnings are emitted")
	}
}
