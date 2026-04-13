// Copyright 2024 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:generate go run generator.go

// Package quickjs is a pure Go embeddable Javascript engine. It supports the
// ECMA script 14 ([ES2023]) specification including modules, asynchronous
// generators, proxies and BigInt.
//
// See also the original [C QuickJS] library.
//
// # Performance
//
// Geomeans of time/op over a set of benchmarks, relative to CCGO, lower number
// is better. Detailed results available in the testdata/benchmarks directory.
//
//	 CCGO: modernc.org/quickjs@v0.16.3
//	 GOJA: github.com/dop251/goja@v0.0.0-20251008123653-cf18d89f3cf6
//	  QJS: github.com/fastschema/qjs@v0.0.5
//
//		                        CCGO     GOJA     QJS
//		-----------------------------------------------
//		        darwin/amd64    1.000    1.169    0.952
//		        darwin/arm64    1.000    1.106    0.928
//		       freebsd/amd64    1.000    1.271    0.866    (qemu)
//		       freebsd/arm64    1.000    1.064    0.746    (qemu)
//		           linux/386    1.000    1.738   59.275    (qemu)
//		         linux/amd64    1.000    1.942    1.014
//		           linux/arm    1.000    2.215   85.887
//		         linux/arm64    1.000    1.315    1.023
//		       linux/loong64    1.000    1.690   68.809
//		       linux/ppc64le    1.000    1.306   44.612
//		       linux/riscv64    1.000    1.370   55.163
//		         linux/s390x    1.000    1.359   45.084    (qemu)
//		       windows/amd64    1.000    1.338    1.034
//		       windows/arm64    1.000    1.516    1.205
//		-----------------------------------------------
//		                        CCGO     GOJA     QJS
//
// # Notes
//
// Parts of the documentation were copied from the quickjs documentation, see
// LICENSE-QUICKJS for details.
//
// # Supported platforms and architectures
//
// These combinations of GOOS and GOARCH are currently supported
//
//	OS      Arch
//	-------------
//	darwin  amd64
//	darwin  arm64
//	freebsd amd64
//	freebsd arm64
//	linux   386
//	linux   amd64
//	linux   arm
//	linux   arm64
//	linux   loong64
//	linux   ppc64le
//	linux   riscv64
//	linux   s390x
//	windows amd64
//	windows arm64
//
// # Builders
//
// Builder results are available [here].
//
// # Change Log
//
// 2025-10-10: Upgrade to QuickJS release 2025-09-13.
//
// [C QuickJS]: https://bellard.org/quickjs
// [ES2023]: https://tc39.es/ecma262/2023/
// [here]: https://modern-c.appspot.com/-/builder/?importpath=modernc.org%2fquickjs
//
// [modernc.org/libquickjs]: https://pkg.go.dev/modernc.org/libquickjs
package quickjs // import "modernc.org/quickjs"

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"modernc.org/libc"
	lib "modernc.org/libquickjs"
)

var (
	_ json.Marshaler = (*Object)(nil)
	_ json.Marshaler = (*Value)(nil)

	staticMagic atomic.Int32

	goFuncsMu sync.Mutex
	goFuncs   = map[int32]*goFunc{}
	freeMagic = map[int32]struct{}{}

	// UndefinedValue is a Value representing Javascript value 'undefined'. It is
	// not associated with any particular VM.
	UndefinedValue = Value{v: undefined}
)

// runtime represents a Javascript runtime.
//
// Note: runtime is not safe for concurrent use by multiple goroutines.
type runtime struct {
	cRuntime uintptr // lib.TJSRuntime
	tls      *libc.TLS
}

// newRuntime returns a newly create Runtime
func newRuntime() (*runtime, error) {
	tls := libc.NewTLS()
	r := lib.XJS_NewRuntime(tls)
	if r == 0 {
		tls.Close()
		return nil, fmt.Errorf("failed to create a new Javascript runtime")
	}

	return &runtime{
		cRuntime: r,
		tls:      tls,
	}, nil
}

// newVM returns a newly created VM.
func (r *runtime) newVM() (*VM, error) {
	tls := r.tls
	argv := libc.Xcalloc(tls, 2, libc.Tsize_t(sizeofJsValue))
	if argv == 0 {
		return nil, fmt.Errorf("OOM")
	}

	interruptData := libc.Xcalloc(tls, 1, libc.Tsize_t(unsafe.Sizeof(interruptData{})))
	if interruptData == 0 {
		libc.Xfree(tls, argv)
		return nil, fmt.Errorf("OOM")
	}

	c := lib.XJS_NewContext(tls, r.cRuntime)
	if c == 0 {
		libc.Xfree(tls, interruptData)
		return nil, fmt.Errorf("failed to create a new Javascript context")
	}

	return &VM{
		cContext:      c,
		int32_16:      lib.XNewInt32(r.tls, c, 16),
		int32_2:       lib.XNewInt32(r.tls, c, 2),
		interruptData: interruptData,
		runtime:       r,
		toStringArgv:  argv,
	}, nil
}

// close releases the resources held by r.
func (r *runtime) close() error {
	lib.XJS_FreeRuntime(r.tls, r.cRuntime)
	r.tls.Close()
	*r = runtime{}
	return nil
}

func newInt32(n int32) (r lib.TJSValue) {
	return lib.XNewInt32(nil, 0, n)
}

// NewInt returns a new Value from 'n'.
//
// Note: See the [Value] documentation for details about manual memory
// management of Value objects.
func (m *VM) NewInt(n int) Value {
	if n >= math.MinInt32 && n <= math.MaxInt32 {
		return Value{vm: m, v: newInt32(int32(n))}
	}

	return m.NewFloat64(float64(n))
}

// NewFloat64 returns a new Value from 'n'.
//
// Note: See the [Value] documentation for details about manual memory
// management of Value objects.
func (m *VM) NewFloat64(n float64) Value {
	return Value{vm: m, v: newFloat(n)}
}

// NewString returns a new Value from 's'.
//
// Note: See the [Value] documentation for details about manual memory
// management of Value objects.
func (m *VM) NewString(s string) (r Value, err error) {
	v, err := m.newString(s)
	if err != nil {
		return r, err
	}

	return Value{vm: m, v: v}, nil
}

// NewObjectValue returns a new Value representing '{}'.
func (m *VM) NewObjectValue() (r Value, err error) {
	return Value{vm: m, v: lib.XJS_NewObject(m.runtime.tls, m.cContext)}, nil
}

func (m *VM) newString(s string) (r lib.TJSValue, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	p, err := libc.CString(s)
	if err != nil {
		return r, err
	}

	defer libc.Xfree(tls, p)

	return lib.XJS_NewStringLen(tls, ctx, p, lib.Tsize_t(len(s))), nil
}

