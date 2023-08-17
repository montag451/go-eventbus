package eventbus

import (
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testEvent string

func (e testEvent) Name() EventName {
	return EventName(e)
}

var (
	testEvent1 = testEvent("test.event1")
	testEvent2 = testEvent("test.event2")
)

func noop(Event, time.Time) {}

func newTestBus(t testing.TB) *Bus {
	b := New()
	t.Cleanup(b.Close)
	return b
}

func subscribe(t testing.TB, b *Bus, n EventName, f HandlerFunc, opts ...SubscribeOption) *Handler {
	return subscribePattern(t, b, n, f, opts...)
}

func subscribePattern(t testing.TB, b *Bus, p EventNamePattern, f HandlerFunc, opts ...SubscribeOption) *Handler {
	h, _ := b.SubscribePattern(p, f, opts...)
	t.Cleanup(func() {
		b.Unsubscribe(h)
	})
	return h
}

func assertHasSubscribers(t testing.TB, b *Bus, name EventName) {
	t.Helper()
	if ok, _ := b.HasSubscribers(name); !ok {
		t.Errorf("bus has no subscribers for %q, expected at least one", name)
	}
}

func assertHasNoSubscribers(t testing.TB, b *Bus, name EventName) {
	t.Helper()
	if ok, _ := b.HasSubscribers(name); ok {
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

func assertHandlerDrain(t testing.TB, h *Handler, expected bool) {
	t.Helper()
	if h.opts.drain != expected {
		t.Errorf("bad handler drain option: got %t, expected %t", h.opts.drain, expected)
	}
}

func assertHandlerCallOnce(t testing.TB, h *Handler, expected bool) {
	t.Helper()
	if h.opts.callOnce != expected {
		t.Errorf("bad handler call once option: got %t, expected %t", h.opts.callOnce, expected)
	}
}

func assertNumberOfEvents(t testing.TB, got uint64, expected uint64) {
	t.Helper()
	if got != expected {
		t.Errorf("bad number of events: got %d, expected %d", got, expected)
	}
}

func TestSubscribeNoPattern(t *testing.T) {
	b := newTestBus(t)
	name := EventName("test.event1")
	h := subscribe(t, b, name, noop)
	assertHandlerPattern(t, h, name)
	assertHasSubscribers(t, b, name)
}

func TestSubscribeWildcardPattern(t *testing.T) {
	b := newTestBus(t)
	t.Run("Simple", func(t *testing.T) {
		pattern := WildcardPattern("test.*")
		assertHasNoSubscribers(t, b, "test.event1")
		assertHasNoSubscribers(t, b, "test.event2")
		assertHasNoSubscribers(t, b, "test1.event1")
		h := subscribePattern(t, b, pattern, noop)
		assertHandlerPattern(t, h, pattern)
		assertHasSubscribers(t, b, "test.event1")
		assertHasSubscribers(t, b, "test.event2")
		assertHasNoSubscribers(t, b, "test1.event1")
	})
	t.Run("All", func(t *testing.T) {
		pattern := WildcardPattern("*")
		assertHasNoSubscribers(t, b, "test.event1")
		assertHasNoSubscribers(t, b, "test.event2")
		assertHasNoSubscribers(t, b, "test1.event1")
		h := subscribePattern(t, b, pattern, noop)
		assertHandlerPattern(t, h, pattern)
		assertHasSubscribers(t, b, "test.event1")
		assertHasSubscribers(t, b, "test.event2")
		assertHasSubscribers(t, b, "test1.event1")
	})
	t.Run("NoWildcard", func(t *testing.T) {
		pattern := WildcardPattern("test")
		assertHasNoSubscribers(t, b, "test")
		assertHasNoSubscribers(t, b, "test.event1")
		assertHasNoSubscribers(t, b, "test.event2")
		h := subscribePattern(t, b, pattern, noop)
		if _, ok := pattern.(EventName); !ok {
			t.Error("wildcard pattern with no wildcard should be an EventName")
		}
		assertHandlerPattern(t, h, pattern)
		assertHasSubscribers(t, b, "test")
		assertHasNoSubscribers(t, b, "test.event1")
		assertHasNoSubscribers(t, b, "test.event2")
	})
}

func TestSubscribeRegexPattern(t *testing.T) {
	b := newTestBus(t)
	pattern := RegexPattern(regexp.MustCompile(`test\.event\d+$`))
	h := subscribePattern(t, b, pattern, noop)
	assertHasSubscribers(t, b, `test.event1`)
	assertHasSubscribers(t, b, `_test.event42`)
	assertHasNoSubscribers(t, b, `test.event`)
	assertHasNoSubscribers(t, b, `test.event42a`)
	assertHandlerPattern(t, h, pattern)
}

func TestSubscribeDefaultOptions(t *testing.T) {
	b := newTestBus(t)
	h := subscribe(t, b, "", noop)
	assertHandlerName(t, h, "")
	assertHandlerQueueSize(t, h, defaultQueueSize)
	assertHandlerDrain(t, h, true)
	assertHandlerCallOnce(t, h, false)
}

func TestSubscribeOptions(t *testing.T) {
	b := newTestBus(t)
	opts := []SubscribeOption{
		WithName("foo"),
		WithQueueSize(42),
		NoDrain(),
		CallOnce(),
	}
	h := subscribe(t, b, "", noop, opts...)
	assertHandlerName(t, h, "foo")
	assertHandlerQueueSize(t, h, 42)
	assertHandlerDrain(t, h, false)
	assertHandlerCallOnce(t, h, true)
}

func TestUnsubscribe(t *testing.T) {
	b := newTestBus(t)
	name := EventName("test.event1")
	h := subscribe(t, b, name, noop)
	assertHasSubscribers(t, b, name)
	b.Unsubscribe(h)
	assertHasNoSubscribers(t, b, name)
}

func TestPublish(t *testing.T) {
	b := newTestBus(t)
	t.Run("Regular", func(t *testing.T) {
		unsubscribed := make(chan struct{})
		var n uint64
		h := subscribe(t, b, "test.event1", func(e Event, t time.Time) {
			atomic.AddUint64(&n, 1)
		}, WithUnsubscribedHandler(func() {
			close(unsubscribed)
		}))
		b.Publish(testEvent1)
		b.Publish(testEvent1)
		b.Publish(testEvent2)
		b.Unsubscribe(h)
		<-unsubscribed
		assertNumberOfEvents(t, atomic.LoadUint64(&n), 2)
	})
	t.Run("Sync", func(t *testing.T) {
		var n uint64
		subscribe(t, b, "test.event1", func(e Event, t time.Time) {
			atomic.AddUint64(&n, 1)
		})
		b.PublishSync(testEvent1)
		b.PublishSync(testEvent1)
		b.PublishSync(testEvent2)
		assertNumberOfEvents(t, atomic.LoadUint64(&n), 2)
	})
	t.Run("Async", func(t *testing.T) {
		unsubscribed := make(chan struct{})
		var n uint64
		h := subscribe(t, b, "test.event1", func(e Event, t time.Time) {
			atomic.AddUint64(&n, 1)
		}, WithUnsubscribedHandler(func() {
			close(unsubscribed)
		}))
		b.PublishAsync(testEvent1)
		b.PublishAsync(testEvent1)
		b.PublishAsync(testEvent2)
		b.Unsubscribe(h)
		<-unsubscribed
		assertNumberOfEvents(t, atomic.LoadUint64(&n), 2)
	})
	t.Run("Drop", func(t *testing.T) {
		var wg sync.WaitGroup
		var n uint64
		done := make(chan struct{})
		wait := make(chan struct{})
		h1 := subscribePattern(t, b, WildcardPattern("test.*"), func(e Event, t time.Time) {
			atomic.AddUint64(&n, 1)
			done <- struct{}{}
			<-wait
		}, WithQueueSize(1), WithUnsubscribedHandler(wg.Done))
		wg.Add(1)
		var dropped Event
		event2 := testEvent2
		h2 := subscribe(t, b, "_bus.dropped", func(e Event, t time.Time) {
			dropped = e.(Dropped).Event
		}, WithUnsubscribedHandler(wg.Done))
		wg.Add(1)
		b.PublishAsync(testEvent1)
		<-done
		b.PublishAsync(testEvent1)
		b.PublishAsync(event2)
		close(wait)
		<-done
		b.Unsubscribe(h1)
		b.Unsubscribe(h2)
		wg.Wait()
		assertNumberOfEvents(t, atomic.LoadUint64(&n), 2)
		if dropped != event2 {
			t.Errorf("unexpected dropped event: got %#v, expected: %#v", dropped, event2)
		}
	})
	t.Run("NoDrain", func(t *testing.T) {
		var n uint64
		wait := make(chan struct{})
		defer close(wait)
		h := subscribePattern(t, b, WildcardPattern("test.*"), func(e Event, t time.Time) {
			<-wait
			atomic.AddUint64(&n, 1)
		}, NoDrain())
		b.PublishAsync(testEvent1)
		b.Unsubscribe(h)
		assertNumberOfEvents(t, atomic.LoadUint64(&n), 0)
	})
	t.Run("CallOnce", func(t *testing.T) {
		var n uint64
		h := subscribePattern(t, b, WildcardPattern("test.*"), func(e Event, t time.Time) {
			atomic.AddUint64(&n, 1)
		}, CallOnce())
		b.PublishSync(testEvent1)
		b.PublishSync(testEvent1)
		b.Unsubscribe(h)
		assertNumberOfEvents(t, atomic.LoadUint64(&n), 1)
	})
}
