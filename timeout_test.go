// Copyright 2024 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quickjs // import "modernc.org/quickjs"

import (
	"testing"
	"time"
)

// TestCallRearmsEvalTimeout is the regression test for a spurious "InternalError:
// interrupted" after an idle period. The eval-timeout deadline is absolute wall-clock
// and was re-armed only by the Eval* functions, not Call/CallValue/ExecutePendingJobs.
// So once the timeout elapsed after the last Eval — even while nothing ran — the next
// Call was interrupted, and stayed interrupted. Those entry points now re-arm the
// deadline like Eval; the runaway guard still interrupts a Call that genuinely runs
// past the timeout.
func TestCallRearmsEvalTimeout(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	// quick loops enough to reach an interrupt check, so the wall-clock deadline is
	// actually consulted while it runs. Its absolute runtime is deliberately kept out
	// of the pass/fail decision below (see the widen step): on a slow emulated builder
	// the loop alone can take >100ms, which must not be confused with a stale deadline.
	if _, err := m.Eval("globalThis.quick = function(){ var s=0; for (var i=0;i<100000;i++) s+=i; return s }", EvalGlobal); err != nil {
		t.Fatal(err)
	}

	// Arm a short deadline, then idle well past it. SetEvalTimeout only records the
	// duration; the live deadline is (re-)armed by configureInterrupt inside the
	// eval-family entry points, so after this idle the live deadline is in the past
	// and the next such entry point is born already interrupted unless it re-arms.
	const short = 50 * time.Millisecond
	if err := m.SetEvalTimeout(short); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Eval("0", EvalGlobal); err != nil { // cheapest way to arm the deadline
		t.Fatal(err)
	}
	time.Sleep(short + 70*time.Millisecond)

	// Widen the budget before dispatching. Because the live deadline is untouched by
	// SetEvalTimeout, a regressed Call/ExecutePendingJobs keeps the stale, already
	// elapsed short deadline and is interrupted, while a correct one re-arms to this
	// wide budget. quick's own runtime cannot make that fail spuriously now: any
	// bounded loop finishes far inside a multi-second budget even on the slowest
	// emulated builders. (Before widening, quick racing the 50ms budget is exactly
	// what made this test fail on slow hardware, e.g. linux/s390x.)
	const wide = 10 * time.Second
	if err := m.SetEvalTimeout(wide); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Call("quick"); err != nil {
		t.Fatalf("Call after idle was interrupted: %v", err)
	}
	if _, err := m.ExecutePendingJobs(); err != nil {
		t.Fatalf("ExecutePendingJobs after idle errored: %v", err)
	}

	// The guard is preserved: re-arming grants a fresh budget, it does not remove the
	// limit. Under a short budget a genuinely runaway Call is still interrupted.
	if err := m.SetEvalTimeout(short); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Eval("globalThis.runaway = function(){ for (;;) {} }", EvalGlobal); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Call("runaway"); err == nil {
		t.Error("a runaway Call was not interrupted — the timeout guard is gone")
	}
}