func jsInt(n int64) lib.TJSValue {
	switch {
	case n >= math.MinInt32 && n <= math.MaxInt32:
		return newInt32(int32(n))
	default:
		return newFloat(float64(n))
	}
}

func jsUint(n uint64) lib.TJSValue {
	switch {
	case n <= math.MaxInt32:
		return newInt32(int32(n))
	default:
		return newFloat(float64(n))
	}
}

var (
	jsonFNC = [...]byte{'J', 'S', 'O', 'N', 0}
	jsonFN  = uintptr(unsafe.Pointer(&jsonFNC[0]))
)

func (m *VM) parseJSON(s string, flags int32) (r lib.TJSValue, err error) {
	tls := m.runtime.tls
	ctx := m.cContext

	cs, err := libc.CString(s)
	if err != nil {
		return r, err
	}

	defer libc.Xfree(tls, cs)

	r = lib.XJS_ParseJSON2(tls, ctx, cs, lib.Tsize_t(len(s)), jsonFN, flags)
	if isException(r) {
		err = m.errFromException()
	}
	return r, err
}

// Unsupported represents an unsupported Javascript value.
type Unsupported struct{}

// String implements fmt.Stringer.
func (u Unsupported) String() string {
	return "unsupported Javascript type"
}

// Undefined represents the Javascript value "undefined".
type Undefined struct{}

// String implements fmt.Stringer.
func (u Undefined) String() string {
	return "undefined"
}

// Object represents the value of a Javascript object, but not the javascript
// object instance itself. Do not compare instances of Object.
type Object struct {
	json string
}

// String implements fmt.Stringer.
func (o *Object) String() string {
	return o.json
}

// MarshalJSON implements encoding/json.Marshaler.
func (o *Object) MarshalJSON() (r []byte, err error) {
	return []byte(o.json), nil
}

// Into helps you quickly deserialize an Object into a native Go type.
// This uses encoding/json.Unmarshal internally, so the target type must be
// compatible with that package.
func (o *Object) Into(v any) error {
	return json.Unmarshal([]byte(o.json), v)
}

// return != 0 if the JS code needs to be interrupted
func interruptHandler(tls *libc.TLS, rt, opaque uintptr) int32 {
	if opaque == 0 {
		return 0
	}

	if atomic.LoadInt32(&(*interruptData)(unsafe.Pointer(opaque)).interrupt) != 0 {
		return 1
	}

	if timeLimit := (*interruptData)(unsafe.Pointer(opaque)).timeLimit; timeLimit != 0 && time.Now().UnixNano() >= timeLimit {
		return 1
	}

	return 0
}

type interruptData struct {
	interrupt int32 // Interrupt() pending
	timeLimit int64 // stop when time.Now().UnixNano() > timeLimit
}

// VM represents a Javascript context (or Realm). Each VM has its
// own global objects and system objects.
//
// Note: VM is not safe for concurrent use by multiple goroutines.
type VM struct {
	cContext uintptr // lib.TJSContext
	goFuncs  map[string]int32
	// Safe to share, not reference counted
	int32_16      lib.TJSValue
	int32_2       lib.TJSValue
	runtime       *runtime
	timeout       time.Duration
	interruptData uintptr
	toStringArgv  uintptr
}

// NewVM returns a newly created VM.
func NewVM() (*VM, error) {
	rt, err := newRuntime()
	if err != nil {
		return nil, err
	}

	vm, err := rt.newVM()
	if err != nil {
		rt.close()
		return nil, err
	}

	lib.XJS_SetInterruptHandler(rt.tls, rt.cRuntime, fp(interruptHandler), vm.interruptData)
	return vm, nil
}

// SetEvalTimeout sets the timeout for the various eval functions. Passing
// zero 'd' disables timeouts.
func (m *VM) SetEvalTimeout(d time.Duration) error {
	m.timeout = d
	return nil
}

// Interrupt requests 'm' to interrupt Javascript evaluation. Interrupt
// can be called asynchronously at any time m is evaluating a script.
func (m *VM) Interrupt() {
	atomic.StoreInt32(&(*interruptData)(unsafe.Pointer(m.interruptData)).interrupt, 1)
}

func (m *VM) configureInterrupt() {
	if m.interruptData != 0 {
		(*interruptData)(unsafe.Pointer(m.interruptData)).interrupt = 0
		if m.timeout != 0 {
			(*interruptData)(unsafe.Pointer(m.interruptData)).timeLimit = time.Now().Add(m.timeout).UnixNano()
		} else {
			(*interruptData)(unsafe.Pointer(m.interruptData)).timeLimit = 0
		}
	}
}

// SetMemoryLimit limits m's maxmimum memory usage, in bytes. Small values of
// 'limit' are not honored because the VM needs to allocate memory also for the
// exception object itself.  The particular value of "small" is unspecified and
// subject to change without notice.
func (m *VM) SetMemoryLimit(limit uintptr) {
	lib.XJS_SetMemoryLimit(m.runtime.tls, m.runtime.cRuntime, max(libc.Tsize_t(limit), 128*1024))
}

// SetGCThreshold sets the memory threshold at which a GC will be performed, in bytes.
func (m *VM) SetGCThreshold(threshold uintptr) {
	lib.XJS_SetGCThreshold(m.runtime.tls, m.runtime.cRuntime, libc.Tsize_t(threshold))
}

//TODO(jnml) needs changes in libquicjs stack slot checking.
// SetMaxStackSize limits m's maxmimum stack depth, in stack frames.
// func (m *VM) SetMaxStackSize(threshold uintptr) {
// 	lib.XJS_SetMaxStackSize(m.runtime.tls, m.runtime.cRuntime, libc.Tsize_t(threshold))
// }

// SetCanBlock configures m's blocking mode.
func (m *VM) SetCanBlock(value bool) {
	n := int32(0)
	if value {
		n = 1
	}
	lib.XJS_SetCanBlock(m.runtime.tls, m.runtime.cRuntime, n)
}

// Close releases the resources held by 'm'.
func (m *VM) Close() error {
	tls := m.runtime.tls
	ctx := m.cContext
	var a []int32
	var b []*goFunc
	for _, k := range m.goFuncs {
		a = append(a, k)
	}
	goFuncsMu.Lock()
	for _, k := range a {
		b = append(b, goFuncs[k])
		freeMagic[k] = struct{}{}
		delete(goFuncs, k)
	}
	goFuncsMu.Unlock()

	for _, v := range b {
		libc.Xfree(tls, v.cName)
		libc.Xfree(tls, v.tab)
	}

	libc.Xfree(tls, m.interruptData)
	libc.Xfree(tls, m.toStringArgv)
	lib.XJS_FreeContext(tls, ctx)
	r := m.runtime
	*m = VM{}
	return r.close()
}

