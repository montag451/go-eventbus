// Package eventbus provides a simple event bus implementation.
//
// The event bus provided by this package supports asynchronous and
// synchronous publishing and wildcard subscription.
package eventbus

import (
	"errors"
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
	mu       sync.Mutex
	opts     subscribeOptions
	closed   bool
	fn       HandlerFunc
	p        EventNamePattern
	re       *regexp.Regexp
	syncPubs sync.WaitGroup
	ch       chan event
	stop     chan struct{}
	done     chan struct{}
}

// Pattern returns the handler pattern.
func (h *Handler) Pattern() EventNamePattern {
	return h.p
}

// Name returns the handler name.
func (h *Handler) Name() string {
	return h.opts.name
}

// QueueSize returns the handler queue size.
func (h *Handler) QueueSize() int {
	return h.opts.queueSize
}

func (h *Handler) init() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ch != nil || h.closed {
		return
	}
	h.ch = make(chan event, h.opts.queueSize)
	h.stop = make(chan struct{})
	h.done = make(chan struct{})
	go h.processEvents()
}

func (h *Handler) publish(e event, sync bool) (published bool, err error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		err = errHandlerClosed
		return
	}
	if sync {
		h.syncPubs.Add(1)
		defer h.syncPubs.Done()
		h.mu.Unlock()
		select {
		case h.ch <- e:
			published = true
		case <-h.stop:
		}
	} else {
		select {
		case h.ch <- e:
			published = true
		default:
		}
		h.mu.Unlock()
	}
	return
}

func (h *Handler) close(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	if f == nil {
		f = h.opts.unsubscribeHandler
	}
	if h.ch == nil {
		if f != nil {
			go f()
		}
		return
	}
	close(h.stop)
	go func() {
		h.syncPubs.Wait()
		// At this point the handler is closed and there is no pending
		// sync publications so we can safely close the channel
		close(h.ch)
		<-h.done
		if f != nil {
			f()
		}
	}()
}

func (h *Handler) processEvent(e event) {
	h.fn(e.e, e.t)
	if e.done != nil {
		e.done <- struct{}{}
	}
}

func (h *Handler) processEvents() {
	defer close(h.done)
Loop:
	for {
		select {
		case e, ok := <-h.ch:
			if !ok {
				return
			}
			h.processEvent(e)
			select {
			case <-h.stop:
				break Loop
			default:
			}
		case <-h.stop:
			break Loop
		}
	}
	for e := range h.ch {
		switch {
		case h.opts.drain:
			h.processEvent(e)
		case e.done != nil:
			// Unblock any pending sync publication
			e.done <- struct{}{}
		}
	}
}

// SubscribeOption configures a Handler as returned by Subscribe.
type SubscribeOption func(*subscribeOptions)

type subscribeOptions struct {
	name               string
	queueSize          int
	unsubscribeHandler func()
	drain              bool
	callOnce           bool
}

// WithName sets the name of the handler.
func WithName(name string) SubscribeOption {
	return func(opts *subscribeOptions) {
		opts.name = name
	}
}

// WithQueueSize sets the queue size of the handler.
func WithQueueSize(size int) SubscribeOption {
	return func(opts *subscribeOptions) {
		if size <= 0 {
			size = 1
		}
		opts.queueSize = size
	}
}

func WithUnsubscribeHandler(f func()) SubscribeOption {
	return func(opts *subscribeOptions) {
		opts.unsubscribeHandler = f
	}
}

// NoDrain prevents Close and Unsubscribe to drain the handler event
// queue.
func NoDrain() SubscribeOption {
	return func(opts *subscribeOptions) {
		opts.drain = false
	}
}

// CallOnce ensures that the handler will be called only once
func CallOnce() SubscribeOption {
	return func(opts *subscribeOptions) {
		opts.callOnce = true
	}
}

var (
	// ErrClosed is the error returned by all bus methods (except
	// Close) if called on a closed bus.
	ErrBusClosed     = errors.New("bus is closed")
	errHandlerClosed = errors.New("handler is closed")
)

// Bus represents an event bus. A Bus is safe for use by multiple
// goroutines simultaneously.
type Bus struct {
	mu       sync.Mutex
	opts     busOptions
	closed   bool
	handlers map[*Handler]struct{}
	events   map[EventName]map[*Handler]struct{}
}

