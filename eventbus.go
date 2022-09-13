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

const defaultQueueSize = 100

type Handler struct {
	mu        sync.Mutex
	fn        func(Event, time.Time)
	p         EventNamePattern
	re        *regexp.Regexp
	name      string
	queueSize int
	drain     bool
	ch        chan eventWithTime
	stop      chan struct{}
	done      chan struct{}
}

func (h *Handler) Pattern() EventNamePattern {
	return h.p
}

func (h *Handler) Name() string {
	return h.name
}

func (h *Handler) QueueSize() int {
	return h.queueSize
}

func (h *Handler) asyncInit() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ch != nil {
		return
	}
	h.ch = make(chan eventWithTime, h.queueSize)
	h.stop = make(chan struct{})
	h.done = make(chan struct{})
	go h.processEvents()
}

func (h *Handler) asyncClose() {
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
			}
		case <-h.stop:
			return
		}
	}
}

type Option func(*Handler)

func WithName(name string) Option {
	return func(h *Handler) {
		h.name = name
	}
}

func WithQueueSize(size int) Option {
	return func(h *Handler) {
		h.queueSize = size
	}
}

func WithNoDrain() Option {
	return func(h *Handler) {
		h.drain = false
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
		h.asyncClose()
	}
}

func (b *Bus) Subscribe(p EventNamePattern, fn func(Event, time.Time), options ...Option) *Handler {
	b.m.Lock()
	defer b.m.Unlock()
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

func (b *Bus) Unsubscribe(h *Handler) {
	b.m.Lock()
	delete(b.handlers, h)
	for _, handlers := range b.events {
		delete(handlers, h)
	}
	b.m.Unlock()
	h.asyncClose()
}

func (b *Bus) PublishAsync(e Event) {
	b.m.Lock()
	defer b.m.Unlock()
	b.checkClosed()
	b.publishAsync(e)
}

func (b *Bus) PublishSync(e Event) {
	b.m.Lock()
	name := e.Name()
	b.checkNewEvent(name)
	handlers := make([]*Handler, len(b.events[name]))
	i := 0
	for h := range b.events[name] {
		handlers[i] = h
		i++
	}
	b.m.Unlock()
	t := time.Now()
	for _, h := range handlers {
		h.fn(e, t)
	}
}

func (b *Bus) checkClosed() {
	if b.closed {
		panic("closed bus")
	}
}

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

func (b *Bus) subscribeAll(h *Handler) {
	for n := range b.events {
		b.subscribe(n, h)
	}
}

func (b *Bus) checkNewEvent(name EventName) {
	if _, ok := b.events[name]; !ok {
		b.events[name] = nil
		for h := range b.handlers {
			b.subscribe(name, h)
		}
	}
}

func (b *Bus) publishAsync(e Event) {
	name := e.Name()
	b.checkNewEvent(name)
	now := time.Now()
	for h := range b.events[name] {
		h.asyncInit()
		select {
		case h.ch <- eventWithTime{now, e}:
		default:
			if _, ok := e.(Dropped); !ok {
				b.publishAsync(Dropped{h, now, e})
			}
		}
	}
}