// Eval flags.
const (
	EvalGlobal = lib.MJS_EVAL_TYPE_GLOBAL // global code
	EvalModule = lib.MJS_EVAL_TYPE_MODULE // module code
)

var evalFN = [...]byte{'<', 'e', 'v', 'a', 'l', '>', 0}

// Eval evaluates a script or module source in 'javascript'.
//
//	Javascript result type  Go result type                          Go result error
//	-------------------------------------------------------------------------------
//	exception               nil                                     non-nil
//	null                    nil                                     nil
//	undefined               Undefined                               nil
//	string                  string                                  nil
//	int                     int                                     nil
//	bool                    bool                                    nil
//	float64                 float64                                 nil
//	BigInt                  *math/big.Int                           nil
//	object                  *Object                                 nil
//	any other type          Unsupported                             nil
func (m *VM) Eval(javascript string, flags int) (r any, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	ps, err := libc.CString(javascript)
	if err != nil {
		return nil, fmt.Errorf("OOM")
	}

	defer libc.Xfree(tls, ps)

	m.configureInterrupt()
	return m.value(lib.XJS_Eval(tls, ctx, ps, libc.Tsize_t(len(javascript)), uintptr(unsafe.Pointer(&evalFN)), int32(flags)), true)
}

// EvalThis is like eval but sets javascript 'this' to 'obj'.
func (m *VM) EvalThis(obj Value, javascript string, flags int) (r any, err error) {
	defer obj.Free()

	tls := m.runtime.tls
	ctx := m.cContext
	ps, err := libc.CString(javascript)
	if err != nil {
		return nil, fmt.Errorf("OOM")
	}

	defer libc.Xfree(tls, ps)

	m.configureInterrupt()
	return m.value(lib.XJS_EvalThis(tls, ctx, obj.v, ps, libc.Tsize_t(len(javascript)), uintptr(unsafe.Pointer(&evalFN)), int32(flags)), true)
}

// EvalValue evaluates a script or module source in 'javascript' and returns
// the resulting Value, or an error, if any.
//
// Exceptions thrown during evaluation of the script are returned as Go errors.
//
// If no error is returned, the caller must properly handle the returned Value
// using Dup/Free.
//
// Note: See the [Value] documentation for details about manual memory
// management of Value objects.
func (m *VM) EvalValue(javascript string, flags int) (r Value, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	ps, err := libc.CString(javascript)
	if err != nil {
		return r, fmt.Errorf("OOM")
	}

	defer libc.Xfree(tls, ps)

	m.configureInterrupt()
	v := lib.XJS_Eval(tls, ctx, ps, libc.Tsize_t(len(javascript)), uintptr(unsafe.Pointer(&evalFN)), int32(flags))
	if isException(v) {
		lib.XFreeValue(tls, ctx, v)
		return r, m.errFromException()
	}

	return Value{vm: m, v: v}, nil
}

// EvalThisValue is like EvalValue but sets javascript 'this' to obj.
func (m *VM) EvalThisValue(obj Value, javascript string, flags int) (r Value, err error) {
	defer obj.Free()

	tls := m.runtime.tls
	ctx := m.cContext
	ps, err := libc.CString(javascript)
	if err != nil {
		return r, fmt.Errorf("OOM")
	}

	defer libc.Xfree(tls, ps)

	m.configureInterrupt()
	v := lib.XJS_EvalThis(tls, ctx, obj.v, ps, libc.Tsize_t(len(javascript)), uintptr(unsafe.Pointer(&evalFN)), int32(flags))
	if isException(v) {
		lib.XFreeValue(tls, ctx, v)
		return r, m.errFromException()
	}

	return Value{vm: m, v: v}, nil
}

// EvalBytecode is like Eval but uses QuickJS bytecode.
//
// Note: The bytecode format is linked to a given QuickJS version. Moreover, no security check is done before
// its execution. Hence the bytecode should not be loaded from untrusted sources.
//
// Note: Javascript 'this' is always set to the global context.
func (m *VM) EvalBytecode(bytecode []byte) (r any, err error) {
	v, err := m.EvalBytecodeValue(bytecode)
	if err != nil {
		return nil, err
	}

	return m.value(v.v, true)
}

// EvalBytecodeValue is like EvalValue but operates on QuickJS bytecode.
//
// Exceptions thrown during evaluation of the script are returned as Go errors.
//
// If no error is returned, the caller must properly handle the returned Value
// using Dup/Free.
//
// Note: See the [Value] documentation for details about manual memory
// management of Value objects.
func (m *VM) EvalBytecodeValue(bytecode []byte) (r Value, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	ps, err := libc.CString(string(bytecode))
	if err != nil {
		return r, fmt.Errorf("OOM")
	}

	defer libc.Xfree(tls, ps)

	// https://github.com/bellard/quickjs/blob/eb2c89087def1829ed99630cb14b549d7a98408c/quickjs-libc.c#L4292
	obj := lib.XJS_ReadObject(tls, ctx, ps, lib.Tsize_t(len(bytecode)), lib.MJS_READ_OBJ_BYTECODE)
	if isException(obj) {
		return r, m.errFromException()
	}

	m.configureInterrupt()

	var v lib.TJSValue
	if tag(obj) == lib.EJS_TAG_MODULE {
		if lib.XJS_ResolveModule(tls, ctx, obj) < 0 {
			lib.XFreeValue(tls, ctx, obj)
			return r, m.errFromException()
		}

		lib.Xjs_module_set_import_meta(tls, ctx, obj, 0, 1)
		// 'this' is set to ctx->global_obj
		// see https://github.com/bellard/quickjs/blob/eb2c89087def1829ed99630cb14b549d7a98408c/quickjs.c#L36045
		v = lib.XJS_EvalFunction(tls, ctx, obj)
		v = lib.Xjs_std_await(tls, ctx, v)
	} else {
		v = lib.XJS_EvalFunction(tls, ctx, obj)
	}

	if isException(v) {
		lib.XFreeValue(tls, ctx, v)
		return r, m.errFromException()
	}

	return Value{vm: m, v: v}, nil
}

