// Package eventbus provides a simple event bus implementation.
//
// The event bus provided by this package supports asynchronous and
// synchronous publishing and a flexible event matching
// mechanism. Each handler registered with [Bus.Subscribe] has its own
// event queue used to store events and its own goroutine to process
// events. Each handler processes events in the order they are
// published. When an asynchronous event is published on the bus and
// the event can't be stored in the event queue of a handler because
// its queue is full, the event is dropped for this handler and a
// [Dropped] event is generated by the bus. A slow handler doesn't
// impact other handlers when publishing asynchronous events.
package eventbus

import (
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
)

// EventNamePattern is the interface implemented by all event patterns.
type EventNamePattern interface {
	Match(n EventName) bool
}

// EventName represents the name of an event. It implements
// [EventNamePattern] so it can be used as a pattern in a call to
// [Bus.Subscribe].
type EventName string

func (p EventName) Match(n EventName) bool {
	return p == n
}

type regexPattern struct {
	re *regexp.Regexp
}

func (p regexPattern) Match(n EventName) bool {
	return p.re.MatchString(string(n))
}

// RegexPattern returns a pattern to match against event names. It
// matches all events that match the given regex.
func RegexPattern(re *regexp.Regexp) EventNamePattern {
	return regexPattern{re}
}

type wildcardPattern string

func (p wildcardPattern) Match(n EventName) bool {
	if p == "*" {
		return true
	}
	return p.match(n)
}

func (p wildcardPattern) match(n EventName) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			return p[1:].match(n) || (len(n) > 0 && p.match(n[1:]))
		default:
			if len(n) == 0 || n[0] != p[0] {
				return false
			}
		}
		n = n[1:]
		p = p[1:]
	}
	return len(n) == 0 && len(p) == 0
}

// WildcardPattern returns a pattern to match against event
// names. Only '*' has a special meaning in a pattern, it matches any
// string, including the empty string.
func WildcardPattern(pattern string) EventNamePattern {
	if strings.Index(pattern, "*") != -1 {
		return wildcardPattern(pattern)
	}
	return EventName(pattern)
}

// Event is the interface implemented by all events that are published
// on the bus.
type Event interface {
	Name() EventName
}

type event struct {
	t  time.Time
	e  Event
	wg *sync.WaitGroup
}

// DroppedEventName is the name of the [Dropped] event
const DroppedEventName = EventName("_bus.dropped")

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

func (Dropped) Name() EventName {
	return DroppedEventName
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
	mu               sync.Mutex
	opts             subscribeOptions
	closed           bool
	fn               HandlerFunc
	p                EventNamePattern
	waiters          sync.WaitGroup
	queueFullHandler func() // only used for testing purpose
	queuedHandler    func() // only used for testing purpose
	ch               chan event
	stop             chan struct{}
	done             chan struct{}
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

// PendingEvents returns the number of events in the handler queue.
func (h *Handler) PendingEvents() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.ch)
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

func (h *Handler) setQueueFullHandler(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.queueFullHandler = f
}

func (h *Handler) setQueuedHandler(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.queuedHandler = f
}

func (h *Handler) publish(e event, wait bool) (published bool, err error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		err = errHandlerClosed
		return
	}
	qfh := h.queueFullHandler
	qh := h.queuedHandler
	if wait {
		h.waiters.Add(1)
		defer h.waiters.Done()
		h.mu.Unlock()
		if qfh != nil {
			select {
			case h.ch <- e:
				published = true
			default:
				qfh()
			}
		}
		if !published {
			select {
			case h.ch <- e:
				published = true
			case <-h.stop:
			}
		}
	} else {
		select {
		case h.ch <- e:
			published = true
		default:
		}
		h.mu.Unlock()
	}
	if qh != nil && published {
		qh()
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
	f2 := func() {
		if h.opts.unsubscribedHandler != nil {
			h.opts.unsubscribedHandler()
		}
		f()
	}
	if h.ch == nil {
		if f2 != nil {
			go f2()
		}
		return
	}
	close(h.stop)
	go func() {
		h.waiters.Wait()
		// At this point the handler is closed and there are no more
		// goroutines waiting to write in the channel so it can be
		// safely closed
		close(h.ch)
		<-h.done
		if f2 != nil {
			f2()
		}
	}()
}

