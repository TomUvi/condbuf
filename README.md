# condbuf

Vanilla, generic, thread-safe **ring buffer** for Go with support for
**conditionally firing events** over the last `K` items window,
after the condition has held for `Threshold` duration.

- No dependencies outside the standard library.
- O(1) `Push`; duplicate items are OK.
- Edge-triggered event: fires once for a contiguous period when `Predicate == true`.
- `ImmediateCheck`: optionally evaluates the predicate on every `Push` as well.
- Custom `Tick` can be injected for deterministic tests.

## Installation

```bash
go get github.com/TomUvi/condbuf
```

## Usage

```go
package main

import (
    "fmt"
    "time"

    "github.com/TomUvi/condbuf"
)

type Rec struct{ ID string }

func main() {
    okIDs := map[string]bool{"000": true, "001": true}
    threshold := 15 * time.Minute

    pred := func(win []condbuf.Item[Rec], now time.Time) (bool, string) {
        if len(win) < 2 { return false, "" }
        if !okIDs[win[0].Value.ID] || !okIDs[win[1].Value.ID] { return false, "" }
        if now.Sub(win[0].Inserted) < threshold || now.Sub(win[1].Inserted) < threshold { return false, "" }
        return true, "last two ∈ {000,001} and both older than 15m"
    }

    onEvent := func(ev condbuf.Event[Rec]) {
        fmt.Printf("[EVENT] %s | %s,%s | %s | size=%d\n",
            ev.When.Format(time.RFC3339), ev.Window[0].Value.ID, ev.Window[1].Value.ID, ev.Reason, ev.BufferSize)
    }

    buf := condbuf.New(condbuf.Config[Rec]{
        Capacity:       8,
        K:              2,
        Threshold:      threshold,
        Interval:       time.Minute,
        ImmediateCheck: true,
        Predicate:      pred,
        OnEvent:        onEvent,
    })
    defer buf.Close()

    buf.Push(Rec{ID: "999"})
    buf.Push(Rec{ID: "000"})
    buf.Push(Rec{ID: "001"})
}
```

## License

MIT License. See LICENSE file for details.