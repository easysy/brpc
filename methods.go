package brpc

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"sync"
)

const useAsyncHook = "UseAsyncHook"

type methods map[string]*method

// functions builds a Function map from ms, merging user-supplied descriptions from fns.
func (m methods) functions(fns map[string]Function) map[string]Function {
	functions := make(map[string]Function, len(m))

	for k, v := range m {
		fn := fns[k]
		functions[k] = Function{
			Name:   k,
			Input:  v.iDesc.merge(fn.Input),
			Output: v.oDesc.merge(fn.Output),
		}
	}
	return functions
}

type method struct {
	method reflect.Method
	iType  reflect.Type // third parameter (after receiver and context)
	oType  reflect.Type // first return value
	iDesc  *Entity
	oDesc  *Entity
}

// suitableMethods returns all methods on typ that match the RPC signature:
//
//	func (t *T) Method(ctx context.Context, in T1) (T2, error)
//
// or the async hook signature:
//
//	func (t *T) UseAsyncHook(hook chan any)
//
// Non-matching methods are silently skipped.
func suitableMethods(typ reflect.Type) methods {
	ms := make(methods)

	for n := 0; n < typ.NumMethod(); n++ {
		m := typ.Method(n)
		mType := m.Type

		if isUseAsyncHook(m.Name, mType) {
			ms[m.Name] = &method{method: m}
			continue
		}

		if mType.NumIn() != 3 || mType.NumOut() != 2 {
			continue
		}

		if cType := mType.In(1); cType != reflect.TypeFor[context.Context]() {
			continue
		}

		iType := mType.In(2)
		if unsuitableType(iType, true) {
			continue
		}

		oType := mType.Out(0)
		if unsuitableType(oType, false) {
			continue
		}

		if eType := mType.Out(1); eType != reflect.TypeFor[error]() {
			continue
		}

		ms[m.Name] = &method{
			method: m,
			iType:  iType,
			oType:  oType,
			iDesc:  cachedDescriptions(iType),
			oDesc:  cachedDescriptions(oType),
		}
	}

	return ms
}

func isUseAsyncHook(mName string, mType reflect.Type) bool {
	if mName != useAsyncHook {
		return false
	}
	return mType.NumIn() == 2 && mType.NumOut() == 0 && mType.In(1) == reflect.TypeFor[chan any]()
}

// unsuitableType reports whether t is an invalid, chan, or func kind.
// When i=true, a non-empty interface is also considered unsuitable.
func unsuitableType(t reflect.Type, i bool) bool {
	k := t.Kind()
	if i && k == reflect.Interface {
		return t != reflect.TypeFor[any]()
	}
	return k == reflect.Invalid || k == reflect.Chan || k == reflect.Func
}

// TypeDescription returns a reflected type description of v.
func TypeDescription(v any) *Entity {
	return cachedDescriptions(reflect.TypeOf(v))
}

var cache sync.Map // map[reflect.Type]*Entity

func cachedDescriptions(t reflect.Type) *Entity {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if c, ok := cache.Load(t); ok {
		return c.(*Entity)
	}
	c, _ := cache.LoadOrStore(t, describe(t))
	return c.(*Entity)
}

func describe(t reflect.Type) *Entity {
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		elem := cachedDescriptions(t.Elem())
		if elem == nil {
			return elem
		}
		return &Entity{Type: "[]" + elem.Type, Fields: elem.Fields}
	case reflect.Map:
		switch t.Key().Kind() {
		case reflect.String,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		default:
			return nil
		}
		elem := cachedDescriptions(t.Elem())
		return &Entity{Type: "map[" + t.Key().Kind().String() + "]" + elem.Type, Fields: elem.Fields}
	case reflect.Struct:
		if t.NumField() == 0 {
			return nil
		}
	default:
		return &Entity{Type: t.Kind().String()}
	}

	entity := &Entity{Type: t.Kind().String()}

	for n := 0; n < t.NumField(); n++ {
		fv := t.Field(n)
		ft := fv.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		tag, ok := fv.Tag.Lookup("brpc")
		if ok && tag == "-" {
			continue
		}

		fd := Entity{
			Name:      fv.Name,
			Type:      ft.Kind().String(),
			Mandatory: true,
		}

		tag, ok = fv.Tag.Lookup("json")
		if ok {
			if tag == "-" {
				continue
			}

			tags := strings.Split(tag, ",")

			fd.Name = tags[0]
			if len(tags) > 1 {
				fd.Mandatory = tags[1] != "omitempty"
			}
		}

		if fv.Anonymous && !ok {
			if !fv.IsExported() && ft.Kind() != reflect.Struct {
				continue
			}

			if embedded := cachedDescriptions(ft); embedded != nil {
				if ft.Kind() != reflect.Struct {
					fd.Type = embedded.Type
					fd.Fields = embedded.Fields
					entity.Fields = append(entity.Fields, fd)
				} else {
					entity.Fields = append(entity.Fields, clean(embedded.Fields, entity.Fields...)...)
				}
			}

			continue
		} else if !fv.IsExported() {
			continue
		}

		if ft.Kind() == reflect.Array || ft.Kind() == reflect.Map || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Struct {
			desc := cachedDescriptions(ft)
			if desc == nil {
				continue
			}

			fd.Type = desc.Type
			fd.Fields = desc.Fields
		}

		entity.Fields = append(clean(entity.Fields, fd), fd)
	}

	if t.Kind() == reflect.Struct && entity.Fields == nil {
		return nil
	}

	return entity
}

func clean(slice []Entity, elems ...Entity) []Entity {
	dst := make([]Entity, 0, len(slice))
	for _, e := range slice {
		if !slices.ContainsFunc(elems, func(ec Entity) bool {
			return e.Name == ec.Name
		}) {
			dst = append(dst, e)
		}
	}
	return dst
}
