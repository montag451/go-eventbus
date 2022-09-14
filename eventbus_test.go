package eventbus

import (
	"testing"
	"time"
)

type TestEvent1 struct{}

func (TestEvent1) Name() EventName {
	return "test.event1"
}

type TestEvent2 struct{}

func (TestEvent2) Name() EventName {
	return "test.event2"
}

func newTestBus(t testing.TB) *Bus {
	t.Helper()
	b := New()
	t.Cleanup(b.Close)
	return b
}

func subscribe(t testing.TB, b *Bus, p EventNamePattern, f func(Event, time.Time), opts ...Option) *Handler {
	t.Helper()
	h := b.Subscribe(p, f, opts...)
	t.Cleanup(func() {
		b.Unsubscribe(h)
	})
	return h
}

func assertHasSubscribers(t testing.TB, b *Bus, name EventName) {
	t.Helper()
	if !b.HasSubscribers(name) {
		t.Errorf("bus has no subscribers for %q, expected at least one", name)
	}
}

func assertHasNoSubscribers(t testing.TB, b *Bus, name EventName) {
	t.Helper()
	if b.HasSubscribers(name) {
		t.Errorf("bus has subscribers for %q, expected none", name)
	}
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

func assertHandlerNoDrain(t testing.TB, h *Handler) {
	t.Helper()
	if h.drain {
		t.Errorf("handler requires draining, no draining was expected")
	}
}

func assertNumberOfEvents(t testing.TB, got int, expected int) {
	t.Helper()
	if got != expected {
		t.Errorf("bad number of events: got %d, expected %d", got, expected)
	}
}

func TestSubscribe(t *testing.T) {
	b := newTestBus(t)
	t.Run("PatternNoWilcards", func(t *testing.T) {
		name := TestEvent1{}.Name()
		pattern := EventNamePattern(name)
		h := subscribe(t, b, pattern, func(Event, time.Time) {})
		assertHasSubscribers(t, b, name)
		assertHandlerPattern(t, h, pattern)
	})
	t.Run("PatternWilcards", func(t *testing.T) {
		pattern := EventNamePattern("test.*")
		h := subscribe(t, b, pattern, func(Event, time.Time) {})
		assertHasSubscribers(t, b, TestEvent1{}.Name())
		assertHasSubscribers(t, b, TestEvent2{}.Name())
		assertHandlerPattern(t, h, pattern)
	})
	t.Run("NoOption", func(t *testing.T) {
		name := TestEvent1{}.Name()
		pattern := EventNamePattern(name)
		h := subscribe(t, b, pattern, func(Event, time.Time) {})
		assertHasSubscribers(t, b, name)
		assertHandlerName(t, h, "")
		assertHandlerQueueSize(t, h, defaultQueueSize)
	})
	t.Run("NameOption", func(t *testing.T) {
		name := TestEvent1{}.Name()
		pattern := EventNamePattern(name)
		handlerName := "foo"
		h := subscribe(t, b, pattern, func(Event, time.Time) {}, WithName(handlerName))
		assertHasSubscribers(t, b, name)
		assertHandlerName(t, h, handlerName)
	})
	t.Run("QueueSizeOption", func(t *testing.T) {
		name := TestEvent1{}.Name()
		pattern := EventNamePattern(name)
		size := defaultQueueSize * 2
		h := subscribe(t, b, pattern, func(Event, time.Time) {}, WithQueueSize(size))
		assertHasSubscribers(t, b, name)
		assertHandlerQueueSize(t, h, size)
	})
	t.Run("NoDrainOption", func(t *testing.T) {
		name := TestEvent1{}.Name()
		pattern := EventNamePattern(name)
		h := subscribe(t, b, pattern, func(Event, time.Time) {}, WithNoDrain())
		assertHasSubscribers(t, b, name)
		assertHandlerNoDrain(t, h)
	})
}

func TestUnsubscribe(t *testing.T) {
	b := newTestBus(t)
	name := TestEvent1{}.Name()
	pattern := EventNamePattern(name)
	h := subscribe(t, b, pattern, func(Event, time.Time) {})
	assertHasSubscribers(t, b, name)
	b.Unsubscribe(h)
	assertHasNoSubscribers(t, b, name)
}

func TestPublish(t *testing.T) {
	b := newTestBus(t)
	t.Run("Sync", func(t *testing.T) {
		n := 0
		subscribe(t, b, "test.event1", func(e Event, t time.Time) {
			n++
		})
		b.PublishSync(TestEvent1{})
		b.PublishSync(TestEvent1{})
		b.PublishSync(TestEvent2{})
		assertNumberOfEvents(t, n, 2)
	})
	t.Run("SyncWildcard", func(t *testing.T) {
		n := 0
		subscribe(t, b, "test.*", func(e Event, t time.Time) {
			n++
		})
		b.PublishSync(TestEvent1{})
		b.PublishSync(TestEvent1{})
		b.PublishSync(TestEvent2{})
		assertNumberOfEvents(t, n, 3)
	})
	t.Run("Async", func(t *testing.T) {
		n := 0
		h := subscribe(t, b, "test.event1", func(e Event, t time.Time) {
			n++
		})
		b.PublishAsync(TestEvent1{})
		b.PublishAsync(TestEvent1{})
		b.PublishAsync(TestEvent2{})
		b.Unsubscribe(h)
		assertNumberOfEvents(t, n, 2)
	})
	t.Run("AsyncWildcard", func(t *testing.T) {
		n := 0
		h := subscribe(t, b, "test.*", func(e Event, t time.Time) {
			n++
		})
		b.PublishAsync(TestEvent1{})
		b.PublishAsync(TestEvent1{})
		b.PublishAsync(TestEvent2{})
		b.Unsubscribe(h)
		assertNumberOfEvents(t, n, 3)
	})
	t.Run("Drop", func(t *testing.T) {
		n := 0
		done := make(chan struct{})
		wait := make(chan struct{})
		h1 := subscribe(t, b, "test.*", func(e Event, t time.Time) {
			n++
			done <- struct{}{}
			<-wait
		}, WithQueueSize(1))
		var dropped Event
		event2 := TestEvent2{}
		h2 := subscribe(t, b, "_bus.dropped", func(e Event, t time.Time) {
			dropped = e.(Dropped).Event
		})
		b.PublishAsync(TestEvent1{})
		<-done
		b.PublishAsync(TestEvent1{})
		b.PublishAsync(event2)
		close(wait)
		<-done
		b.Unsubscribe(h1)
		b.Unsubscribe(h2)
		assertNumberOfEvents(t, n, 2)
		if dropped != event2 {
			t.Errorf("unexpected dropped event: %#v", dropped)
		}
	})
	t.Run("NoDrain", func(t *testing.T) {
		n := 0
		wait := make(chan struct{})
		h := subscribe(t, b, "test.*", func(e Event, t time.Time) {
			<-wait
			n++
		}, WithNoDrain())
		b.PublishAsync(TestEvent1{})
		b.Unsubscribe(h)
		assertNumberOfEvents(t, n, 0)
		close(wait)
	})
}

func TestClose(t *testing.T) {
}
