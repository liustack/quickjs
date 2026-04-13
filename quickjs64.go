// Copyright 2025 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !(386 || arm)

package quickjs // import "modernc.org/quickjs"

import (
	"unsafe"

	lib "modernc.org/libquickjs"
)

const sizeofJsValue = unsafe.Sizeof(lib.TJSValue{})

var (
	null      = lib.TJSValue{Ftag: lib.EJS_TAG_NULL}
	undefined = lib.TJSValue{Ftag: lib.EJS_TAG_UNDEFINED}
)

func newFloat(n float64) (r lib.TJSValue) {
	*(*float64)(unsafe.Pointer(&r)) = n
	r.Ftag = lib.EJS_TAG_FLOAT64
	return r
}

func newBool(n bool) (r lib.TJSValue) {
	switch {
	case n:
		*(*int32)(unsafe.Pointer(&r)) = 1
	default:
		*(*int32)(unsafe.Pointer(&r)) = 0
	}
	r.Ftag = lib.EJS_TAG_BOOL
	return r
}

func isException(v lib.TJSValue) bool {
	return v.Ftag == lib.EJS_TAG_EXCEPTION
}

// #define JS_VALUE_GET_TAG(v) ((int32_t)(v).tag)

func tag(v lib.TJSValue) int32 {
	return int32(v.Ftag)
}

func jsvToFloat64(v lib.TJSValue) (r float64) {
	return *(*float64)(unsafe.Pointer(&v))
}

// jsvToPtr extracts the JSModuleDef pointer from a JSValue.
func jsvToPtr(v lib.TJSValue) uintptr {
	return *(*uintptr)(unsafe.Pointer(&v))
}