func (h *Handler) processEvent(e event) {
	h.fn(e.e, e.t)
	if e.wg != nil {
		e.wg.Done()
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
		case h.opts.drain, h.opts.callOnce:
			h.processEvent(e)
		case e.wg != nil:
			// Unblock any pending sync publication
			e.wg.Done()
		}
	}
}

// SubscribeOption configures a [Handler] as returned by
// [Bus.Subscribe].
type SubscribeOption func(*subscribeOptions)

type subscribeOptions struct {
	name                string
	queueSize           int
	unsubscribedHandler func()
	drain               bool
	callOnce            bool
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

// WithUnsubscribedHandler sets the handler that will be called after
// the handler has been unsubscribed and drained.
func WithUnsubscribedHandler(f func()) SubscribeOption {
	return func(opts *subscribeOptions) {
		opts.unsubscribedHandler = f
	}
}

// WithNoDrain prevents [Bus.Close] and [Bus.Unsubscribe] to drain the
// handler event queue.
func WithNoDrain() SubscribeOption {
	return func(opts *subscribeOptions) {
		opts.drain = false
	}
}

// WithCallOnce ensures that the handler will be called only once
func WithCallOnce() SubscribeOption {
	return func(opts *subscribeOptions) {
		opts.callOnce = true
	}
}

// ErrBusClosed is the error returned by all bus methods (except
// [Bus.Close]) if called on a closed bus. It is the only error
// returned by this package.
var ErrBusClosed = errors.New("bus is closed")

var errHandlerClosed = errors.New("handler is closed")

// Bus represents an event bus. A Bus is safe for use by multiple
// goroutines simultaneously.
type Bus struct {
	mu              sync.Mutex
	opts            busOptions
	closed          bool
	closeWg         sync.WaitGroup
	handlers        map[*Handler]struct{}
	patternHandlers map[*Handler]struct{}
	events          map[EventName]map[*Handler]struct{}
}

// BusOption configures a [Bus] as returned by [New].
type BusOption func(*busOptions)

type busOptions struct {
	closedHandler func()
}

// WithClosedHandler sets the handler that will be called after the
// bus has been closed and all subscribed handlers have been drained.
func WithClosedHandler(f func()) BusOption {
	return func(opts *busOptions) {
		opts.closedHandler = f
	}
}

// New creates a new event bus, ready to be used.
func New(options ...BusOption) *Bus {
	b := &Bus{
		handlers:        make(map[*Handler]struct{}),
		patternHandlers: make(map[*Handler]struct{}),
		events:          make(map[EventName]map[*Handler]struct{}),
	}
	for _, opt := range options {
		opt(&b.opts)
	}
	return b
}

// Close closes the event bus and starts draining, in the background,
// the event queue of all handlers that has not been registered with
// the [WithNoDrain] option. When all the handlers have been drained,
// the callback set with [WithClosedHandler] is called.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	go func() {
		for h := range b.handlers {
			b.unsubscribe(h)
		}
		b.closeWg.Wait()
		if b.opts.closedHandler != nil {
			b.opts.closedHandler()
		}
	}()
}

// Subscribe subscribes to all events matching the given event name
// pattern. It returns a [Handler] instance representing the
// subscription.
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
	}
	for _, opt := range options {
		opt(&h.opts)
	}
	b.handlers[h] = struct{}{}
	if _, ok := h.p.(EventName); !ok {
		b.patternHandlers[h] = struct{}{}
	}
	b.subscribeAll(h)
	return h, nil
}

