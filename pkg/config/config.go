package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

const (
	varsKey            = "vars"
	noValuePlaceholder = "<no value>"
)

// Option configures template processing.
type Option func(*options)

type options struct {
	vars map[string]string // additional template vars (below env priority)
	env  map[string]string // override env source (default: os.Environ())
}

// WithVars provides additional template variables.
// These have lower priority than environment variables but higher than
// the config's vars: section.
func WithVars(vars map[string]string) Option {
	return func(o *options) {
		o.vars = vars
	}
}

// WithEnv overrides the environment variable source.
// By default, os.Environ() is used.
func WithEnv(env map[string]string) Option {
	return func(o *options) {
		o.env = env
	}
}

// ProcessFile reads a YAML file, processes Go templates, and returns
// the processed bytes ready for unmarshaling, plus resolved vars.
func ProcessFile(path string, opts ...Option) ([]byte, map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return Process(data, opts...)
}

// Process processes raw YAML bytes as a Go template.
// Returns processed bytes and resolved vars from the vars: section.
//
// Template features:
//   - vars: section for defining template variables
//   - Dual delimiters: {{ .VAR }} and [[ .VAR ]]
//   - Template functions: default, required, env, add
//   - Iterative resolution (max 10 passes) for recursive var definitions
//   - Priority: env vars > WithVars() > config's vars: section
func Process(data []byte, opts ...Option) ([]byte, map[string]string, error) {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}

	// Build env map
	env := o.env
	if env == nil {
		env = environMap()
	}

	// Merge WithVars into env (env wins)
	if o.vars != nil {
		merged := make(map[string]string, len(env)+len(o.vars))
		for k, v := range o.vars {
			merged[k] = v
		}
		for k, v := range env {
			merged[k] = v
		}
		env = merged
	}

	result, err := processRawConfig(data, env)
	if err != nil {
		return nil, nil, err
	}

	// Extract resolved vars before removing the section
	var rawCfg struct {
		Vars map[string]string `yaml:"vars"`
	}
	yaml.Unmarshal(result, &rawCfg)
	resolvedVars := rawCfg.Vars
	if resolvedVars == nil {
		resolvedVars = make(map[string]string)
	}

	// Remove the vars section from the output
	result = removeVarsSection(result)

	return result, resolvedVars, nil
}

// processRawConfig performs template substitution on raw YAML bytes.
// It resolves the vars section first (iteratively, to handle inter-var
// dependencies), then applies the fully-resolved vars to the rest of
// the config in a single pass.
func processRawConfig(data []byte, env map[string]string) ([]byte, error) {
	original := data

	// Phase 1: resolve vars iteratively.
	resolvedVars, err := resolveVars(data, env)
	if err != nil {
		return nil, err
	}

	// Phase 2: build final template data and process the full config.
	// Priority: env > resolved vars
	templateData := make(map[string]any, len(resolvedVars)+len(env))
	for k, v := range resolvedVars {
		templateData[k] = v
	}
	for k, v := range env {
		templateData[k] = v
	}

	result := data

	result, err = executeTemplate(result, templateData, "[[", "]]", env)
	if err != nil {
		return nil, fmt.Errorf("template error (using [[ ]]): %w", err)
	}

	result, err = executeTemplate(result, templateData, "{{", "}}", env)
	if err != nil {
		return nil, fmt.Errorf("template error (using {{ }}): %w", err)
	}

	if bytes.Contains(result, []byte(noValuePlaceholder)) {
		resultLines := bytes.Split(result, []byte("\n"))
		originalLines := bytes.Split(original, []byte("\n"))
		var problemLines []string
		for i, line := range resultLines {
			if bytes.Contains(line, []byte(noValuePlaceholder)) {
				originalLine := ""
				if i < len(originalLines) {
					originalLine = string(originalLines[i])
				}
				problemLines = append(problemLines, fmt.Sprintf("  line %d: %s", i+1, strings.TrimSpace(originalLine)))
			}
		}
		return nil, fmt.Errorf("undefined variable in config. Use 'default' function or define the variable.\nProblem lines:\n%s", strings.Join(problemLines, "\n"))
	}

	return result, nil
}

