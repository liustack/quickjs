// Copyright 2024 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quickjs // import "modernc.org/quickjs"

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	goruntime "runtime"
	"runtime/debug"
	"testing"
	"time"

	_ "golang.org/x/mod/modfile" // generator.go
	util "modernc.org/fileutil/ccgo"
	lib "modernc.org/libquickjs"
)

var (
	goarch   = goruntime.GOARCH
	goos     = goruntime.GOOS
	memgrind bool
	target   = fmt.Sprintf("%s/%s", goos, goarch)
)

//lint:ignore U1000 debug helper
func stack() []byte { return debug.Stack() }

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestEval(t *testing.T) {
	t.Run("eval1", testEval1)
	t.Run("eval2", testEval2)
	t.Run("eval3", testEval3)
}

func testEval1(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	for _, test := range []struct {
		js string
		v  any
	}{
		{"42*314;", 42 * 314},
		{"'foo'+'bar';", "foobar"},
		{"42 < 314;", true},
		{"null;", nil},
		{"undefined;", Undefined{}},
		{"throw new Error('FAIL')", fmt.Errorf("Error: FAIL")},
	} {
		v, err := m.Eval(test.js, EvalGlobal)
		t.Logf("%s: %T(%[1]v) %v", test.js, v, err)
		if err != nil {
			switch x := test.v.(type) {
			case error:
				if g, e := err.Error(), x.Error(); g != e {
					t.Fatalf("FAIL %s: %v %v", test.js, g, e)
				}

				continue
			default:
				t.Errorf("FAIL %s: %v", test.js, err)
				continue
			}
		}

		if g, e := v, test.v; g != e {
			t.Fatalf("FAIL %s: %T(%[1]v) %T(%[2]v)", test.js, g, e)
		}
	}
}

func testEval2(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	for _, test := range []struct {
		js string
		v  any
		sv string
	}{
		{"BigInt('1234567890123456789')", newBigInt(t, "1234567890123456789"), "1234567890123456789"},
		{"BigInt('-1234567890123456789')", newBigInt(t, "-1234567890123456789"), "-1234567890123456789"},
		{"1234567890123456789n", newBigInt(t, "1234567890123456789"), "1234567890123456789"},
		{"-1234567890123456789n", newBigInt(t, "-1234567890123456789"), "-1234567890123456789"},
	} {
		v, err := m.Eval(test.js, EvalGlobal)
		t.Logf("%s: %T(%[1]v) %v", test.js, v, err)
		if err != nil {
			t.Errorf("FAIL %s: %v", test.js, err)
			continue
		}

		if g, e := fmt.Sprintf("%T", v), fmt.Sprintf("%T", test.v); g != e {
			t.Errorf("FAIL %s: %T(%[1]v) %T(%[2]v)", test.js, g, e)
			continue
		}

		if g, e := fmt.Sprint(v), test.sv; g != e {
			t.Errorf("FAIL %v: %T(%[1]v) %T(%[2]v)", test.js, g, e)
			continue
		}
	}
}

func testEval3(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	for _, test := range []struct {
		js string
		v  string
	}{
		{`a = {}; a;`, `{}`},
		{`a = {foo: 'bar', baz: "qux", i: 42}; a;`, `{"foo":"bar","baz":"qux","i":42}`},
		{`a = []; a;`, `[]`},
		{`a = [1, 2, 3]; a;`, `[1,2,3]`},
	} {
		v, err := m.Eval(test.js, EvalGlobal)
		t.Logf("js=`%s`: v=%T(%[2]v) err=%T(%[3]v)", test.js, v, err)
		if err != nil {
			t.Errorf("FAIL js=`%s`: err=%v", test.js, err)
			continue
		}

		switch x := v.(type) {
		case *Object:
			if g, e := string(x.json), test.v; g != e {
				t.Errorf("got=`%s` expected=`%s`", g, e)
			}
		default:
			t.Errorf("unexpected result type: %T", x)
		}
	}
}

func TestCall0(t *testing.T) {
	t.Run("call1", testCall1)
	t.Run("call2", testCall2)
	t.Run("call3", testCall3)
	t.Run("call4", testCall4)
}