// Unsubscribe unsubscribes the given handler for all events matching
// the handler pattern and starts draining, in the background, the
// handler event queue if the given handler has not been registered
// with the [WithNoDrain] option. When the handler has been drained,
// the callback set with [WithUnsubscribedHandler] is called.
func (b *Bus) Unsubscribe(h *Handler) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrBusClosed
	}
	b.unsubscribe(h)
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

// Publish publishes an event on the bus and returns when the event
// has been put in the event queue of all the handlers subscribed to
// the event.
func (b *Bus) Publish(e Event) error {
	return b.publish(e, false)
}

// PublishSync publishes an event synchronously. It returns when the
// event has been processed by all the handlers subscribed to the
// event.
func (b *Bus) PublishSync(e Event) error {
	return b.publish(e, true)
}

// PublishAsync publishes an event asynchronously. It returns as soon
// as the event has been put in the event queue of all the handlers
// subscribed to the event. If the event queue of a handler is full,
// the event is dropped for this handler and a [Dropped] event is
// generated.
func (b *Bus) PublishAsync(e Event) error {
	t := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrBusClosed
	}
	b.publishAsync(event{t: t, e: e})
	return nil
}

func (b *Bus) subscribe(n EventName, h *Handler) {
	m := b.events[n]
	if m == nil {
		m = make(map[*Handler]struct{})
		b.events[n] = m
	}
	m[h] = struct{}{}
}

func (b *Bus) maybeSubscribe(n EventName, h *Handler) {
	if !h.p.Match(n) {
		return
	}
	b.subscribe(n, h)
}

func (b *Bus) subscribeAll(h *Handler) {
	if n, ok := h.p.(EventName); ok {
		b.subscribe(n, h)
	} else {
		for n := range b.events {
			b.maybeSubscribe(n, h)
		}
	}
}

func (b *Bus) unsubscribe(h *Handler) {
	delete(b.handlers, h)
	delete(b.patternHandlers, h)
	for _, handlers := range b.events {
		delete(handlers, h)
	}
	b.closeWg.Add(1)
	h.close(b.closeWg.Done)
}

func (b *Bus) checkNewEvent(name EventName) {
	if _, ok := b.events[name]; !ok {
		b.events[name] = nil
		for h := range b.patternHandlers {
			b.maybeSubscribe(name, h)
		}
	}
}

func (b *Bus) publish(e Event, synch bool) error {
	ev := event{t: time.Now(), e: e}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrBusClosed
	}
	name := e.Name()
	b.checkNewEvent(name)
	handlers := b.events[name]
	n := len(handlers)
	if n == 0 {
		b.mu.Unlock()
		return nil
	}
	var busyHandlers []*Handler
	var wg sync.WaitGroup
	wg.Add(n)
	if synch {
		ev.wg = &wg
	}
	for h := range handlers {
		h.init()
		ok, err := h.publish(ev, false)
		switch {
		case ok && !synch, err != nil:
			wg.Done()
		case !ok:
			busyHandlers = append(busyHandlers, h)
		}
		if h.opts.callOnce {
			// Here the event is in the queue of the handler so it is
			// safe to unsubscribe and to close the handler
			b.unsubscribe(h)
		}
	}
	b.mu.Unlock()
	for _, h := range busyHandlers {
		go func(h *Handler) {
			if ok, _ := h.publish(ev, true); !ok || !synch {
				// The event has not been published because the
				// handler has been unsubscribed in the meantime or
				// it's not a synchronous publish
				wg.Done()
			}
		}(h)
	}
	wg.Wait()
	return nil
}

func (b *Bus) publishAsync(e event) {
	name := e.e.Name()
	b.checkNewEvent(name)
	for h := range b.events[name] {
		h.init()
		if ok, err := h.publish(e, false); !ok && err == nil {
			if _, ok := e.e.(Dropped); !ok {
				b.publishAsync(event{
					t: time.Now(),
					e: Dropped{h, e.t, e.e},
				})
			}
		}
		if h.opts.callOnce {
			// Here the event is in the queue of the handler so it is
			// safe to unsubscribe and to close the handler
			b.unsubscribe(h)
		}
	}
}
