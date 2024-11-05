package brpc

import (
	"context"
	"reflect"
)

const useAsyncHook = "UseAsyncHook"

// methodType holds metadata about a registered method.
type methodType struct {
	method reflect.Method // The reflected method
	iType  reflect.Type   // Input type of the method
	oType  reflect.Type   // Output type of the method
}

// suitableMethods returns suitable methods from the provided type.
func suitableMethods(typ reflect.Type) map[string]*methodType {
	methods := make(map[string]*methodType)

	for n := 0; n < typ.NumMethod(); n++ {
		method := typ.Method(n)
		mType := method.Type

		if isUseAsyncHook(method.Name, mType) {
			methods[method.Name] = &methodType{method: method}
			continue
		}

		// Check that the method has exactly 3 input parameters and 2 output parameters
		if mType.NumIn() != 3 || mType.NumOut() != 2 {
			continue
		}

		// Ensure the second input parameter is of type context.Context
		if cType := mType.In(1); cType != reflect.TypeFor[context.Context]() {
			continue
		}

		// Ensure the second input parameter is not an invalid type, chan, func or interface
		if unsuitableType(mType.In(2), true) {
			continue
		}

		// Ensure the first output parameter is not an invalid type, chan or func
		if unsuitableType(mType.Out(0), false) {
			continue
		}

		// Ensure the second output parameter is of type error
		if eType := mType.Out(1); eType != reflect.TypeFor[error]() {
			continue
		}

		// Register the method, storing the input and output types
		methods[method.Name] = &methodType{method: method, iType: mType.In(2), oType: mType.Out(0)}
	}

	return methods
}

// isUseAsyncHook checks if a method is the "UseAsyncHook" function with the expected signature.
func isUseAsyncHook(mName string, mType reflect.Type) bool {
	if mName != useAsyncHook {
		return false
	}

	// Check that the method has exactly 2 input parameters
	if mType.NumIn() != 2 || mType.NumOut() != 0 {
		return false
	}

	// Ensure the second input parameter is a channel of any type
	return mType.In(1) == reflect.TypeFor[chan any]()
}

// unsuitableType checks if type `t` is unsuitable (invalid type, chan or func)
// if `i` is true, additionally checks for a non-empty interface.
func unsuitableType(t reflect.Type, i bool) bool {
	k := t.Kind()
	if i && k == reflect.Interface {
		return t != reflect.TypeFor[any]()
	}
	return k == reflect.Invalid || k == reflect.Chan || k == reflect.Func
}