func testCall1(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	for _, test := range []struct {
		js string
	}{
		{`42;`},
		{`var a = 42; a;`},
		{`var a = { }; a;`},
	} {
		v, err := m.Call(test.js)
		t.Logf("js=`%s`: v=%T(%[2]v) err=%T(%[3]v)", test.js, v, err)
		if err == nil {
			t.Errorf("FAIL js=`%s`: expected non nil err", test.js)
		}
	}
}

func testCall2(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	for _, test := range []struct {
		js string
		v  any
	}{
		{`function f() { return null; }; f`, nil},
		{`function f() { return undefined; }; f`, Undefined{}},
		{`function f() { return "foo"; }; f`, "foo"},
		{`function f() { return 42; }; f`, 42},
		{`function f() { return true; }; f`, true},
		{`function f() { return 3.14; }; f`, 3.14},
		{`function f() { return 1n; }; f`, 1},
		{`function f() { return {1:2,3:4}; }; f`, `{"1":2,"3":4}`},
	} {
		v, err := m.Call(test.js)
		t.Logf("js=`%s`: v=%T(%[2]v) err=%T(%[3]v)", test.js, v, err)
		if err != nil {
			t.Errorf("FAIL js=`%s`: err=%v", test.js, err)
		}

		switch x := v.(type) {
		case nil:
			if test.v != nil {
				t.Errorf("expected nil")
			}
		case Undefined, string, int, bool, float64:
			if g, e := x, test.v; g != e {
				t.Errorf("got %v, expected %v", g, e)
			}
		case *big.Int:
			if g, e := x, big.NewInt(int64(test.v.(int))); g.Cmp(e) != 0 {
				t.Errorf("got %v, expected %v", g, e)
			}
		case *Object:
			if g, e := x.json, test.v; g != e {
				t.Errorf("got %v, expected %v", g, e)
			}
		default:
			panic(todo("%T", x))
		}
	}
}

func testCall3(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	type T struct {
		A int
		B string
	}

	for _, test := range []struct {
		js   string
		args []any
		v    any
	}{
		{`function f(a) { return a; }; f`, nil, Undefined{}},
		{`function f(a) { return a; }; f`, []any{nil}, nil},
		{`function f(a) { return a; }; f`, []any{Undefined{}}, Undefined{}},
		{`function f(a) { return a; }; f`, []any{123}, 123},
		{`function f(a) { return a; }; f`, []any{int64(1234567890123)}, 1.234567890123e+12},
		{`function f(a) { return a; }; f`, []any{true}, true},
		{`function f(a) { return a; }; f`, []any{"foo"}, "foo"},
		{`function f(a) { return a; }; f`, []any{big.NewInt(42)}, 42},
		{`function f(a, b, c, d) { return {a: a, b: b, c: c, d: d}; }; f`, []any{"aa", 11, "cc", 22}, `{"a":"aa","b":11,"c":"cc","d":22}`},
		{`function f(a) { return a; }; f`, []any{T{11, "aa"}}, `{"A":11,"B":"aa"}`},
		{`function f(a) { return a.A; }; f`, []any{T{11, "aa"}}, 11},
		{`function f(a) { return a; }; f`, []any{&T{11, "aa"}}, `{"A":11,"B":"aa"}`},
		{`var obj={"a":42, foo() { return obj.a; }}; obj.foo`, nil, 42},
	} {
		v, err := m.Call(test.js, test.args...)
		t.Logf("js=`%s`: v=%T(%[2]v) err=%T(%[3]v)", test.js, v, err)
		if err != nil {
			t.Errorf("FAIL js=`%s`: err=%v", test.js, err)
		}

		switch x := v.(type) {
		case nil:
			if test.v != nil {
				t.Errorf("expected nil")
			}
		case Undefined, string, int, bool, float64:
			if g, e := x, test.v; g != e {
				t.Errorf("got %v, expected %v", g, e)
			}
		case *big.Int:
			if g, e := x, big.NewInt(int64(test.v.(int))); g.Cmp(e) != 0 {
				t.Errorf("got %v, expected %v", g, e)
			}
		case *Object:
			if g, e := x.json, test.v; g != e {
				t.Errorf("got %v, expected %v", g, e)
			}
		default:
			panic(todo("%T", x))
		}
	}
}

