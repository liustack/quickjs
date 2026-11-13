package intl

import (
	"slices"
	"testing"
	"time"

	"github.com/liustack/quickjs"
)

func TestZZWarm(t *testing.T) {
	for _, limit := range []uintptr{0, 1 << 30} {
		vm := newVM(t)
		if limit != 0 {
			vm.SetMemoryLimit(limit)
		}
		if _, err := vm.Eval(`var N = 8 << 20, s = 'hello world '.repeat(N / 12), g = new Intl.Segmenter('en', {granularity: 'word'}); 0`, quickjs.EvalGlobal); err != nil {
			t.Fatal(err)
		}
		var d []time.Duration
		for range 40 {
			t0 := time.Now()
			if _, err := vm.Eval(`g.segment(s).containing(0).segment`, quickjs.EvalGlobal); err != nil {
				t.Fatal(err)
			}
			d = append(d, time.Since(t0))
		}
		d = d[5:]
		slices.Sort(d)
		t.Logf("first word, limit %d: min %v median %v max %v", limit, d[0], d[len(d)/2], d[len(d)-1])
	}
}
