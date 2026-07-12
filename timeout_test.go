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
	if err := m.SetEvalTimeout(50 * time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// A quick function that still loops enough to reach an interrupt check.
	if _, err := m.Eval("globalThis.quick = function(){ var s=0; for (var i=0;i<100000;i++) s+=i; return s }", EvalGlobal); err != nil {
		t.Fatal(err)
	}

	// Idle well past the deadline, then dispatch: previously interrupted; now re-armed.
	time.Sleep(120 * time.Millisecond)
	if _, err := m.Call("quick"); err != nil {
		t.Fatalf("Call after idle was interrupted: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, err := m.ExecutePendingJobs(); err != nil {
		t.Fatalf("ExecutePendingJobs after idle errored: %v", err)
	}

	// The guard is preserved: a Call that genuinely runs past the timeout is still
	// interrupted (re-arming grants a fresh budget, it does not remove the limit).
	if _, err := m.Eval("globalThis.runaway = function(){ for (;;) {} }", EvalGlobal); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Call("runaway"); err == nil {
		t.Error("a runaway Call was not interrupted — the timeout guard is gone")
	}
}
