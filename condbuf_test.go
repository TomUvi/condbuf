package condbuf_test

import (
	"fmt"
	"time"

	"github.com/TomUvi/condbuf"
)

type Rec struct{ ID string }

func Example() {
	okIDs := map[string]bool{"000": true, "001": true}
	threshold := 15 * time.Minute

	pred := func(win []condbuf.Item[Rec], now time.Time) (bool, string) {
		if len(win) < 2 {
			return false, ""
		}
		// [0]=newest, [1]=second newest
		if !okIDs[win[0].Value.ID] || !okIDs[win[1].Value.ID] {
			return false, ""
		}
		// both have been in the buffer for longer than 15 minutes
		if now.Sub(win[0].Inserted) < threshold || now.Sub(win[1].Inserted) < threshold {
			return false, ""
		}
		return true, "last two ∈ {000,001} and both older than 15m"
	}

	onEvent := func(ev condbuf.Event[Rec]) {
		fmt.Printf("[EVENT] %s | ids=%s,%s | %s | size=%d\n",
			ev.When.Format(time.RFC3339),
			ev.Window[0].Value.ID, ev.Window[1].Value.ID,
			ev.Reason, ev.BufferSize)
	}

	buf := condbuf.New(condbuf.Config[Rec]{
		Capacity:       8,
		WindowSize:     2,
		Threshold:      threshold,
		Interval:       time.Minute, // periodic evaluation
		ImmediateCheck: true,        // also check on Push
		Predicate:      pred,
		OnEvent:        onEvent,
	})

	defer buf.Close()

	buf.Push(Rec{ID: "999"})
	buf.Push(Rec{ID: "000"})
	buf.Push(Rec{ID: "001"})

	// Output:
}