// Compile compiles the bytecode representation of the passed script.
//
// The returned bytecode can be later executed using EvalBytecode or EvalBytecodeValue.
//
// Note: The bytecode format is linked to a given QuickJS version.
func (m *VM) Compile(javascript string, flags int) ([]byte, error) {
	tls := m.runtime.tls
	ctx := m.cContext
	// https://github.com/bellard/quickjs/blob/eb2c89087def1829ed99630cb14b549d7a98408c/qjsc.c#L346-L360

	flags |= lib.MJS_EVAL_FLAG_COMPILE_ONLY

	v, err := m.eval(javascript, flags)
	if err != nil {
		return nil, err
	}

	defer lib.XFreeValue(tls, ctx, v)

	if isException(v) {
		return nil, m.errFromException()
	}

	// https://github.com/bellard/quickjs/blob/eb2c89087def1829ed99630cb14b549d7a98408c/qjsc.c#L193
	flags2 := int32(lib.MJS_WRITE_OBJ_BYTECODE)
	usize := int(unsafe.Sizeof(libc.Tsize_t(0)))
	plen := tls.Alloc(usize)
	defer tls.Free(usize)

	p := lib.XJS_WriteObject(tls, ctx, plen, v, flags2)
	if p == 0 {
		return nil, fmt.Errorf("OOM")
	}

	plen2 := int(*(*libc.Tsize_t)(unsafe.Pointer(plen)))
	b := libc.GoBytes(p, plen2)
	bcopy := make([]byte, len(b))
	copy(bcopy, b)

	lib.Xjs_free(tls, ctx, p)
	return bcopy, nil
}

func (m *VM) eval(js string, flags int) (r lib.TJSValue, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	ps, err := libc.CString(js)
	if err != nil {
		return r, fmt.Errorf("OOM")
	}

	defer libc.Xfree(tls, ps)

	m.configureInterrupt()
	return lib.XJS_Eval(tls, ctx, ps, libc.Tsize_t(len(js)), uintptr(unsafe.Pointer(&evalFN)), int32(flags)), nil
}

// GlobalObject returns m's global object.
//
// Note: See the [Value] documentation for details about manual memory
// management of Value objects.
func (m *VM) GlobalObject() (r Value) {
	return Value{vm: m, v: m.globalObject()}
}

func (m *VM) globalObject() lib.TJSValue {
	return lib.XJS_GetGlobalObject(m.runtime.tls, m.cContext)
}

// Call evaluates 'function(args...)' and returns the resulting (value, error).
//
// Argument types must be one of:
//
//	Go argument type                        Javascript argument type
//	----------------------------------------------------------------
//	nil                                     null
//	Undefined                               undefined
//	string                                  string
//	int*/uint* (value in int32 range)       int
//	int*/uint* (value out of int32 range)   float
//	bool                                    bool
//	float64                                 float64
//	*math/big.Int                           BigInt
//	*Object                                 object
//	Value                                   native Javascript Value
//	any other type                          object from JSON produced by encoding.json/Marshall(arg)
func (m *VM) Call(function string, args ...any) (r any, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	ps, err := libc.CString(function)
	if err != nil {
		return nil, fmt.Errorf("OOM")
	}

	defer libc.Xfree(tls, ps)

	f := lib.XJS_Eval(tls, ctx, ps, libc.Tsize_t(len(function)), uintptr(unsafe.Pointer(&evalFN)), int32(EvalGlobal))

	defer lib.XFreeValue(tls, ctx, f)

	g := m.globalObject()

	defer lib.XFreeValue(tls, ctx, g)

	return m.call(f, g, args...)
}

// CallValue is like Call but returns (Value, error) like EvalValue
//
// Note: See the [Value] documentation for details about manual memory
// management of Value objects.
func (m *VM) CallValue(function string, args ...any) (r Value, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	ps, err := libc.CString(function)
	if err != nil {
		return r, fmt.Errorf("OOM")
	}

	defer libc.Xfree(tls, ps)

	f := lib.XJS_Eval(tls, ctx, ps, libc.Tsize_t(len(function)), uintptr(unsafe.Pointer(&evalFN)), int32(EvalGlobal))

	defer lib.XFreeValue(tls, ctx, f)

	g := m.globalObject()

	defer lib.XFreeValue(tls, ctx, g)

	return m.callValue(f, g, args...)
}

func (m *VM) convertArgs(goArgs ...any) (jsArgs, free []lib.TJSValue, err error) {
	tls := m.runtime.tls
	ctx := m.cContext

	defer func() {
		if err != nil && len(free) != 0 {
			for _, v := range free {
				lib.XFreeValue(tls, ctx, v)
			}
			free = nil
		}
	}()

	for _, v := range goArgs {
		switch x := v.(type) {
		case nil:
			jsArgs = append(jsArgs, null)
		case Value:
			if x.vm != m && tag(x.v) < 0 {
				return nil, free, fmt.Errorf("cannot use a Value from a different VM")
			}

			dup := x.Dup().v
			free = append(free, dup)
			jsArgs = append(jsArgs, dup)
		case Undefined:
			jsArgs = append(jsArgs, undefined)
		case int8:
			jsArgs = append(jsArgs, newInt32(int32(x)))
		case uint8:
			jsArgs = append(jsArgs, newInt32(int32(x)))
		case int16:
			jsArgs = append(jsArgs, newInt32(int32(x)))
		case uint16:
			jsArgs = append(jsArgs, newInt32(int32(x)))
		case int32:
			jsArgs = append(jsArgs, newInt32(int32(x)))
		case uint32:
			switch {
			case x <= math.MaxInt32:
				jsArgs = append(jsArgs, newInt32(int32(x)))
			default:
				jsArgs = append(jsArgs, newFloat(float64(x)))
			}
		case int:
			switch {
			case x >= math.MinInt32 && x <= math.MaxInt32:
				jsArgs = append(jsArgs, newInt32(int32(x)))
			default:
				jsArgs = append(jsArgs, newFloat(float64(x)))
			}
		case uint:
			switch {
			case x <= math.MaxInt32:
				jsArgs = append(jsArgs, newInt32(int32(x)))
			default:
				jsArgs = append(jsArgs, newFloat(float64(x)))
			}
		case int64:
			switch {
			case x >= math.MinInt32 && x <= math.MaxInt32:
				jsArgs = append(jsArgs, newInt32(int32(x)))
			default:
				jsArgs = append(jsArgs, newFloat(float64(x)))
			}
		case uint64:
			switch {
			case x <= math.MaxInt32:
				jsArgs = append(jsArgs, newInt32(int32(x)))
			default:
				jsArgs = append(jsArgs, newFloat(float64(x)))
			}
		case bool:
			jsArgs = append(jsArgs, newBool(x))
		case string:
			p, err := libc.CString(x)
			if err != nil {
				return nil, free, err
			}

			defer libc.Xfree(tls, p)

			jv := lib.XJS_NewStringLen(tls, ctx, p, libc.Tsize_t(len(x)))
			free = append(free, jv)
			jsArgs = append(jsArgs, jv)
		case *big.Int:
			s := x.String() + "n"
			jv, err := m.eval(s, EvalGlobal)
			if err != nil {
				return nil, free, err
			}

			free = append(free, jv)
			jsArgs = append(jsArgs, jv)
		default:
			b, err := json.Marshal(x)
			if err != nil {
				return nil, free, err
			}

			p, err := libc.CString(string(b))
			if err != nil {
				return nil, free, err
			}

			defer libc.Xfree(tls, p)

			jv := lib.XJS_ParseJSON(tls, ctx, p, libc.Tsize_t(len(b)), uintptr(unsafe.Pointer(&evalFN)))
			free = append(free, jv)
			jsArgs = append(jsArgs, jv)
		}
	}
	return jsArgs, free, nil
}