func testCall4(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	v, err := m.EvalValue(`
function f(o) {
	return o.a;
}

obj = {a:42,g:function(){return this;}};
obj;
`, EvalGlobal)
	if err != nil {
		t.Fatal(err)
	}

	defer v.Free()

	w, err := m.Call("f", v)
	if err != nil {
		t.Fatal(err)
	}

	if g, e := w, 42; g != e {
		t.Fatal(g, e)
	}

	x, err := m.CallValue("obj.g", v)
	if err != nil {
		t.Fatal(err)
	}

	defer x.Free()

	if g, e := tag(x.v), lib.EJS_TAG_OBJECT; g != int32(e) {
		t.Fatal(g, e)
	}

	y, err := m.value(x.v, false)
	if err != nil {
		t.Fatal(err)
	}

	if g, e := fmt.Sprint(y), `{"obj":{"a":42}}`; g != e {
		t.Fatal(g, e)
	}
}

func newBigInt(t *testing.T, s string) *big.Int {
	n := big.NewInt(0)
	if _, ok := n.SetString(s, 10); !ok {
		t.Fatalf("big.Int.SetString(%q, 10) failed", s)
	}

	return n
}

func TestMem(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}

	switch target {
	case
		"freebsd/arm64",
		"linux/riscv64",
		"linux/s390x",
		"windows/arm64":

		t.Skip("target too slow")
	}

	if !memgrind {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)

		defer cancel()

		t.Log(time.Now().Format(time.DateTime))
		if _, err := util.Shell(ctx, "go", "test", fmt.Sprintf("-short=%v", testing.Short()), "-v", "-tags", "libc.memgrind", "-timeout", "12h", "-run", "TestMemgrind"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRegisterGoFunc(t *testing.T) {
	t.Run("fnFail", testRegisterGoFuncMustFail)
	t.Run("fnOK", testRegisterGoFuncOK)
}

func testRegisterGoFuncMustFail(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	for _, test := range []struct {
		wantThis bool
		f        any
	}{
		{false, 42},       // Not a function
		{true, func() {}}, // Wants this but no parameters
	} {
		switch err := m.RegisterFunc("myfunc", test.f, test.wantThis); {
		case err != nil:
			t.Logf("registering function '%T(%[1]v)' failed as expected: err=%v", test, err)
		default:
			t.Errorf("registering should have failed: %T", test.f)
		}
	}
}

func testRegisterGoFuncOK(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	obj, err := m.Eval("obj = {a: 463, b: 'foo'}; obj;", EvalGlobal)
	if err != nil {
		t.Fatalf("obj: %v", err)
	}

	foo, err := m.NewAtom("foo")
	if err != nil {
		t.Fatal(err)
	}

	type T struct {
		A int
		B string
	}

	for i, test := range []struct {
		nm       string
		wantThis bool
		f        any
		args     []any
		call     string
		v        any
	}{
		{"", false, func() {}, nil, "", Undefined{}},
		{"", false, func() any { return nil }, nil, "", nil},
		{"", false, func() any { return Undefined{} }, nil, "", Undefined{}},
		{"", false, func() any { return fmt.Errorf("abc") }, nil, "", "abc"},
		{"", false, func() any { return error(nil) }, nil, "", nil},
		{"", false, func() error { return fmt.Errorf("abc") }, nil, "", "abc"},
		{"", false, func() error { return nil }, nil, "", nil},
		{"", false, func() *int { return nil }, nil, "", nil},
		{"", false, func() any { i := 495; return &i }, nil, "", 495},
		{"", false, func() *int { i := 496; return &i }, nil, "", 496},
		{"", false, func() float64 { return 0.5 }, nil, "", 0.5},
		{"", false, func() float32 { return 2.5 }, nil, "", 2.5},
		{"", false, func() string { return "2.5" }, nil, "", "2.5"},
		{"", false, func() any { return big.NewInt(500) }, nil, "", 500},
		{"", false, func() *big.Int { return big.NewInt(501) }, nil, "", 501},
		{"", false, func() any { return obj }, nil, "", `{"a":463,"b":"foo"}`},
		{"", false, func() any { return []any{42, "foo"} }, nil, "", `[42,"foo"]`},
		{"", false, func() []any { return []any{42, "foo"} }, nil, "", `[42,"foo"]`},
		{"", false, func() []int { return []int{42, 314} }, nil, "", `[42,314]`},
		{"", false, func() []any { return []any{42, obj, "foo"} }, nil, "", `[42,{"a":463,"b":"foo"},"foo"]`},
		{"", false, func() (int, string) { return 42, "foo" }, nil, "", `[42,"foo"]`},
		{"", true, func(any) {}, nil, "", Undefined{}},
		{"", false, func() any { return T{511, "foo"} }, nil, "", `{"A":511,"B":"foo"}`},
		{"g1", true, func(this any) any { return this }, nil, "var a = {foo: 512, bar: g1}; a.bar();", `{"foo":512}`},
		{"g2", false, func(in any) any { return in }, nil, "g2(obj)", `{"a":463,"b":"foo"}`},
		{"g3", false, func(i int, s string) (string, int) { return s, i }, nil, "g3(514, 'foo')", `["foo",514]`},
		{"g4", false, func(in ...any) []any { return in }, nil, "g4(515, 'foo')", `[515,"foo"]`},
		{"g5", false, func(s string, args ...any) string { return fmt.Sprintf(s, args...) }, nil, "g5('hello %v %q', 516, 'foo')", `hello 516 "foo"`},
		{"g6", false, func(in ...any) []any { return in }, nil, "g6()", `[]`},
		{"g7", false, func(s string, args ...any) string { return fmt.Sprintf(s, args...) }, nil, "g7('hello %v')", `hello %!v(MISSING)`},
		{"g8", true, func(this Value) (any, error) {
			this.SetProperty(foo, 522)
			return this.Any()
		}, nil, "var a = {foo: 5220, bar: g8}; a.bar();", `[{"foo":522},null]`},
		{"g9", false, func(v Value) (any, error) {
			v.SetProperty(foo, 526)
			return v.Any()
		}, nil, "var a = {foo: 5260, bar: g9}; g9(a);", `[{"foo":526},null]`},
		{"g10", true, func(this Value) Value {
			this.SetProperty(foo, 530)
			return this.Dup()
		}, nil, "var a = {foo: 5300, bar: g10}; a.bar();", `{"foo":530}`},
		{"g11", false, func(a Value) (int, Value) {
			a.SetProperty(foo, 533)
			return 534, a.Dup()
		}, nil, "var a = {foo: 42, bar: g11}; g11(a);", `[534,{"foo":533}]`},
	} {
		nm := test.nm
		if nm == "" {
			nm = fmt.Sprintf("f%v", i)
		}
		if err := m.RegisterFunc(nm, test.f, test.wantThis); err != nil {
			t.Errorf("registering function '%T(%[1]v)': %v", test, err)
			continue
		}

		call := test.call
		if call == "" {
			call = fmt.Sprintf("%s()", nm)
		}
		rv, err := m.Eval(call, EvalGlobal)
		t.Logf("%s: %T(%[2]v)", call, rv)
		if err != nil {
			t.Errorf("calling %s: err=%v", nm, err)
			continue
		}

		switch x := rv.(type) {
		case nil:
			if test.v != nil {
				t.Errorf("expected nil")
			}
		case Undefined, string, int, bool, float64:
			if g, e := x, test.v; g != e {
				t.Errorf("got %v, expected %v", g, e)
			}
		case *big.Int:
			if g, e := x, big.NewInt(int64(test.v.(int))); g.Cmp(e) != 0 {
				t.Errorf("got %v, expected %v", g, e)
			}
		case *Object:
			if g, e := x.json, test.v; g != e {
				t.Errorf("%s: got %v, expected %v", nm, g, e)
			}
		default:
			panic(todo("%T", x))
		}
	}
}

func TestEvalValue(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	v, err := m.EvalValue("obj = {foo:42,bar:'baz'}; obj;", EvalGlobal)
	if err != nil {
		t.Fatal(err)
	}

	defer v.Free()
}

func TestAtom(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	n, err := m.NewAtom("foo")
	t.Logf("n=%v err=%v", n, err)
	if n == lib.MJS_ATOM_NULL || err != nil {
		t.Fatal("FAIL")
	}

	n2, err := m.NewAtom("bar")
	t.Logf("n2=%v err=%v", n2, err)
	if n2 == lib.MJS_ATOM_NULL || n2 == n || err != nil {
		t.Fatal("FAIL")
	}
}

func TestSetPropertyValue(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	obj, err := m.EvalValue("var obj = {}; obj;", EvalGlobal)
	if err != nil {
		t.Fatal(err)
	}

	defer obj.Free()

	atom, err := m.NewAtom("foo")
	if err != nil {
		t.Fatal(err)
	}

	n, err := m.NewString("bar")
	if err != nil {
		t.Fatal(err)
	}

	defer n.Free()

	if err := m.SetPropertyValue(obj, atom, n); err != nil {
		t.Fatal(err)
	}

	v, err := m.Eval("obj.foo;", EvalGlobal)
	if err != nil {
		t.Fatal(err)
	}

	if g, e := v, "bar"; g != e {
		t.Fatal(g, e)
	}
}

func TestSetProperty(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	obj, err := m.EvalValue("var obj = {}; obj;", EvalGlobal)
	if err != nil {
		t.Fatal(err)
	}

	defer obj.Free()

	atom, err := m.NewAtom("foo")
	if err != nil {
		t.Fatal(err)
	}

	if err := m.SetProperty(obj, atom, "bar"); err != nil {
		t.Fatal(err)
	}

	v, err := m.Eval("obj.foo;", EvalGlobal)
	if err != nil {
		t.Fatal(err)
	}

	if g, e := v, "bar"; g != e {
		t.Fatal(g, e)
	}
}

func TestGetPropertyValue(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	obj, err := m.EvalValue("var obj = {}; obj;", EvalGlobal)
	if err != nil {
		t.Fatal(err)
	}

	defer obj.Free()

	atom, err := m.NewAtom("foo")
	if err != nil {
		t.Fatal(err)
	}

	n, err := m.NewString("bar")
	if err != nil {
		t.Fatal(err)
	}

	defer n.Free()

	if err := m.SetPropertyValue(obj, atom, n); err != nil {
		t.Fatal(err)
	}

	v, err := m.GetPropertyValue(obj, atom)
	if err != nil {
		t.Fatal(err)
	}

	gv, err := m.value(v.v, true)
	if err != nil {
		t.Fatal(err)
	}

	if g, e := gv, "bar"; g != e {
		t.Fatal(g, e)
	}
}

func TestGetProperty(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	obj, err := m.EvalValue("var obj = {}; obj;", EvalGlobal)
	if err != nil {
		t.Fatal(err)
	}

	defer obj.Free()

	atom, err := m.NewAtom("foo")
	if err != nil {
		t.Fatal(err)
	}

	n, err := m.NewString("bar")
	if err != nil {
		t.Fatal(err)
	}

	defer n.Free()

	if err := m.SetPropertyValue(obj, atom, n); err != nil {
		t.Fatal(err)
	}

	v, err := m.GetProperty(obj, atom)
	if err != nil {
		t.Fatal(err)
	}

	if g, e := v, "bar"; g != e {
		t.Fatal(g, e)
	}
}

func TestBytecode(t *testing.T) {
	m, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	defer m.Close()

	bytecode, err := m.Compile("42 * 314;", EvalGlobal)
	if err != nil {
		t.Fatal(err)
	}

	// Hello future reader: this will change if you update the QuickJS version.
	// You just need to update the expected value below!
	// You can also use this chance to document that something changed.
	// See: https://gitlab.com/cznic/quickjs/-/merge_requests/3#note_2862847634
	hex := hex.EncodeToString(bytecode)
	if g, e := hex, "05000c000600a4010001000200000801a601000000bf2ac03a019ccf28a4010400001b0600"; g != e {
		t.Fatalf("got %s, expected %s", g, e)
	}

	v, err := m.EvalBytecode(bytecode)
	if err != nil {
		t.Fatal(err)
	}

	if g, e := v, 42*314; g != e {
		t.Fatal(g, e)
	}
}