// resolveVars extracts the vars section from YAML and resolves template
// expressions iteratively. Each pass resolves vars whose dependencies
// are already resolved, until all vars are stable or max iterations reached.
func resolveVars(data []byte, env map[string]string) (map[string]string, error) {
	var rawCfg struct {
		Vars map[string]any `yaml:"vars"`
	}
	yaml.Unmarshal(data, &rawCfg)
	if len(rawCfg.Vars) == 0 {
		return nil, nil
	}

	// Convert raw var values to strings
	unresolved := make(map[string]string, len(rawCfg.Vars))
	for k, v := range rawCfg.Vars {
		unresolved[k] = fmt.Sprintf("%v", v)
	}

	resolved := make(map[string]string)

	for i := 0; i < 10; i++ {
		if len(unresolved) == 0 {
			break
		}

		progress := false
		for k, expr := range unresolved {
			// Build template data: already-resolved vars + env (env wins)
			td := make(map[string]any, len(resolved)+len(env))
			for rk, rv := range resolved {
				td[rk] = rv
			}
			for ek, ev := range env {
				td[ek] = ev
			}

			// Try to resolve this var's expression
			val, err := resolveExpr(expr, td, env)
			if err != nil {
				continue // dependency not yet resolved
			}

			// Check if result still contains template expressions
			if strings.Contains(val, "{{") || strings.Contains(val, "[[") ||
				strings.Contains(val, noValuePlaceholder) {
				continue
			}

			resolved[k] = val
			delete(unresolved, k)
			progress = true
		}

		if !progress {
			break
		}
	}

	// If any vars remain unresolved, try once more to get a useful error.
	if len(unresolved) > 0 {
		td := make(map[string]any, len(resolved)+len(env))
		for k, v := range resolved {
			td[k] = v
		}
		for k, v := range env {
			td[k] = v
		}
		for k, expr := range unresolved {
			_, err := resolveExpr(expr, td, env)
			if err != nil {
				return nil, fmt.Errorf("var %q: %w", k, err)
			}
			return nil, fmt.Errorf("var %q could not be resolved (circular dependency?)", k)
		}
	}

	return resolved, nil
}

// resolveExpr evaluates a single template expression string, trying
// both [[ ]] and {{ }} delimiters.
func resolveExpr(expr string, templateData map[string]any, env map[string]string) (string, error) {
	result := expr

	if strings.Contains(result, "[[") {
		out, err := executeTemplate([]byte(result), templateData, "[[", "]]", env)
		if err != nil {
			return "", err
		}
		result = string(out)
	}

	if strings.Contains(result, "{{") {
		out, err := executeTemplate([]byte(result), templateData, "{{", "}}", env)
		if err != nil {
			return "", err
		}
		result = string(out)
	}

	return result, nil
}

// executeTemplate runs Go template substitution with the given delimiters.
func executeTemplate(data []byte, templateData map[string]any, leftDelim, rightDelim string, env map[string]string) ([]byte, error) {
	tmpl, err := template.New("config").
		Delims(leftDelim, rightDelim).
		Option("missingkey=zero").
		Funcs(templateFuncs(env)).
		Parse(string(data))
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, templateData); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// templateFuncs returns custom functions available in templates.
func templateFuncs(env map[string]string) template.FuncMap {
	return template.FuncMap{
		"default": func(def, val any) any {
			if val == nil {
				return def
			}
			if s, ok := val.(string); ok && s == "" {
				return def
			}
			return val
		},

		"env": func(name string) string {
			return env[name]
		},

		"required": func(msg string, val any) (any, error) {
			if val == nil {
				return nil, fmt.Errorf("%s", msg)
			}
			if s, ok := val.(string); ok && s == "" {
				return nil, fmt.Errorf("%s", msg)
			}
			return val, nil
		},

		// asInt converts a value to int (for unquoted YAML numbers)
		// Usage: {{ .PORT | asInt }}
		"asInt": func(val any) (int, error) {
			return toInt(val)
		},

		// int is an alias for asInt
		// Usage: {{ .PORT | int }}
		"int": func(val any) (int, error) {
			return toInt(val)
		},

		"add": func(a, b any) (int, error) {
			aInt, err := toInt(a)
			if err != nil {
				return 0, fmt.Errorf("add: first argument: %w", err)
			}
			bInt, err := toInt(b)
			if err != nil {
				return 0, fmt.Errorf("add: second argument: %w", err)
			}
			return aInt + bInt, nil
		},
	}
}

// toInt converts various numeric types to int.
func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int8:
		return int(n), nil
	case int16:
		return int(n), nil
	case int32:
		return int(n), nil
	case int64:
		return int(n), nil
	case uint:
		return int(n), nil
	case uint8:
		return int(n), nil
	case uint16:
		return int(n), nil
	case uint32:
		return int(n), nil
	case uint64:
		return int(n), nil
	case float32:
		return int(n), nil
	case float64:
		return int(n), nil
	case string:
		// If the string contains unresolved template expressions, return 0
		// without error. The iterative resolution loop will re-evaluate
		// once the dependency is resolved in a subsequent pass.
		if strings.Contains(n, "{{") || strings.Contains(n, "[[") {
			return 0, nil
		}
		var i int
		if _, err := fmt.Sscanf(n, "%d", &i); err != nil {
			return 0, fmt.Errorf("cannot convert %q to int", n)
		}
		return i, nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int", v)
	}
}

// removeVarsSection removes the vars: top-level key from YAML bytes.
func removeVarsSection(data []byte) []byte {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return data
	}
	delete(raw, varsKey)
	out, err := yaml.Marshal(raw)
	if err != nil {
		return data
	}
	return out
}

// environMap converts os.Environ() to a map.
func environMap() map[string]string {
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok {
			env[k] = v
		}
	}
	return env
}