// Note: See the [Value] documentation for details about manual memory
// management of Value objects.
func (m *VM) callValue(f, this lib.TJSValue, args ...any) (r Value, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	if lib.XJS_IsFunction(tls, ctx, f) == 0 {
		return r, fmt.Errorf("cannot call a non-function")
	}

	jsArgs, free, err := m.convertArgs(args...)
	if err != nil {
		return r, err
	}

	if len(free) != 0 {
		defer func() {
			for _, v := range free {
				lib.XFreeValue(tls, ctx, v)
			}
		}()
	}
	var argv uintptr
	if len(jsArgs) != 0 {
		sz := libc.Tsize_t(sizeofJsValue * uintptr(len(jsArgs)))
		argv = libc.Xmalloc(tls, sz)

		defer libc.Xfree(tls, argv)

		for i, v := range jsArgs {
			unsafe.Slice((*lib.TJSValue)(unsafe.Pointer(argv)), len(jsArgs))[i] = v
		}

	}
	v := lib.XJS_Call(tls, ctx, f, this, int32(len(jsArgs)), argv)
	if isException(v) {
		lib.XFreeValue(tls, ctx, v)
		return r, m.errFromException()
	}

	return Value{vm: m, v: v}, nil
}

func (m *VM) call(f, this lib.TJSValue, args ...any) (r any, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	if lib.XJS_IsFunction(tls, ctx, f) == 0 {
		return nil, fmt.Errorf("cannot call a non-function")
	}

	jsArgs, free, err := m.convertArgs(args...)
	if err != nil {
		return nil, err
	}

	if len(free) != 0 {
		defer func() {
			for _, v := range free {
				lib.XFreeValue(tls, ctx, v)
			}
		}()
	}
	var argv uintptr
	if len(jsArgs) != 0 {
		sz := libc.Tsize_t(sizeofJsValue * uintptr(len(jsArgs)))
		argv = libc.Xmalloc(tls, sz)

		defer libc.Xfree(tls, argv)

		for i, v := range jsArgs {
			unsafe.Slice((*lib.TJSValue)(unsafe.Pointer(argv)), len(jsArgs))[i] = v
		}

	}
	return m.value(lib.XJS_Call(tls, ctx, f, this, int32(len(jsArgs)), argv), true)
}

func (m *VM) newObject(v lib.TJSValue) (r *Object, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	json := lib.XJS_JSONStringify(tls, ctx, v, undefined, undefined)

	defer lib.XFreeValue(tls, ctx, json)

	if isException(json) {
		return nil, m.errFromException()
	}

	p := lib.XToCString(tls, ctx, json)

	defer lib.XJS_FreeCString(tls, ctx, p)

	return &Object{json: libc.GoString(p)}, nil
}

var (
	toStringC = [...]byte{'t', 'o', 'S', 't', 'r', 'i', 'n', 'g', 0}
	toString  = uintptr(unsafe.Pointer(&toStringC[0]))
)

// value "unpacks" 'v'. FreeValue(v) is called before returning, 'v' must not
// be used afterwards.
func (m *VM) value(v lib.TJSValue, free bool) (r any, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	if free && tag(v) < 0 {
		// all tags with a reference count are negative
		defer lib.XFreeValue(tls, ctx, v)
	}

	switch tag(v) {
	case
		lib.EJS_TAG_BIG_INT,       // -9,
		lib.EJS_TAG_SHORT_BIG_INT: // 7,

		ps := lib.XJS_GetPropertyStr(tls, ctx, v, toString)
		if tag(ps) != lib.EJS_TAG_OBJECT {
			return nil, fmt.Errorf("failed to get BigInt.toString()")
		}

		defer lib.XFreeValue(tls, ctx, ps)

		*(*lib.TJSValue)(unsafe.Pointer(m.toStringArgv)) = m.int32_16
		jsString := lib.XJS_Call(tls, ctx, ps, v, 1, m.toStringArgv)

		defer lib.XFreeValue(tls, ctx, jsString)

		p := lib.XToCString(tls, ctx, jsString)

		defer lib.XJS_FreeCString(tls, ctx, p)

		n := big.NewInt(0)
		if _, ok := n.SetString(libc.GoString(p), 16); !ok {
			return nil, fmt.Errorf("big.Int.SetString failed")
		}

		return n, nil
	case
		lib.EJS_TAG_STRING_ROPE, // -6,
		lib.EJS_TAG_STRING:      // -7,

		p := lib.XToCString(tls, ctx, v)

		defer lib.XJS_FreeCString(tls, ctx, p)

		return libc.GoString(p), nil
	case lib.EJS_TAG_OBJECT: // -1
		return m.newObject(v)
	case lib.EJS_TAG_INT: //  0,
		return int(*(*int32)(unsafe.Pointer(&v))), nil
	case lib.EJS_TAG_BOOL: //  1,
		return *(*int32)(unsafe.Pointer(&v)) != 0, nil
	case lib.EJS_TAG_NULL: //  2,
		return nil, nil
	case lib.EJS_TAG_UNDEFINED: //  3,
		return Undefined{}, nil
	case lib.EJS_TAG_EXCEPTION: // 6,
		return nil, m.errFromException()
		panic(todo(""))
	case lib.EJS_TAG_FLOAT64: // 8,
		return jsvToFloat64(v), nil
	}
	return Unsupported{}, nil
}

func (m *VM) errFromException() error {
	tls := m.runtime.tls
	ctx := m.cContext
	e := lib.XJS_GetException(tls, ctx)

	defer lib.XFreeValue(tls, ctx, e)

	p := lib.XToCString(tls, ctx, e)

	defer lib.XJS_FreeCString(tls, ctx, p)

	return fmt.Errorf("%s", libc.GoString(p))
}

// StdAddHelpers adds the 'print' and 'console' global objects to 'm'.
func (m *VM) StdAddHelpers() error {
	tls := m.runtime.tls
	ctx := m.cContext
	lib.Xjs_std_add_helpers(tls, ctx, -1, 0)
	return nil
}

