// Package eventbus provides a simple event bus implementation.
//
// The event bus provided by this package supports asynchronous and
// synchronous publishing and wildcard subscription.
package eventbus

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

type (
	// EventName represents the name of an event.
	EventName string
	// EventNamePattern represents a pattern to match against event
	// names. Only '*' has a special meaning in a pattern, it matches
	// any string, including the empty string. To prevent '*' to be
	// interpreted as a wildcard, it must be escaped.
	EventNamePattern string
)

// Event is the interface implemented by all events that are published
// on the bus.
type Event interface {
	Name() EventName
}

type event struct {
	t    time.Time
	e    Event
	done chan struct{}
}

// Dropped is the event published internally by the bus to signal that
// an event has been dropped. This event occurs only when an event is
// published asynchronously and the handler queue is full. Dropped
// events can themselves be dropped (no Dropped event is generated in
// this case) if a handler queue is full.
type Dropped struct {
	Handler   *Handler
	EventTime time.Time
	Event     Event
}

// Name returns the string "_bus.dropped" which is the name of the
// Dropped event.
func (Dropped) Name() EventName {
	return "_bus.dropped"
}

func patternToRegex(p EventNamePattern) *regexp.Regexp {
	pattern := "^"
	sp := string(p)
	for len(sp) > 0 {
		idx := strings.Index(sp, "*")
		if idx == -1 {
			pattern += regexp.QuoteMeta(sp)
			break
		}
		if part := sp[:idx]; idx == 0 || !strings.HasSuffix(part, "\\") {
			pattern += regexp.QuoteMeta(part) + ".*"
		} else {
			pattern += regexp.QuoteMeta(part[:len(part)-1] + "*")
		}
		sp = sp[idx+1:]
	}
	return regexp.MustCompile(pattern + "$")
}

const defaultQueueSize = 100

// HandlerFunc is the type of the function called by the bus to
// process events.
//
// The e argument is the event to process.
// The t argument is the time when the event has been generated.
type HandlerFunc func(e Event, t time.Time)

// Handler represents a subscription to some events.
type Handler struct {
	mu        sync.Mutex
	callOnce  bool
	fn        HandlerFunc
	p         EventNamePattern
	re        *regexp.Regexp
	name      string
	queueSize int
	drain     bool
	ch        chan event
	stop      chan struct{}
	done      chan struct{}
}

// Pattern returns the handler pattern.
func (h *Handler) Pattern() EventNamePattern {
	return h.p
}

// Name returns the handler name.
func (h *Handler) Name() string {
	return h.name
}

// QueueSize returns the handler queue size.
func (h *Handler) QueueSize() int {
	return h.queueSize
}

func (h *Handler) init() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ch != nil {
		return
	}
	h.ch = make(chan event, h.queueSize)
	h.stop = make(chan struct{})
	h.done = make(chan struct{})
	go h.processEvents()
}

func (h *Handler) close() {
	f := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.ch == nil {
			return
		}
		close(h.ch)
		if !h.drain {
			close(h.stop)
		}
		<-h.done
		if h.drain {
			close(h.stop)
		}
		h.ch = nil
	}
	if h.drain {
		f()
	} else {
		go f()
	}
}

func (h *Handler) processEvents() {
	defer close(h.done)
	for {
		select {
		case e, ok := <-h.ch:
			if !ok {
				return
			}
			select {
			case <-h.stop:
				return
			default:
				h.fn(e.e, e.t)
				if e.done != nil {
					e.done <- struct{}{}
				}
			}
		case <-h.stop:
			return
		}
	}
}

// SubscribeOption configures a Handler as returned by Subscribe.
type SubscribeOption func(*Handler)

// WithName sets the name of the handler.
func WithName(name string) SubscribeOption {
	return func(h *Handler) {
		h.name = name
	}
}

// WithQueueSize sets the queue size of the handler.
func WithQueueSize(size int) SubscribeOption {
	return func(h *Handler) {
		if size <= 0 {
			size = 1
		}
		h.queueSize = size
	}
}

// WithNoDrain prevents Close and Unsubscribe to drain the handler
// event queue before returning.
func WithNoDrain() SubscribeOption {
	return func(h *Handler) {
		h.drain = false
	}
}

// WithCallOnce ensures that the handler will be called only once
func WithCallOnce() SubscribeOption {
	return func(h *Handler) {
		h.callOnce = true
	}
}

// Bus represents an event bus. A Bus is safe for use by multiple
// goroutines simultaneously.
type Bus struct {
	mu       sync.Mutex
	closed   bool
	wg       sync.WaitGroup
	handlers map[*Handler]struct{}
	events   map[EventName]map[*Handler]struct{}
}

// New creates a new event bus, ready to be used.
func New() *Bus {
	return &Bus{
		handlers: make(map[*Handler]struct{}),
		events:   make(map[EventName]map[*Handler]struct{}),
	}
}

// Close closes the event bus. It drains the event queue of all
// handlers that has not been registered with the WithNoDrain option
// before returning. Calling any method (except Close which does
// nothing if the bus is already closed) on a closed bus will
// panic. Calling Close in a handler which has not been registered
// with the WithNoDrain option will deadlock if the callback is
// invoked to process an event asynchronously. For example:
//
//	b := eventbus.New()
//	var h *eventbus.Handler
//	h = b.Subscribe("foo", func(Event, time.Time) {
//		b.Close(b)
//	})
//	b.PublishAsync(FooEvent{})
//
// will deadlock while:
//
//	b := eventbus.New()
//	var h *eventbus.Handler
//	h = b.Subscribe("foo", func(Event, time.Time) {
//		b.Close(b)
//	}, eventbus.WithNoDrain())
//	b.PublishAsync(FooEvent{})
//
// will not.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	b.wg.Wait()
	for h := range b.handlers {
		h.close()
	}
}

