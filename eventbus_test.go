package eventbus

import (
	"testing"
	"time"
)

func newTestBus(t testing.TB) *Bus {
	t.Helper()
	b := New()
	t.Cleanup(b.Close)
	return b
}

func assertHandlerPattern(t testing.TB, h *Handler, expected EventNamePattern) {
	t.Helper()
	if h.Pattern() != expected {
		t.Errorf("bad handler pattern: got %q, expected %q", h.Pattern(), expected)
	}
}

func assertHandlerName(t testing.TB, h *Handler, expected string) {
	t.Helper()
	if h.Name() != expected {
		t.Errorf("bad handler name: got %q, expected %q", h.Name(), expected)
	}
}

func assertHandlerQueueSize(t testing.TB, h *Handler, expected int) {
	t.Helper()
	if h.QueueSize() != expected {
		t.Errorf("bad handler queue size: got %d, expected %q", h.QueueSize(), expected)
	}
}

func TestSubscribe(t *testing.T) {
	b := newTestBus(t)
	t.Run("PatternNoWilcards", func(t *testing.T) {
		pattern := EventNamePattern("test")
		h := b.Subscribe(pattern, func(Event, time.Time) {})
		assertHandlerPattern(t, h, pattern)
	})
	t.Run("PatternWilcards", func(t *testing.T) {
		pattern := EventNamePattern("test.*")
		h := b.Subscribe(pattern, func(Event, time.Time) {})
		assertHandlerPattern(t, h, pattern)
	})
	t.Run("NoOption", func(t *testing.T) {
		pattern := EventNamePattern("test")
		h := b.Subscribe(pattern, func(Event, time.Time) {})
		assertHandlerName(t, h, "")
		assertHandlerQueueSize(t, h, defaultQueueSize)
	})
	t.Run("NameOption", func(t *testing.T) {
		pattern := EventNamePattern("test")
		name := "foo"
		h := b.Subscribe(pattern, func(Event, time.Time) {}, WithName(name))
		assertHandlerName(t, h, name)
	})
	t.Run("QueueSizeOption", func(t *testing.T) {
		pattern := EventNamePattern("test")
		size := defaultQueueSize * 2
		h := b.Subscribe(pattern, func(Event, time.Time) {}, WithQueueSize(size))
		assertHandlerQueueSize(t, h, size)
	})
}

func TestPublish(t *testing.T) {
}

func TestClose(t *testing.T) {
}
