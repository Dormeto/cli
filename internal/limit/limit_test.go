package limit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResultsAreInTheItemsOrderWhateverFinishesFirst(t *testing.T) {
	items := []int{0, 1, 2, 3, 4, 5, 6, 7}
	got, err := Map(context.Background(), items, 4, func(_ context.Context, i int) (int, error) {
		time.Sleep(time.Duration(len(items)-i) * 3 * time.Millisecond)
		return i * 10, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range got {
		if v != i*10 {
			t.Fatalf("result %d is %d, got %v", i, v, got)
		}
	}
	if len(got) != len(items) {
		t.Fatalf("got %d results", len(got))
	}
}

func TestAtMostNAtOnceAndNWhenThereAreEnoughItems(t *testing.T) {
	var active, most atomic.Int64
	var mu sync.Mutex
	items := make([]int, 20)
	_, err := Map(context.Background(), items, 8, func(_ context.Context, _ int) (int, error) {
		now := active.Add(1)
		mu.Lock()
		most.Store(max(most.Load(), now))
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		active.Add(-1)
		return 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if most.Load() != 8 {
		t.Errorf("most at once: %d", most.Load())
	}
}

func TestNoItemsNoCalls(t *testing.T) {
	got, err := Map(context.Background(), []int{}, 5, func(context.Context, int) (int, error) { t.Fatal("called"); return 0, nil })
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestTheFirstErrorIsReturnedAndNoNewItemIsStarted(t *testing.T) {
	boom := errors.New("boom")
	var started atomic.Int64
	_, err := Map(context.Background(), make([]int, 50), 1, func(_ context.Context, _ int) (int, error) {
		if started.Add(1) == 3 {
			return 0, boom
		}
		return 0, nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if started.Load() != 3 {
		t.Errorf("started %d", started.Load())
	}
}

func TestACancelledContextStopsAndIsReturned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var started atomic.Int64
	_, err := Map(ctx, make([]int, 50), 1, func(_ context.Context, _ int) (int, error) {
		if started.Add(1) == 2 {
			cancel()
		}
		return 0, nil
	})
	if !errors.Is(err, context.Canceled) || started.Load() != 2 {
		t.Fatalf("%v, started %d", err, started.Load())
	}
}

func TestANonPositiveLimitStillRunsOneAtATime(t *testing.T) {
	got, err := Map(context.Background(), []int{1, 2, 3}, 0, func(_ context.Context, i int) (int, error) { return i, nil })
	if err != nil || len(got) != 3 || got[2] != 3 {
		t.Fatalf("%v %v", got, err)
	}
}

// F6: a panic in a call is that item's error, with the panic's value (an error panicked is wrapped), and fails Map as
// any error does; the process goes on.
func TestAPanicInACallIsThatItemsErrorWithItsValue(t *testing.T) {
	var started atomic.Int64
	_, err := Map(context.Background(), make([]int, 50), 1, func(_ context.Context, _ int) (int, error) {
		if started.Add(1) == 3 {
			panic("boom")
		}
		return 0, nil
	})
	var p *Panic
	if !errors.As(err, &p) || p.Value != "boom" || err.Error() != "boom" {
		t.Fatalf("err %#v", err)
	}
	if started.Load() != 3 {
		t.Errorf("started %d", started.Load())
	}
	inner := errors.New("inner")
	if _, err := Map(context.Background(), []int{1, 2, 3}, 3, func(_ context.Context, i int) (int, error) {
		if i == 2 {
			panic(inner)
		}
		return i, nil
	}); !errors.Is(err, inner) || err.Error() != "inner" {
		t.Fatalf("err %#v", err)
	}
	// A runtime error: its message.
	if _, err := Map(context.Background(), []int{1}, 1, func(_ context.Context, _ int) (int, error) {
		var m map[string]int
		m["x"] = 1
		return 0, nil
	}); err == nil || err.Error() != "assignment to entry in nil map" {
		t.Fatalf("err %v", err)
	}
}