// Subscribe subscribes to all events matching the given pattern. It
// returns a Handler instance representing the subscription.
func (b *Bus) Subscribe(p EventNamePattern, fn HandlerFunc, options ...SubscribeOption) *Handler {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checkClosed()
	h := &Handler{
		fn:        fn,
		p:         p,
		re:        patternToRegex(p),
		queueSize: defaultQueueSize,
		drain:     true,
	}
	for _, opt := range options {
		opt(h)
	}
	b.handlers[h] = struct{}{}
	b.subscribeAll(h)
	return h
}

// Unsubscribe unsubscribes the given handler for all events matching
// the handler pattern. It drains the handler event queue before
// returning if the given handler has not been registered with the
// WithNoDrain option. Calling Unsubscribe from the handler callback
// will result in a deadlock if the handler has not been registered
// with the WithNoDrain option and the callback is invoked to process
// an event asynchronously. For example:
//
//	b := eventbus.New()
//	var h *eventbus.Handler
//	h = b.Subscribe("foo", func(Event, time.Time) {
//		b.Unsubscribe(h)
//	})
//	b.PublishAsync(FooEvent{})
//
// will deadlock while:
//
//	b := eventbus.New()
//	var h *eventbus.Handler
//	h = b.Subscribe("foo", func(Event, time.Time) {
//		b.Unsubscribe(h)
//	}, eventbus.WithNoDrain())
//	b.PublishAsync(FooEvent{})
//
// will not.
func (b *Bus) Unsubscribe(h *Handler) {
	b.mu.Lock()
	b.unsubscribe(h)
	b.mu.Unlock()
	h.close()
}

// HasSubscribers returns true if the given event name has subscribers
// otherwise it returns false.
func (b *Bus) HasSubscribers(name EventName) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checkNewEvent(name)
	return len(b.events[name]) > 0
}

// PublishAsync publishes an event asynchronously. It returns as soon
// as the event has been put in the event queue of all the handlers
// subscribed to the event. If the event queue of a handler is full,
// the event is dropped for this handler and a Dropped event is
// generated.
func (b *Bus) PublishAsync(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checkClosed()
	b.publishAsync(e)
}

// PublishSync publishes an event synchronously. It returns when the
// event has been processed by all the handlers subscribed to the
// event.
func (b *Bus) PublishSync(e Event) {
	t := time.Now()
	b.mu.Lock()
	func() {
		defer func() {
			if e := recover(); e != nil {
				b.mu.Unlock()
				panic(e)
			}
		}()
		b.checkClosed()
	}()
	name := e.Name()
	b.checkNewEvent(name)
	handlers := b.events[name]
	n, ack := len(handlers), 0
	if n == 0 {
		b.mu.Unlock()
		return
	}
	var busyHandlers []*Handler
	done := make(chan struct{})
	defer close(done)
	for h := range handlers {
		h.init()
		if h.callOnce {
			b.unsubscribe(h)
		}
		select {
		case h.ch <- event{t, e, done}:
		default:
			busyHandlers = append(busyHandlers, h)
		}
	}
	b.wg.Add(1)
	defer b.wg.Done()
	b.mu.Unlock()
	for _, h := range busyHandlers {
		go func(h *Handler) {
			h.ch <- event{t, e, done}
		}(h)
	}
	for ack < n {
		<-done
		ack++
	}
}

// checkClosed must be called with the lock held.
func (b *Bus) checkClosed() {
	if b.closed {
		panic("closed bus")
	}
}

// subscribe must be called with the lock held.
func (b *Bus) subscribe(n EventName, h *Handler) {
	if !h.re.MatchString(string(n)) {
		return
	}
	m := b.events[n]
	if m == nil {
		m = make(map[*Handler]struct{})
		b.events[n] = m
	}
	m[h] = struct{}{}
}

// subscribeAll must be called with the lock held.
func (b *Bus) subscribeAll(h *Handler) {
	for n := range b.events {
		b.subscribe(n, h)
	}
}

// unsubscribe must be called with the lock held.
func (b *Bus) unsubscribe(h *Handler) {
	delete(b.handlers, h)
	for _, handlers := range b.events {
		delete(handlers, h)
	}
}

// checkNewEvent must be called with the lock held.
func (b *Bus) checkNewEvent(name EventName) {
	if _, ok := b.events[name]; !ok {
		b.events[name] = nil
		for h := range b.handlers {
			b.subscribe(name, h)
		}
	}
}

// publishAsync must be called with the lock held.
func (b *Bus) publishAsync(e Event) {
	name := e.Name()
	b.checkNewEvent(name)
	t := time.Now()
	for h := range b.events[name] {
		h.init()
		if h.callOnce {
			b.unsubscribe(h)
		}
		select {
		case h.ch <- event{t: t, e: e}:
		default:
			if _, ok := e.(Dropped); !ok {
				b.publishAsync(Dropped{h, t, e})
			}
		}
	}
}
