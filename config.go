package yuruidb

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Config is the driver-neutral configuration path for applications that import
// only backends and choose drivers through build tags. Keys are the fields of
// the selected driver's Config, case-insensitively. Typed constructors and
// typed driver Config values remain available for direct imports. Function and
// client values can be supplied without serializing credentials or callbacks.
type Config map[string]any

// DecodeConfig helps statically imported third-party factories accept either
// their own configuration type or a driver-neutral Config/map. Unknown fields
// are rejected. It performs no I/O or environment lookup.
func DecodeConfig[T any](input any) (T, error) {
	var output T
	if typed, ok := input.(T); ok {
		return typed, nil
	}
	if typed, ok := input.(*T); ok && typed != nil {
		return *typed, nil
	}
	var values map[string]any
	switch v := input.(type) {
	case Config:
		values = v
	case map[string]any:
		values = v
	default:
		return output, fmt.Errorf("%w: expected driver config or yuruidb.Config", ErrInvalid)
	}
	target := reflect.ValueOf(&output).Elem()
	if target.Kind() != reflect.Struct {
		return output, fmt.Errorf("%w: driver config must be a struct", ErrInvalid)
	}
	seen := map[int]bool{}
	for key, value := range values {
		index := -1
		for i := 0; i < target.NumField(); i++ {
			if strings.EqualFold(target.Type().Field(i).Name, key) {
				index = i
				break
			}
		}
		if index < 0 || !target.Field(index).CanSet() {
			return output, fmt.Errorf("%w: unknown config field %q", ErrInvalid, key)
		}
		if seen[index] {
			return output, fmt.Errorf("%w: duplicate config field %q", ErrInvalid, key)
		}
		seen[index] = true
		field := target.Field(index)
		if value == nil {
			switch field.Kind() {
			case reflect.Pointer, reflect.Func, reflect.Map, reflect.Slice, reflect.Interface:
				field.SetZero()
				continue
			default:
				return output, fmt.Errorf("%w: nil config field %q", ErrInvalid, key)
			}
		}
		incoming := reflect.ValueOf(value)
		if incoming.Type().AssignableTo(field.Type()) {
			field.Set(incoming)
			continue
		}
		if field.Type() == reflect.TypeOf(time.Duration(0)) {
			if text, ok := value.(string); ok {
				duration, err := time.ParseDuration(text)
				if err != nil {
					return output, fmt.Errorf("%w: invalid duration for %q", ErrInvalid, key)
				}
				field.SetInt(int64(duration))
				continue
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return output, fmt.Errorf("%w: wrong type for config field %q", ErrInvalid, key)
		}
		if err = json.Unmarshal(encoded, field.Addr().Interface()); err != nil {
			return output, fmt.Errorf("%w: wrong type for config field %q", ErrInvalid, key)
		}
	}
	return output, nil
}