func throwTypeError(tls *libc.TLS, ctx uintptr, msg string, args ...any) (r lib.TJSValue) {
	p, err := libc.CString(msg)
	if err != nil {
		return lib.XJS_ThrowTypeError(tls, ctx, 0, 0)
	}

	defer libc.Xfree(tls, p)

	bp := tls.Alloc(8 + 8*len(args))

	defer tls.Free(8 + 8*len(args))

	return lib.XJS_ThrowTypeError(tls, ctx, p, libc.VaList(bp+8, args...))
}

func throwInternalError(tls *libc.TLS, ctx uintptr, msg string, args ...any) (r lib.TJSValue) {
	p, err := libc.CString(msg)
	if err != nil {
		return lib.XJS_ThrowInternalError(tls, ctx, 0, 0)
	}

	defer libc.Xfree(tls, p)

	bp := tls.Alloc(8 + 8*len(args))

	defer tls.Free(8 + 8*len(args))

	return lib.XJS_ThrowInternalError(tls, ctx, p, libc.VaList(bp+8, args...))
}

type goFunc struct {
	cName        uintptr
	f            reflect.Value
	in           []reflect.Type
	name         string
	out          []reflect.Type
	tab          uintptr
	variadicType reflect.Type
	vm           *VM

	minArgs int
	maxArgs int

	wantThis bool
}

// RegisterFunc registers a Go function 'f' and makes it callable from
// Javascript.
//
// The 'f' argument can be a regular Go function, a closure, a method
// expression or a method value. All of them are called 'Go function' below.
//
// The Go function can have zero or more parameters. If 'wantThis' is true then
// the first parameter of the Go function will get the Javascript value of
// 'this'.  Depending on context, 'this' can be Javascript null or undefined.
//
// Go functions with multiple results return them as an Javascript array.
//
// Go nil errors are converted to Javascript null.
//
// Go non-nil errors are converted to Javascript strings using the Error()
// method.
//
// Any Go <-> Javascript failing type conversion between arguments/return
// values throws a Javascript type error exception.
//
// There is a limit on the total number of currently registered Go
// functions.
//
// Note: The 'name' argument should be a valid Javascript identifier. It is not
// currently enforced but this may change later.
func (m *VM) RegisterFunc(name string, f any, wantThis bool) (err error) {
	if name == "" {
		return fmt.Errorf("func name cannot be empty")
	}

	if _, ok := m.goFuncs[name]; ok {
		return fmt.Errorf("func already registered: %s", name)
	}

	tls := m.runtime.tls
	ctx := m.cContext
	val := reflect.ValueOf(f)
	typ := reflect.TypeOf(f)
	if typ.Kind() != reflect.Func {
		return fmt.Errorf("%s is not a function", name)
	}

	if wantThis && typ.NumIn() == 0 {
		return fmt.Errorf("'wantThis' is true but 'f' has zero parameters")
	}

	minArgs := typ.NumIn()
	if wantThis {
		minArgs--
	}
	maxArgs := minArgs
	var vt reflect.Type
	if typ.IsVariadic() {
		maxArgs = -1
		sliceT := typ.In(typ.NumIn() - 1)
		vt = sliceT.Elem()
		minArgs--
	}
	info := &goFunc{
		f:            val,
		maxArgs:      maxArgs,
		minArgs:      minArgs,
		name:         name,
		variadicType: vt,
		vm:           m,
		wantThis:     wantThis,
	}
	for i := 0; i < typ.NumIn(); i++ {
		info.in = append(info.in, typ.In(i))
	}
	if maxArgs < 0 {
		info.in = info.in[:len(info.in)-1]
	}
	for i := 0; i < typ.NumOut(); i++ {
		info.out = append(info.out, typ.Out(i))
	}

	magic := int32(-1)
	goFuncsMu.Lock()
	for k := range freeMagic {
		magic = k
		delete(freeMagic, k)
		break
	}
	if magic < 0 {
		magic = staticMagic.Add(1)
		if magic > math.MaxInt16 {
			staticMagic.Add(-1)
			goFuncsMu.Unlock()
			return fmt.Errorf("too many registered functions")
		}

	}
	goFuncs[magic] = info
	goFuncsMu.Unlock()
	if m.goFuncs == nil {
		m.goFuncs = map[string]int32{}
	}
	m.goFuncs[name] = magic

	cName, err := libc.CString(name)
	if err != nil {
		return err
	}

	info.cName = cName
	argc := int32(typ.NumIn()) - 1
	if argc < 0 {
		argc = 0
	}
	tab := libc.Xcalloc(tls, 1, libc.Tsize_t(unsafe.Sizeof(lib.TJSCFunctionListEntry{})))
	info.tab = tab

	g := m.globalObject()

	defer lib.XFreeValue(tls, ctx, g)

	(*lib.TJSCFunctionListEntry)(unsafe.Pointer(tab)).Fname = cName
	(*lib.TJSCFunctionListEntry)(unsafe.Pointer(tab)).Fprop_flags = lib.MJS_PROP_WRITABLE | lib.MJS_PROP_CONFIGURABLE
	(*lib.TJSCFunctionListEntry)(unsafe.Pointer(tab)).Fdef_type = lib.MJS_DEF_CFUNC
	(*lib.TJSCFunctionListEntry)(unsafe.Pointer(tab)).Fmagic = int16(magic)
	(*lib.TJSCFunctionListEntry)(unsafe.Pointer(tab)).Fu.Ffunc1.Flength = uint8(argc)
	(*lib.TJSCFunctionListEntry)(unsafe.Pointer(tab)).Fu.Ffunc1.Fcproto = lib.EJS_CFUNC_generic_magic
	(*lib.TJSCFunctionListEntry)(unsafe.Pointer(tab)).Fu.Ffunc1.Fcfunc.Fgeneric = fp(callGo)
	lib.XJS_SetPropertyFunctionList(tls, ctx, g, tab, 1)
	return nil
}

var rvt = reflect.TypeOf(Value{})

func isNative(t reflect.Type) bool {
	return t == rvt
}

func fp(f interface{}) uintptr {
	type iface [2]uintptr
	return (*iface)(unsafe.Pointer(&f))[1]
}

