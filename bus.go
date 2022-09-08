package eventbus

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

type (
	EventName        string
	EventNamePattern string
)

type Event interface {
	Name() EventName
}

type eventWithTime struct {
	t time.Time
	e Event
}

type Dropped struct {
	Handler   *Handler
	eventTime time.Time
	event     Event
}

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

type Handler struct {
	fn        func(Event, time.Time)
	p         *regexp.Regexp
	name      string
	queueSize uint
	ch        chan eventWithTime
	stop      chan struct{}
	done      chan struct{}
}

func (h *Handler) Name() string {
	return h.name
}

func (h *Handler) QueueSize() uint {
	return h.queueSize
}

type Option func(*Handler)

func WithName(name string) Option {
	return func(h *Handler) {
		h.name = name
	}
}

func WithQueueSize(size uint) Option {
	return func(h *Handler) {
		h.queueSize = size
	}
}

type Bus struct {
	m        sync.Mutex
	closed   bool
	handlers map[*Handler]struct{}
	events   map[EventName]map[*Handler]struct{}
}

func New() *Bus {
	return &Bus{
		handlers: make(map[*Handler]struct{}),
		events:   make(map[EventName]map[*Handler]struct{}),
	}
}

func (b *Bus) Close() {
	b.m.Lock()
	defer b.m.Unlock()
	b.checkClosed()
	b.closed = true
	for h := range b.handlers {
		close(h.ch)
	}
	for h := range b.handlers {
		<-h.done
		close(h.stop)
	}
}

func (b *Bus) Subscribe(p EventNamePattern, fn func(Event, time.Time), options ...Option) *Handler {
	b.m.Lock()
	defer b.m.Unlock()
	b.checkClosed()
	h := &Handler{
		fn:        fn,
		p:         patternToRegex(p),
		queueSize: 100,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	for _, opt := range options {
		opt(h)
	}
	h.ch = make(chan eventWithTime, h.queueSize)
	go b.processEvents(h)
	b.handlers[h] = struct{}{}
	b.subscribeAll(h)
	return h
}

func (b *Bus) Unsubscribe(h *Handler) {
	b.m.Lock()
	defer b.m.Unlock()
	close(h.ch)
	close(h.stop)
	<-h.done
	delete(b.handlers, h)
	for _, handlers := range b.events {
		delete(handlers, h)
	}
}

func (b *Bus) Publish(e Event) {
	b.m.Lock()
	defer b.m.Unlock()
	b.checkClosed()
	b.publish(e)
}

func (b *Bus) checkClosed() {
	if b.closed {
		panic("closed bus")
	}
}

func (b *Bus) subscribe(n EventName, h *Handler) {
	if !h.p.MatchString(string(n)) {
		return
	}
	m := b.events[n]
	if m == nil {
		m = make(map[*Handler]struct{})
		b.events[n] = m
	}
	m[h] = struct{}{}
}

func (b *Bus) subscribeAll(h *Handler) {
	for n := range b.events {
		b.subscribe(n, h)
	}
}

func (b *Bus) processEvents(h *Handler) {
	defer close(h.done)
	for {
		select {
		case e, ok := <-h.ch:
			if !ok {
				return
			}
			h.fn(e.e, e.t)
		case <-h.stop:
			return
		}
	}
}

func (b *Bus) publish(e Event) {
	name := e.Name()
	if _, ok := b.events[name]; !ok {
		b.events[name] = nil
		for h := range b.handlers {
			b.subscribe(name, h)
		}
	}
	now := time.Now()
	for h := range b.events[name] {
		select {
		case h.ch <- eventWithTime{now, e}:
		default:
			if _, ok := e.(Dropped); !ok {
				b.publish(Dropped{h, now, e})
			}
		}
	}
}
