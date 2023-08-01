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

type ProcessSignaled struct {
	Pid    int
	Signal int
}

func (ProcessSignaled) Name() eventbus.EventName {
	return "process.signaled"
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
		case ProcessSignaled:
			fmt.Printf("Process %d has been signaled with signal %d\n", e.Pid, e.Signal)
		case ProcessExited:
			fmt.Printf("Process %d exited with code %d\n", e.Pid, e.ExitCode)
		}
	})
	b.Publish(ProcessStarted{12000})
	b.PublishSync(ProcessSignaled{12000, 9})
	b.PublishAsync(ProcessExited{12000, 0})
	b.Close()
	<-closed
	// Output:
	// Process 12000 started
	// Process 12000 has been signaled with signal 9
	// Process 12000 exited with code 0
}