// typedef JSValue JSCFunctionMagic(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int magic);
func callGo(tls *libc.TLS, ctx uintptr, this lib.TJSValue, argc int32, argv uintptr, magic int32) (r lib.TJSValue) {
	goFuncsMu.Lock()
	info := goFuncs[magic]
	goFuncsMu.Unlock()

	if info == nil {
		return throwInternalError(tls, ctx, "callback id=%i not registered", magic)
	}

	m := info.vm
	haveArgs := int(argc)
	if haveArgs < info.minArgs {
		trc("wantThis=%v haveArgs=%v minArgs=%v maxArgs=%v argc=%v", info.wantThis, haveArgs, info.minArgs, info.maxArgs, argc)
		return throwTypeError(tls, ctx, fmt.Sprintf("not enough arguments in call to %s", info.name))
	}

	if info.maxArgs >= 0 && haveArgs > info.maxArgs {
		// trc("wantThis=%v haveArgs=%v minArgs=%v maxArgs=%v argc=%v", info.wantThis, haveArgs, info.minArgs, info.maxArgs, argc)
		return throwTypeError(tls, ctx, fmt.Sprintf("too many arguments in call to %s", info.name))
	}

	var in []reflect.Value
	i := 0
	if info.wantThis {
		switch {
		case isNative(info.in[0]):
			dup := lib.XDupValue(tls, ctx, this)
			defer lib.XFreeValue(tls, ctx, dup)
			in = append(in, reflect.ValueOf(Value{vm: m, v: dup}))
		default:
			v, err := m.value(this, false)
			if err != nil {
				return throwTypeError(tls, ctx, fmt.Sprintf("calling %s: argument conversion error: %v", info.name, err))
			}

			rv := reflect.ValueOf(v)
			if !rv.Type().AssignableTo(info.in[0]) {
				return throwTypeError(tls, ctx, fmt.Sprintf("calling %s: cannot assign %s to %s", info.name, rv.Type(), info.in[0]))
			}

			in = append(in, rv)
		}
		i++
	}
	types := info.in[i:]
	for i := 0; i < int(argc); i++ {
		var typ reflect.Type
		switch {
		case i < len(types):
			typ = types[i]
		default:
			typ = info.variadicType
		}
		switch {
		case isNative(typ):
			dup := lib.XDupValue(tls, ctx, *(*lib.TJSValue)(unsafe.Pointer(argv)))
			defer lib.XFreeValue(tls, ctx, dup)
			in = append(in, reflect.ValueOf(Value{vm: m, v: dup}))
		default:
			v, err := m.value(*(*lib.TJSValue)(unsafe.Pointer(argv)), false)
			if err != nil {
				return throwTypeError(tls, ctx, fmt.Sprintf("calling %s: argument conversion error: %v", info.name, err))
			}

			rv := reflect.ValueOf(v)
			if !rv.Type().AssignableTo(typ) {
				return throwTypeError(tls, ctx, fmt.Sprintf("calling %s: cannot assign %s to %s", info.name, rv.Type(), typ))
			}

			in = append(in, rv)
		}
		argv += sizeofJsValue
	}

	out := info.f.Call(in)
	var err error
	switch len(info.out) {
	case 0:
		return undefined
	case 1:
		switch {
		case isNative(info.out[0]):
			x, ok := out[0].Interface().(Value)
			if !ok {
				return throwTypeError(tls, ctx, fmt.Sprintf("internal error: native Javascript value does not have type 'Value' (%v:)", origin(1)))
			}

			r = x.v
		default:
			r, err = m.jsValue(out[0])
			if err != nil {
				return throwTypeError(tls, ctx, fmt.Sprintf("cannot convert value: %v", err))
			}
		}
	default:
		r, err = m.jsArray(out)
	}
	if err != nil {
		return throwTypeError(tls, ctx, fmt.Sprintf("callback %s: %v", info.name, err))
	}

	return r
}

