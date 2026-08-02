package eval

import (
	"fmt"
	"reflect"
)

type payloadCloneVisit struct {
	kind reflect.Kind
	typ  reflect.Type
	ptr  uintptr
}

func cloneFixturePoints(points []FixturePoint) ([]FixturePoint, error) {
	if points == nil {
		return nil, nil
	}
	cloned := make([]FixturePoint, len(points))
	for i := range points {
		cloned[i] = points[i]
		cloned[i].Vector = append(Vector(nil), points[i].Vector...)
		payload, err := clonePayload(points[i].Payload)
		if err != nil {
			return nil, fmt.Errorf("point %q payload: %w", points[i].ID.String(), err)
		}
		cloned[i].Payload = payload
	}
	return cloned, nil
}

func clonePayload(source map[string]any) (map[string]any, error) {
	if source == nil {
		return nil, nil
	}
	value, err := clonePayloadReflect(
		reflect.ValueOf(source),
		make(map[payloadCloneVisit]struct{}),
	)
	if err != nil {
		return nil, err
	}
	return value.Interface().(map[string]any), nil
}

func clonePayloadReflect(
	value reflect.Value,
	stack map[payloadCloneVisit]struct{},
) (reflect.Value, error) {
	if !value.IsValid() {
		return reflect.Value{}, nil
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		cloned, err := clonePayloadReflect(value.Elem(), stack)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result, nil
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		visit := payloadCloneVisit{
			kind: value.Kind(), typ: value.Type(), ptr: value.Pointer(),
		}
		if _, cyclic := stack[visit]; cyclic {
			return reflect.Value{}, fmt.Errorf("cyclic payload value of type %s", value.Type())
		}
		stack[visit] = struct{}{}
		defer delete(stack, visit)
		cloned, err := clonePayloadReflect(value.Elem(), stack)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(value.Type().Elem())
		result.Elem().Set(cloned)
		return result, nil
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		if value.Type().Key().Kind() != reflect.String {
			return reflect.Value{}, fmt.Errorf(
				"unsupported payload map key type %s", value.Type().Key())
		}
		visit := payloadCloneVisit{
			kind: value.Kind(), typ: value.Type(), ptr: value.Pointer(),
		}
		if _, cyclic := stack[visit]; cyclic {
			return reflect.Value{}, fmt.Errorf("cyclic payload value of type %s", value.Type())
		}
		stack[visit] = struct{}{}
		defer delete(stack, visit)
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			cloned, err := clonePayloadReflect(iterator.Value(), stack)
			if err != nil {
				return reflect.Value{}, err
			}
			result.SetMapIndex(iterator.Key(), cloned)
		}
		return result, nil
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		visit := payloadCloneVisit{
			kind: value.Kind(), typ: value.Type(), ptr: value.Pointer(),
		}
		if _, cyclic := stack[visit]; cyclic {
			return reflect.Value{}, fmt.Errorf("cyclic payload value of type %s", value.Type())
		}
		stack[visit] = struct{}{}
		defer delete(stack, visit)
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			cloned, err := clonePayloadReflect(value.Index(i), stack)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(i).Set(cloned)
		}
		return result, nil
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			cloned, err := clonePayloadReflect(value.Index(i), stack)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(i).Set(cloned)
		}
		return result, nil
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		for i := 0; i < value.NumField(); i++ {
			if !result.Field(i).CanSet() || !value.Type().Field(i).IsExported() {
				return reflect.Value{}, fmt.Errorf(
					"unsupported payload type %s with unexported fields", value.Type())
			}
			cloned, err := clonePayloadReflect(value.Field(i), stack)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Field(i).Set(cloned)
		}
		return result, nil
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return value, nil
	default:
		return reflect.Value{}, fmt.Errorf("unsupported payload type %s", value.Type())
	}
}
