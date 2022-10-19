# Description #

The `eventbus` package provides a simple event bus for Go. It
features:
- async/sync publish
- wildcard subscription

# Usage #

``` go
package main

import (
	"fmt"
	"time"

	"github.com/montag451/go-eventbus"
)

type ProcessStarted struct {
	Pid int
}

func (ProcessStarted) Name() eventbus.EventName {
	return "process.started"
}

type ProcessExited struct {
	Pid      int
	ExitCode int
}

func (ProcessExited) Name() eventbus.EventName {
	return "process.exited"
}

func main() {
	closed := make(chan struct{})
	b := eventbus.New(eventbus.WithClosedHandler(func() {
		close(closed)
	}))
	b.Subscribe("process.*", func(e eventbus.Event, t time.Time) {
		switch e := e.(type) {
		case ProcessStarted:
			fmt.Printf("[%s] Process %d started\n", t, e.Pid)
		case ProcessExited:
			fmt.Printf("[%s] Process %d exited with code %d\n", t, e.Pid, e.ExitCode)
		}
	})
	b.PublishSync(ProcessStarted{12000})
	time.Sleep(1 * time.Second)
	b.PublishAsync(ProcessExited{12000, 0})
	b.Close()
	<-closed
}
```

# Documentation #

You can find the documentation of the package [here](https://pkg.go.dev/github.com/montag451/go-eventbus)