func (m *VM) jsValue(in reflect.Value) (out lib.TJSValue, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	defer func() {
		if isException(out) && err == nil {
			err = m.errFromException()
		}
	}()

	switch x := in.Interface().(type) {
	case nil:
		return null, nil
	case Undefined:
		return undefined, nil
	case error:
		return m.newString(x.Error())
	}

	switch in.Kind() {
	case reflect.Pointer:
		return m.jsPtrValue(in)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return jsInt(in.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return jsUint(in.Uint()), nil
	case reflect.Float64, reflect.Float32:
		return newFloat(in.Float()), nil
	case reflect.String:
		return m.newString(in.String())
	case reflect.Struct:
		b, err := json.Marshal(in.Interface())
		if err != nil {
			return out, err
		}

		p, err := libc.CString(string(b))
		if err != nil {
			return out, err
		}

		defer libc.Xfree(tls, p)

		v := lib.XJS_ParseJSON(tls, ctx, p, libc.Tsize_t(len(b)), uintptr(unsafe.Pointer(&evalFN)))
		if isException(v) {
			return out, m.errFromException()
		}

		return v, nil
	case reflect.Interface:
		return m.jsInterfaceValue(in)
	case reflect.Slice:
		var a []reflect.Value
		for i := 0; i < in.Len(); i++ {
			a = append(a, in.Index(i))
		}
		return m.jsArray(a)
	default:
		return out, fmt.Errorf("internal error: %v (%v:)", in.Kind(), origin(1))
	}
}

func (m *VM) jsInterfaceValue(in reflect.Value) (out lib.TJSValue, err error) {
	if in.IsNil() {
		return null, nil
	}

	return m.jsValue(in.Elem())
}

func (m *VM) jsPtrValue(in reflect.Value) (out lib.TJSValue, err error) {
	if in.IsNil() {
		return null, nil
	}

	switch x := in.Interface().(type) {
	case *Object:
		return m.parseJSON(x.json, 0)
	case *big.Int:
		s := x.String() + "n"
		return m.eval(s, EvalGlobal)
	default:
		ev := in.Elem()
		if ev.Kind() == reflect.Pointer { // avoid unbound recursion
			return out, fmt.Errorf("type not supported: %T", x)
		}

		return m.jsValue(ev)
	}
}

func (m *VM) jsArray(a []reflect.Value) (out lib.TJSValue, err error) {
	var s []lib.TJSValue
	for _, v := range a {
		switch x := v.Interface().(type) {
		case Value:
			s = append(s, x.v)
		default:
			jv, err := m.jsValue(v)
			if err != nil {
				return out, err
			}

			s = append(s, jv)
		}
	}
	return m.jsArrayOf(s)
}

func (m *VM) jsArrayOf(a []lib.TJSValue) (out lib.TJSValue, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	argv := libc.Xmalloc(tls, lib.Tsize_t(len(a))*lib.Tsize_t(sizeofJsValue))

	defer libc.Xfree(tls, argv)

	p := argv
	for _, v := range a {
		*(*lib.TJSValue)(unsafe.Pointer(p)) = v
		p += sizeofJsValue
	}
	out = lib.Xjs_array_of(tls, ctx, undefined, int32(len(a)), argv)
	if isException(out) {
		err = m.errFromException()
	}
	for _, v := range a {
		lib.XFreeValue(tls, ctx, v)
	}
	return out, err
}

// SetDefaultModuleLoader will enable loading module using the default module
// loader.
func (m *VM) SetDefaultModuleLoader() {
	lib.XJS_SetModuleLoaderFunc2(m.runtime.tls, m.runtime.cRuntime, 0, fp(lib.Xjs_module_loader), 0, 0)
}

// Atom is an unique identifier of, for example, a string value. Atom values
// are VM-specific.
type Atom = lib.TJSAtom

// NewAtom returns an unique indentifier of 's' or an error, if any.
func (m *VM) NewAtom(s string) (r Atom, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	p, err := libc.CString(s)
	if err != nil {
		return lib.MJS_ATOM_NULL, fmt.Errorf("OOM")
	}

	defer libc.Xfree(tls, p)

	if r = lib.XJS_NewAtom(tls, ctx, p); r == lib.MJS_ATOM_NULL {
		err = fmt.Errorf("OOM")
	}

	return r, err
}

// SetPropertyValue sets this.prop = val.
func (m *VM) SetPropertyValue(this Value, prop Atom, val Value) (err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	dup := val.Dup()
	if lib.XSetProperty(tls, ctx, this.v, prop, dup.v) < 0 {
		return fmt.Errorf("failed to set property")
	}

	return nil
}

// SetProperty sets this.prop = val.
func (m *VM) SetProperty(this Value, prop Atom, val any) (err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	jsArgs, free, err := m.convertArgs(val)
	if err != nil {
		return err
	}

	if len(free) != 0 {
		defer func() {
			for _, v := range free {
				lib.XFreeValue(tls, ctx, v)
			}
		}()
	}

	return m.SetPropertyValue(this, prop, Value{vm: m, v: jsArgs[0]})
}

// GetPropertyValue returns this.prop.
//
// Note: See the [Value] documentation for details about manual memory
// management of Value objects.
func (m *VM) GetPropertyValue(this Value, prop Atom) (r Value, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	v := lib.XGetProperty(tls, ctx, this.v, prop)
	if isException(v) {
		err = m.errFromException()
	}
	return Value{vm: m, v: v}, err
}

// GetProperty returns this.prop.
func (m *VM) GetProperty(this Value, prop Atom) (r any, err error) {
	tls := m.runtime.tls
	ctx := m.cContext
	return m.value(lib.XGetProperty(tls, ctx, this.v, prop), true)
}

// Value represents a native Javascript value. Values are reference counted and
// their lifetime is managed by an independent Javascript garbage collector.
// To avoid memory corruption/leaks caused by tripping the Javascript GC, a
// Value must not
//
//   - be copied. Use the Dup method instead.
//   - become unreachable without calling its Free method.
//   - be used after its Free() method was called.
//   - outlive its VM.
//
// It is recommended to use native Go values instead of Value where possible.
//
// When passing a Value down the call stack use Dup. For example in main
//
//	v, _ := EvalValue(someScript)
//	defer v.Free()
//	foo(v.Dup()) // Instead of foo(v)
//
// In 'foo' Free must be used. For example
//
//	func foo(v Value) {
//	        defer v.Free()
//	        ...
//	}
//
// This ensures the/only topmost Free marks 'v' eligible for garbage collection.
//
// Beware that the correct setup/handling becomes more complicated when using
// closures, Values are sent through a channel etc. In particular, if a
// goroutine 1 passes a Dup of 'v' to goroutine 2 and goroutine 1 completes and
// thus frees 'v' before goroutine 2 completes, the reference counting
// mechanism will fail. In other words, every Free must be strictly paired with
// the Dup that preceded obtaining the Value and the Dup/Free calls must
// respect the original nesting. This is correct.
//
//	Dup             // in main
//	        Dup     // in foo
//	        Free    // in foo
//	Free            // in main
//
// This will fail, for example in the above discussed goroutines scenario.
//
//	Dup            // in g1
//	        Dup    // in g2
//	Free           // in g1
//	        Free   // in g2
//
// The fix might be in this case to arrange goroutine 1 to wait for goroutine 2
// to complete before executing Free in goroutine 1.
//
// See TestMemgrind for an example of how to detect memory leaks caused not
// only by improper Value use.
type Value struct {
	vm *VM
	v  lib.TJSValue
}

// VM returns the VM associated with 'v'.
func (v *Value) VM() *VM {
	return v.vm
}

// SetProperty sets v.prop = val.
func (v Value) SetProperty(prop Atom, val any) (err error) {
	return v.vm.SetProperty(v, prop, val)
}

// SetPropertyValue sets v.prop = val.
func (v Value) SetPropertyValue(prop Atom, val Value) (err error) {
	return v.vm.SetPropertyValue(v, prop, val)
}

// GetPropertyValue returns v.prop.
func (v Value) GetPropertyValue(prop Atom) (r Value, err error) {
	return v.vm.GetPropertyValue(v, prop)
}

// GetProperty returns v.prop.
func (v Value) GetProperty(this Value, prop Atom) (r any, err error) {
	return v.vm.GetProperty(v, prop)
}

// MarshalJSON implements encoding/json.Marshaler.
func (v Value) MarshalJSON() (r []byte, err error) {
	tls := v.vm.runtime.tls
	ctx := v.vm.cContext
	json := lib.XJS_JSONStringify(tls, ctx, v.v, undefined, undefined)

	defer lib.XFreeValue(tls, ctx, json)

	if isException(json) {
		return nil, v.vm.errFromException()
	}

	p := lib.XToCString(tls, ctx, json)
	l := libc.Xstrlen(tls, p)

	defer lib.XJS_FreeCString(tls, ctx, p)

	return libc.GoBytes(p, int(l)), nil
}

// Dup returns a copy of 'v' while updating its reference count.
func (v Value) Dup() Value {
	return Value{
		vm: v.vm,
		v:  lib.XDupValue(v.vm.runtime.tls, v.vm.cContext, v.v),
	}
}

// Free marks 'v' as no longer used and updates its reference count. 'v' must
// not be used afterwards.
func (v *Value) Free() {
	lib.XFreeValue(v.vm.runtime.tls, v.vm.cContext, v.v)
	*v = Value{}
}

// Any attemtps to convert 'v' to any using the same rules as there are for
// the return value of VM.Eval.
func (v Value) Any() (r any, err error) {
	return v.vm.value(v.v, false)
}

// IsUndefined reports whether 'v' represents the Javascript value 'undefined'.
func (v Value) IsUndefined() bool {
	return tag(v.v) == lib.EJS_TAG_UNDEFINED
}

// Version returns the underlying QuickJS version.
func Version() string {
	return lib.MCONFIG_VERSION
}
