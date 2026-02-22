package config

import (
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/mitchellh/mapstructure"
)

var validate = validator.New()

// O is a configuration object represented as a nested map.
// It provides methods for accessing configuration values using dot-notation paths.
type O map[string]any

// Get retrieves a value at the given dot-notation path.
// Returns the value and true if found, or nil and false if the path doesn't exist.
func (this O) Get(path string) (any, bool) {
	parts := strings.Split(path, ".")
	var current any = this

	for _, p := range parts {
		m, ok := current.(O)
		if !ok {
			m, ok = current.(map[string]any)
			if !ok {
				return nil, false
			}
		}
		current, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// GetString retrieves a value at the given path and returns it as a string.
// Returns an empty string if the key doesn't exist.
func (this O) GetString(key string) string {
	if v, ok := this.Get(key); !ok {
		return ""
	} else {
		return fmt.Sprintf("%v", v)
	}
}

// GetStringOrDefault retrieves a value at the given path and returns it as a string.
// Returns the provided default value if the key doesn't exist.
func (this O) GetStringOrDefault(key string, defaultValue string) string {
	if v, ok := this.Get(key); !ok {
		return defaultValue
	} else {
		return fmt.Sprintf("%v", v)
	}
}

// GetNumber retrieves a numeric value at the given path and coerces it to type T.
// Returns the value and true if successful, or zero value and false if the key
// doesn't exist or the value is not numeric.
func GetNumber[T ~int | ~int8 | ~int16 | ~int32 | ~int64 |
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
	~float32 | ~float64](cfg O, path string) (T, bool) {
	v, ok := cfg.Get(path)
	if !ok {
		return T(0), false
	}

	switch n := v.(type) {
	case int:
		return T(n), true
	case int8:
		return T(n), true
	case int16:
		return T(n), true
	case int32:
		return T(n), true
	case int64:
		return T(n), true
	case uint:
		return T(n), true
	case uint8:
		return T(n), true
	case uint16:
		return T(n), true
	case uint32:
		return T(n), true
	case uint64:
		return T(n), true
	case float32:
		return T(n), true
	case float64:
		return T(n), true
	default:
		return T(0), false
	}
}

// GetNumberOrDefault retrieves a numeric value at the given path and coerces it to type T.
// Returns the provided default value if the key doesn't exist or the value is not numeric.
func GetNumberOrDefault[T ~int | ~int8 | ~int16 | ~int32 | ~int64 |
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
	~float32 | ~float64](cfg O, path string, defaultValue T) T {
	if val, ok := GetNumber[T](cfg, path); ok {
		return val
	}
	return defaultValue
}

// GetIntoOption is a functional option for GetInto.
type GetIntoOption func(*getIntoOptions)

type getIntoOptions struct {
	validate bool
}

// WithValidation enables struct validation using "validate" tags after decoding.
func WithValidation() GetIntoOption {
	return func(o *getIntoOptions) {
		o.validate = true
	}
}

// GetInto retrieves a value at the given path and decodes it into the target.
// The target must be a pointer to a struct, map, slice, or primitive type.
// Uses "yaml" struct tags for field mapping and supports weakly-typed input.
func (this O) GetInto(path string, target any, opts ...GetIntoOption) error {
	options := getIntoOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	val, ok := this.Get(path)
	if !ok {
		return fmt.Errorf("key not found: %s", path)
	}

	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           target,
		TagName:          "yaml",
		WeaklyTypedInput: true,
		DecodeHook:       mapstructure.StringToTimeDurationHookFunc(),
	})
	if err != nil {
		return fmt.Errorf("failed to create decoder: %w", err)
	}

	if err := decoder.Decode(val); err != nil {
		return fmt.Errorf("failed to decode into target: %w", err)
	}

	if options.validate {
		if err := validate.Struct(target); err != nil {
			return fmt.Errorf("validation failed: %w", err)
		}
	}

	return nil
}
