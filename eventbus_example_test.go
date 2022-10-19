package eventbus_test

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

func Example() {
	closed := make(chan struct{})
	b := eventbus.New(eventbus.WithClosedHandler(func() {
		close(closed)
	}))
	b.Subscribe("process.*", func(e eventbus.Event, t time.Time) {
		switch e := e.(type) {
		case ProcessStarted:
			fmt.Printf("Process %d started\n", e.Pid)
		case ProcessExited:
			fmt.Printf("Process %d exited with code %d\n", e.Pid, e.ExitCode)
		}
	})
	b.PublishSync(ProcessStarted{12000})
	b.PublishAsync(ProcessExited{12000, 0})
	b.Close()
	<-closed
	// Output: Process 12000 started
	// Process 12000 exited with code 0
}
