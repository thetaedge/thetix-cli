package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/thetaedge/thetix-cli/internal/contract"
)

// Property is one tool argument, as the tool's JSON Schema declares it.
// The constraints are those the server checks; checking them here turns a bad value into a
// usage error before any network call. Nil means unconstrained.
type Property struct {
	Name            string
	Type            string // string, integer, number, boolean, array or object
	Description     string
	Enum            []any
	ItemsType       string // the array's item type: string, integer, number, boolean or object
	ContentEncoding string // "base64": the command line takes a file path
	Default         any
	Minimum         *float64
	Maximum         *float64
	MinLength       *int
	MaxLength       *int
	Pattern         string // a string's (or string item's) regular expression, anchored at the start
	ItemsPattern    string
}

// Schema is a tool's (flat) input schema. Invariant: Required names are Properties.
type Schema struct {
	Properties map[string]Property
	Required   []string
}

// ParseSchema reads the input schema a server sent for a tool.
func ParseSchema(raw any) (Schema, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return Schema{}, err
	}
	var wire struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type            string   `json:"type"`
			Description     string   `json:"description"`
			Enum            []any    `json:"enum"`
			ContentEncoding string   `json:"contentEncoding"`
			Default         any      `json:"default"`
			Minimum         *float64 `json:"minimum"`
			Maximum         *float64 `json:"maximum"`
			MinLength       *int     `json:"minLength"`
			MaxLength       *int     `json:"maxLength"`
			Pattern         string   `json:"pattern"`
			Items           struct {
				Type    string `json:"type"`
				Pattern string `json:"pattern"`
			} `json:"items"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return Schema{}, fmt.Errorf("unreadable input schema: %v", err)
	}
	if wire.Type != "object" {
		return Schema{}, fmt.Errorf("input schema type is %q, not object", wire.Type)
	}
	schema := Schema{Properties: map[string]Property{}, Required: wire.Required}
	for name, p := range wire.Properties {
		kind := p.Type
		if kind == "" {
			kind = "string"
		}
		schema.Properties[name] = Property{Name: name, Type: kind, Description: p.Description, Enum: p.Enum,
			ItemsType: p.Items.Type, ContentEncoding: p.ContentEncoding, Default: p.Default,
			Minimum: p.Minimum, Maximum: p.Maximum, MinLength: p.MinLength, MaxLength: p.MaxLength,
			Pattern: p.Pattern, ItemsPattern: p.Items.Pattern}
	}
	for _, name := range schema.Required {
		if _, ok := schema.Properties[name]; !ok {
			return Schema{}, fmt.Errorf("required argument %q is not declared", name)
		}
	}
	return schema, nil
}

// Names are the property names, sorted.
func (s Schema) Names() []string {
	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// FlagName is a property's command-line flag: chat_id -> chat-id.
func FlagName(property string) string { return strings.ReplaceAll(property, "_", "-") }

// CommandName is a tool's command: ask_thetix -> ask-thetix.
func CommandName(tool string) string { return strings.ReplaceAll(tool, "_", "-") }

// readFile reads a path, or standard input for "-".
func readFile(path string, stdin io.Reader) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(stdin)
	}
	return os.ReadFile(path)
}

// Convert turns one command-line value into the JSON value the property
// takes. A base64 property takes a file path ("-" for standard input); an
// object or array of non-strings takes JSON, or @file with JSON in it.
func (s Schema) Convert(name, value string, stdin io.Reader) (any, error) {
	p, ok := s.Properties[name]
	contract.Require(ok, "Convert of a declared property, not %q", name)
	var out any
	switch p.Type {
	case "string":
		if p.ContentEncoding == "base64" {
			data, err := readFile(value, stdin)
			if err != nil {
				return nil, fmt.Errorf("--%s: %v", FlagName(name), err)
			}
			out = base64.StdEncoding.EncodeToString(data)
		} else {
			out = value
		}
	case "integer", "number", "boolean":
		converted, err := convertScalar(name, p.Type, value)
		if err != nil {
			return nil, err
		}
		out = converted
	case "array":
		if IsListFlag(p) && !strings.HasPrefix(strings.TrimSpace(value), "[") && !strings.HasPrefix(value, "@") {
			items := []any{}
			for _, item := range strings.Split(value, ",") {
				if item = strings.TrimSpace(item); item == "" {
					continue
				}
				converted, err := convertScalar(name, p.ItemsType, item)
				if err != nil {
					return nil, err
				}
				items = append(items, converted)
			}
			out = items
		} else {
			parsed, err := parseJSONArg(name, value, stdin)
			if err != nil {
				return nil, err
			}
			out = parsed
		}
	default:
		parsed, err := parseJSONArg(name, value, stdin)
		if err != nil {
			return nil, err
		}
		out = parsed
	}
	if len(p.Enum) > 0 && !inEnum(p.Enum, out) {
		return nil, fmt.Errorf("--%s must be one of %s", FlagName(name), enumText(p.Enum))
	}
	if err := p.check(out); err != nil {
		return nil, err
	}
	return out, nil
}

// IsListFlag reports whether an array property takes a comma-separated
// list on the command line (items are scalars), rather than JSON.
func IsListFlag(p Property) bool {
	if p.Type != "array" {
		return false
	}
	switch p.ItemsType {
	case "", "string", "integer", "number", "boolean":
		return true
	}
	return false
}

// convertScalar parses one integer, number, boolean or string value.
func convertScalar(name, kind, value string) (any, error) {
	trimmed := strings.TrimSpace(value)
	switch kind {
	case "integer":
		n, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("--%s takes a whole number, not %q", FlagName(name), value)
		}
		return n, nil
	case "number":
		f, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil, fmt.Errorf("--%s takes a number, not %q", FlagName(name), value)
		}
		return f, nil
	case "boolean":
		b, err := strconv.ParseBool(trimmed)
		if err != nil {
			return nil, fmt.Errorf("--%s takes true or false, not %q", FlagName(name), value)
		}
		return b, nil
	}
	return value, nil
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// matches is the server's re.match: the pattern anchored at the start. A
// pattern Go cannot compile is left for the server to check.
func matches(pattern, value string) bool {
	re, err := regexp.Compile(`^(?:` + pattern + `)`)
	return err != nil || re.MatchString(value)
}

// check applies the schema's bounds, lengths and patterns to a converted value.
func (p Property) check(v any) error {
	flag := FlagName(p.Name)
	if n, ok := asFloat(v); ok {
		if p.Minimum != nil && n < *p.Minimum {
			return fmt.Errorf("--%s must be at least %v", flag, *p.Minimum)
		}
		if p.Maximum != nil && n > *p.Maximum {
			return fmt.Errorf("--%s must be at most %v", flag, *p.Maximum)
		}
	}
	if text, ok := v.(string); ok && p.ContentEncoding == "" {
		if p.MinLength != nil && len([]rune(strings.TrimSpace(text))) < *p.MinLength {
			if *p.MinLength <= 1 {
				return fmt.Errorf("--%s must not be empty", flag)
			}
			return fmt.Errorf("--%s must be at least %d characters", flag, *p.MinLength)
		}
		if p.MaxLength != nil && len([]rune(text)) > *p.MaxLength {
			return fmt.Errorf("--%s must be at most %d characters", flag, *p.MaxLength)
		}
		if p.Pattern != "" && !matches(p.Pattern, text) {
			return fmt.Errorf("--%s %q is not in the expected form (%s)", flag, text, p.Description)
		}
	}
	if items, ok := v.([]any); ok && p.ItemsPattern != "" {
		for _, item := range items {
			if text, ok := item.(string); ok && !matches(p.ItemsPattern, text) {
				return fmt.Errorf("--%s item %q is not in the expected form (%s)", flag, text, p.Description)
			}
		}
	}
	return nil
}

func parseJSONArg(name, value string, stdin io.Reader) (any, error) {
	data := []byte(value)
	if strings.HasPrefix(value, "@") {
		var err error
		if data, err = readFile(value[1:], stdin); err != nil {
			return nil, fmt.Errorf("--%s: %v", FlagName(name), err)
		}
	}
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("--%s takes JSON (or @file.json): %v", FlagName(name), err)
	}
	return parsed, nil
}

func inEnum(enum []any, value any) bool {
	for _, e := range enum {
		if fmt.Sprint(e) == fmt.Sprint(value) {
			return true
		}
	}
	return false
}

func enumText(enum []any) string {
	parts := make([]string, len(enum))
	for i, e := range enum {
		parts[i] = fmt.Sprint(e)
	}
	return strings.Join(parts, ", ")
}

// FillFileFields names an uploaded file after its path when the tool has
// filename / content_type arguments the person left out.
func (s Schema) FillFileFields(args map[string]any, filePaths map[string]string) {
	for name, path := range filePaths {
		if s.Properties[name].ContentEncoding != "base64" || path == "-" {
			continue
		}
		if _, ok := s.Properties["filename"]; ok && args["filename"] == nil {
			args["filename"] = filepath.Base(path)
		}
		if _, ok := s.Properties["content_type"]; ok && args["content_type"] == nil {
			if kind := mime.TypeByExtension(filepath.Ext(path)); kind != "" {
				args["content_type"] = strings.Split(kind, ";")[0]
			}
		}
	}
}

// Missing are the required arguments args lacks.
func (s Schema) Missing(args map[string]any) []string {
	var missing []string
	for _, name := range s.Required {
		if _, ok := args[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}
