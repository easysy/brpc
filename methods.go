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

func (m methods) functions() []Function {
	functions := make([]Function, 0, len(m))
	for k, v := range m {
		functions = append(functions, Function{
			Name:   k,
			Input:  v.iDesc,
			Output: v.oDesc,
		})
	}
	return functions
}

// method holds metadata about a registered method.
type method struct {
	method reflect.Method // The reflected method
	iType  reflect.Type   // Input type of the method
	oType  reflect.Type   // Output type of the method
	iDesc  *Entity        // Description of the input data type of the method
	oDesc  *Entity        // Description of the output data type of the method
}

// suitableMethods returns suitable methods from the provided type.
func suitableMethods(typ reflect.Type) methods {
	ms := make(methods)

	for n := 0; n < typ.NumMethod(); n++ {
		m := typ.Method(n)
		mType := m.Type

		if isUseAsyncHook(m.Name, mType) {
			ms[m.Name] = &method{method: m}
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

		iType := mType.In(2)

		// Ensure the second input parameter is not an invalid type, chan, func or interface
		if unsuitableType(iType, true) {
			continue
		}

		oType := mType.Out(0)

		// Ensure the first output parameter is not an invalid type, chan or func
		if unsuitableType(mType.Out(0), false) {
			continue
		}

		// Ensure the second output parameter is of type error
		if eType := mType.Out(1); eType != reflect.TypeFor[error]() {
			continue
		}

		// Register the method, storing the input and output types
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

func TypeDescription(v any) *Entity {
	return cachedDescriptions(reflect.TypeOf(v))
}

var cache sync.Map // map[reflect.Type]*Entity

// cachedDescriptions is like describe but uses a cache to avoid repeated work.
func cachedDescriptions(t reflect.Type) *Entity {
	t = indirect(t)
	if c, ok := cache.Load(t); ok {
		return c.(*Entity)
	}
	c, _ := cache.LoadOrStore(t, describe(t))
	return c.(*Entity)
}

func describe(t reflect.Type) *Entity {
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		desc := cachedDescriptions(t.Elem())
		if desc != nil {
			desc.Type = "[]" + desc.Type
		}
		return desc
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

	// Iterate over fields
	for n := 0; n < t.NumField(); n++ {
		fv := t.Field(n)
		ft := indirect(fv.Type)

		tag, ok := fv.Tag.Lookup("brpc")
		if ok && tag == "-" {
			// Ignore the field if the tag has a skip value.
			continue
		}

		fd := Entity{
			Name:      fv.Name,
			Type:      ft.Kind().String(),
			Mandatory: true,
		}

		tag, ok = fv.Tag.Lookup("json")
		if ok {
			// Ignore the field if the tag has a skip value.
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
			// Ignore embedded fields of unexported non-struct types.
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
			// Ignore unexported non-embedded fields.
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

func indirect(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Ptr {
		return indirect(t.Elem())
	}
	return t
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