// BusOption configures a Bus as returned by New.
type BusOption func(*busOptions)

type busOptions struct {
	closedHandler func()
}

func WithClosedHandler(f func()) BusOption {
	return func(opts *busOptions) {
		opts.closedHandler = f
	}
}

// New creates a new event bus, ready to be used.
func New(options ...BusOption) *Bus {
	b := &Bus{
		handlers: make(map[*Handler]struct{}),
		events:   make(map[EventName]map[*Handler]struct{}),
	}
	for _, opt := range options {
		opt(&b.opts)
	}
	return b
}

// Close closes the event bus and starts draining, in the background,
// the event queue of all handlers that has not been registered with
// the NoDrain option. When all the handlers have been drained, the
// callback set with WithClosedHandler is called.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	go func() {
		n := len(b.handlers)
		done := make(chan struct{})
		defer close(done)
		f := func() {
			done <- struct{}{}
		}
		for h := range b.handlers {
			h.close(f)
		}
		for n > 0 {
			<-done
			n--
		}
		if b.opts.closedHandler != nil {
			b.opts.closedHandler()
		}
	}()
}

// Subscribe subscribes to all events matching the given pattern. It
// returns a Handler instance representing the subscription.
func (b *Bus) Subscribe(p EventNamePattern, fn HandlerFunc, options ...SubscribeOption) (*Handler, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrBusClosed
	}
	h := &Handler{
		opts: subscribeOptions{
			queueSize: defaultQueueSize,
			drain:     true,
		},
		fn: fn,
		p:  p,
		re: patternToRegex(p),
	}
	for _, opt := range options {
		opt(&h.opts)
	}
	b.handlers[h] = struct{}{}
	b.subscribeAll(h)
	return h, nil
}

// Unsubscribe unsubscribes the given handler for all events matching
// the handler pattern and starts draining, in the background, the
// handler event queue if the given handler has not been registered
// with the NoDrain option. When the handler has been drained, the
// callback set with WithUnsubscribeHandler is called.
func (b *Bus) Unsubscribe(h *Handler) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrBusClosed
	}
	b.unsubscribe(h)
	b.mu.Unlock()
	h.close(nil)
	return nil
}

// HasSubscribers returns true if the given event name has subscribers
// otherwise it returns false.
func (b *Bus) HasSubscribers(name EventName) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false, ErrBusClosed
	}
	b.checkNewEvent(name)
	return len(b.events[name]) > 0, nil
}

// PublishAsync publishes an event asynchronously. It returns as soon
// as the event has been put in the event queue of all the handlers
// subscribed to the event. If the event queue of a handler is full,
// the event is dropped for this handler and a Dropped event is
// generated.
func (b *Bus) PublishAsync(e Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrBusClosed
	}
	b.publishAsync(e)
	return nil
}

// PublishSync publishes an event synchronously. It returns when the
// event has been processed by all the handlers subscribed to the
// event.
func (b *Bus) PublishSync(e Event) error {
	t := time.Now()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrBusClosed
	}
	name := e.Name()
	b.checkNewEvent(name)
	handlers := b.events[name]
	n, ack := len(handlers), 0
	if n == 0 {
		b.mu.Unlock()
		return nil
	}
	var busyHandlers []*Handler
	done := make(chan struct{})
	defer close(done)
	for h := range handlers {
		h.init()
		if h.opts.callOnce {
			b.unsubscribe(h)
		}
		if ok, err := h.publish(event{t, e, done}, false); !ok {
			if err == nil {
				busyHandlers = append(busyHandlers, h)
			} else {
				n--
			}
		}
	}
	b.mu.Unlock()
	for _, h := range busyHandlers {
		go func(h *Handler) {
			if ok, _ := h.publish(event{t, e, done}, true); !ok {
				// The event has not been published because the
				// handler has been unsubscribed in the meantime
				done <- struct{}{}
			}
		}(h)
	}
	for ack < n {
		<-done
		ack++
	}
	return nil
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
		if h.opts.callOnce {
			b.unsubscribe(h)
		}
		if ok, err := h.publish(event{t: t, e: e}, false); !ok && err == nil {
			if _, ok := e.(Dropped); !ok {
				b.publishAsync(Dropped{h, t, e})
			}
		}
	}
}
