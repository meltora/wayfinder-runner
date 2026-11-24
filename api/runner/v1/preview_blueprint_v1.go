package v1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	PreviewBlueprintContentSchemaVersionV1 = 1
	previewBlueprintMaxSafeInteger         = 1<<53 - 1
)

type PreviewBlueprintEnvKindV1 string

const (
	PreviewBlueprintEnvGenerated PreviewBlueprintEnvKindV1 = "generated"
	PreviewBlueprintEnvSecret    PreviewBlueprintEnvKindV1 = "secret"
	PreviewBlueprintEnvFake      PreviewBlueprintEnvKindV1 = "fake"
	PreviewBlueprintEnvDisabled  PreviewBlueprintEnvKindV1 = "disabled"
)

type PreviewBlueprintEgressModeV1 string

const (
	PreviewBlueprintEgressNone  PreviewBlueprintEgressModeV1 = "none"
	PreviewBlueprintEgressAllow PreviewBlueprintEgressModeV1 = "allow"
)

type PreviewBlueprintContentV1 struct {
	SchemaVersion int                         `json:"schema_version"`
	Services      []PreviewBlueprintServiceV1 `json:"services"`
	App           PreviewBlueprintAppV1       `json:"app"`
	Limits        PreviewBlueprintLimitsV1    `json:"limits"`
	Egress        PreviewBlueprintEgressV1    `json:"egress"`
}

type PreviewBlueprintServiceV1 struct {
	Name   string                                `json:"name"`
	Role   string                                `json:"role"`
	Image  string                                `json:"image"`
	Env    map[string]PreviewBlueprintEnvEntryV1 `json:"env,omitempty"`
	Health *PreviewBlueprintServiceHealthV1      `json:"health,omitempty"`
}

type PreviewBlueprintServiceHealthV1 struct {
	Command         []string `json:"command"`
	IntervalSeconds int      `json:"interval_seconds"`
}

type PreviewBlueprintEnvEntryV1 struct {
	Kind      PreviewBlueprintEnvKindV1 `json:"kind"`
	Generator string                    `json:"generator,omitempty"`
	Note      *string                   `json:"note,omitempty"`
	Value     string                    `json:"value,omitempty"`
}

type PreviewBlueprintAppV1 struct {
	Image            string                                `json:"image"`
	WorkingDirectory string                                `json:"working_directory"`
	Install          []string                              `json:"install"`
	Build            []string                              `json:"build"`
	Migrate          []string                              `json:"migrate"`
	Seed             []string                              `json:"seed"`
	Start            string                                `json:"start"`
	Port             int                                   `json:"port"`
	HealthChecks     []PreviewBlueprintHealthCheckV1       `json:"health_checks"`
	Env              map[string]PreviewBlueprintEnvEntryV1 `json:"env"`
}

type PreviewBlueprintHealthCheckV1 struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	ExpectedStatus int    `json:"expected_status"`
}

type PreviewBlueprintLimitsV1 struct {
	MemoryMB           int     `json:"memory_mb"`
	CPUs               float64 `json:"cpus"`
	MaxLifetimeMinutes int     `json:"max_lifetime_minutes"`
}

type PreviewBlueprintEgressV1 struct {
	Mode  PreviewBlueprintEgressModeV1 `json:"mode"`
	Hosts []string                     `json:"hosts,omitempty"`
}

type PreviewBlueprintIssueV1 struct {
	Path    []string
	Message string
}

type PreviewBlueprintContentErrorV1 struct {
	Issues []PreviewBlueprintIssueV1
}

func (err *PreviewBlueprintContentErrorV1) Error() string {
	parts := make([]string, len(err.Issues))
	for index, issue := range err.Issues {
		path := strings.Join(issue.Path, ".")
		if path == "" {
			path = "(root)"
		}
		parts[index] = path + ": " + issue.Message
	}
	return "preview blueprint content is invalid: " + strings.Join(parts, "; ")
}

func (err *PreviewBlueprintContentErrorV1) Paths() [][]string {
	paths := make([][]string, len(err.Issues))
	for index, issue := range err.Issues {
		paths[index] = append([]string{}, issue.Path...)
	}
	return paths
}

func ParsePreviewBlueprintContentV1(raw []byte) (PreviewBlueprintContentV1, error) {
	value, err := decodePreviewBlueprintJSON(raw)
	if err != nil {
		return PreviewBlueprintContentV1{}, err
	}
	return structurePreviewBlueprintContent(value)
}

func PreviewBlueprintCanonicalJSONV1(raw []byte) ([]byte, error) {
	value, err := decodePreviewBlueprintJSON(raw)
	if err != nil {
		return nil, err
	}
	if _, err := structurePreviewBlueprintContent(value); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writePreviewBlueprintCanonicalJSON(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func PreviewBlueprintDigestV1(raw []byte) (string, error) {
	canonical, err := PreviewBlueprintCanonicalJSONV1(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func decodePreviewBlueprintJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("preview blueprint content is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	reader := &previewBlueprintTokenReader{raw: raw, decoder: decoder}
	value, err := reader.value(0)
	if err != nil {
		return nil, fmt.Errorf("preview blueprint content is not valid JSON: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("preview blueprint content has data after the JSON value")
	}
	if err := refuseNonFiniteNumbers(value); err != nil {
		return nil, err
	}
	return value, nil
}

const previewBlueprintMaxDepth = 10000

type previewBlueprintTokenReader struct {
	raw     []byte
	decoder *json.Decoder
}

func (reader *previewBlueprintTokenReader) token() (json.Token, error) {
	start := reader.decoder.InputOffset()
	token, err := reader.decoder.Token()
	if err != nil {
		return nil, err
	}
	if _, ok := token.(string); ok {
		span := reader.raw[start:reader.decoder.InputOffset()]
		quote := bytes.IndexByte(span, '"')
		if quote < 0 || len(span)-quote < 2 || span[len(span)-1] != '"' {
			return nil, errors.New("string token is not a JSON string literal")
		}
		return decodePreviewBlueprintStringLiteral(span[quote+1 : len(span)-1]), nil
	}
	return token, nil
}

func (reader *previewBlueprintTokenReader) value(depth int) (any, error) {
	if depth > previewBlueprintMaxDepth {
		return nil, errors.New("exceeded max depth")
	}
	token, err := reader.token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '[':
		items := []any{}
		for reader.decoder.More() {
			item, err := reader.value(depth + 1)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		if _, err := reader.decoder.Token(); err != nil {
			return nil, err
		}
		return items, nil
	case '{':
		object := map[string]any{}
		for reader.decoder.More() {
			keyToken, err := reader.token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("object key is not a string")
			}
			item, err := reader.value(depth + 1)
			if err != nil {
				return nil, err
			}
			object[key] = item
		}
		if _, err := reader.decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	}
	return nil, fmt.Errorf("unexpected delimiter %v", delim)
}

func decodePreviewBlueprintStringLiteral(body []byte) string {
	out := make([]byte, 0, len(body))
	for index := 0; index < len(body); {
		if body[index] != '\\' || index+1 >= len(body) {
			out = append(out, body[index])
			index++
			continue
		}
		escape := body[index+1]
		index += 2
		switch escape {
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			unit, ok := previewBlueprintHexUnit(body, index)
			if !ok {
				out = append(out, '\\', 'u')
				continue
			}
			index += 4
			if unit >= 0xD800 && unit < 0xDC00 && index+1 < len(body) && body[index] == '\\' && body[index+1] == 'u' {
				if low, ok := previewBlueprintHexUnit(body, index+2); ok && low >= 0xDC00 && low <= 0xDFFF {
					out = utf8.AppendRune(out, utf16.DecodeRune(unit, low))
					index += 6
					continue
				}
			}
			out = appendPreviewBlueprintCodePoint(out, unit)
		default:
			out = append(out, escape)
		}
	}
	return string(out)
}

func previewBlueprintHexUnit(body []byte, start int) (rune, bool) {
	if start+4 > len(body) {
		return 0, false
	}
	unit, err := strconv.ParseUint(string(body[start:start+4]), 16, 16)
	if err != nil {
		return 0, false
	}
	return rune(unit), true
}

func appendPreviewBlueprintCodePoint(out []byte, codePoint rune) []byte {
	if codePoint >= 0xD800 && codePoint <= 0xDFFF {
		return append(out, byte(0xE0|codePoint>>12), byte(0x80|(codePoint>>6)&0x3F), byte(0x80|codePoint&0x3F))
	}
	return utf8.AppendRune(out, codePoint)
}

func PreviewBlueprintTextCodePoints(value string) []rune {
	codePoints := make([]rune, 0, len(value))
	for index := 0; index < len(value); {
		if surrogate, ok := previewBlueprintSurrogateAt(value, index); ok {
			codePoints = append(codePoints, surrogate)
			index += 3
			continue
		}
		character, size := utf8.DecodeRuneInString(value[index:])
		codePoints = append(codePoints, character)
		index += size
	}
	return codePoints
}

func previewBlueprintSurrogateAt(value string, index int) (rune, bool) {
	if index+3 > len(value) || value[index] != 0xED || value[index+1] < 0xA0 || value[index+1] > 0xBF || value[index+2] < 0x80 || value[index+2] > 0xBF {
		return 0, false
	}
	return 0xD000 | rune(value[index+1]&0x3F)<<6 | rune(value[index+2]&0x3F), true
}

func previewBlueprintUTF16(value string) []uint16 {
	var units []uint16
	for _, codePoint := range PreviewBlueprintTextCodePoints(value) {
		if codePoint >= 0x10000 {
			high, low := utf16.EncodeRune(codePoint)
			units = append(units, uint16(high), uint16(low))
			continue
		}
		units = append(units, uint16(codePoint))
	}
	return units
}

func refuseNonFiniteNumbers(value any) error {
	switch typed := value.(type) {
	case json.Number:
		if _, err := previewBlueprintNumber(typed); err != nil {
			return err
		}
	case []any:
		for _, item := range typed {
			if err := refuseNonFiniteNumbers(item); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, item := range typed {
			if err := refuseNonFiniteNumbers(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func previewBlueprintNumber(number json.Number) (float64, error) {
	value, err := strconv.ParseFloat(string(number), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, fmt.Errorf("preview blueprint number %q is invalid", string(number))
	}
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, fmt.Errorf("preview blueprint number %q is not finite", string(number))
	}
	return value, nil
}

func writePreviewBlueprintCanonicalJSON(out io.StringWriter, value any) error {
	var err error
	write := func(text string) {
		if err == nil {
			_, err = out.WriteString(text)
		}
	}
	switch typed := value.(type) {
	case nil:
		write("null")
	case bool:
		write(strconv.FormatBool(typed))
	case string:
		write(ecmaScriptJSONString(typed))
	case json.Number:
		number, numberErr := previewBlueprintNumber(typed)
		if numberErr != nil {
			return numberErr
		}
		write(ecmaScriptNumberString(number))
	case []any:
		write("[")
		for index, item := range typed {
			if index > 0 {
				write(",")
			}
			if err == nil {
				err = writePreviewBlueprintCanonicalJSON(out, item)
			}
		}
		write("]")
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		slices.SortFunc(keys, compareUTF16CodeUnits)
		write("{")
		for index, key := range keys {
			if index > 0 {
				write(",")
			}
			write(ecmaScriptJSONString(key))
			write(":")
			if err == nil {
				err = writePreviewBlueprintCanonicalJSON(out, typed[key])
			}
		}
		write("}")
	default:
		return fmt.Errorf("preview blueprint content has an unsupported %T value", value)
	}
	return err
}

func compareUTF16CodeUnits(left, right string) int {
	return slices.Compare(previewBlueprintUTF16(left), previewBlueprintUTF16(right))
}

func ecmaScriptJSONString(value string) string {
	var out strings.Builder
	out.Grow(len(value) + 2)
	out.WriteByte('"')
	for _, character := range PreviewBlueprintTextCodePoints(value) {
		switch character {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if character < 0x20 || (character >= 0xD800 && character <= 0xDFFF) {
				fmt.Fprintf(&out, `\u%04x`, character)
				continue
			}
			out.WriteRune(character)
		}
	}
	out.WriteByte('"')
	return out.String()
}

func ecmaScriptNumberString(value float64) string {
	if value == 0 {
		return "0"
	}
	if value < 0 {
		return "-" + ecmaScriptNumberString(-value)
	}
	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	mantissa, exponentText, _ := strings.Cut(scientific, "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	exponent, _ := strconv.Atoi(exponentText)
	k := len(digits)
	n := exponent + 1
	switch {
	case k <= n && n <= 21:
		return digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return "0." + strings.Repeat("0", -n) + digits
	}
	sign := "+"
	if n-1 < 0 {
		sign = "-"
	}
	magnitude := strconv.Itoa(n - 1)
	if n-1 < 0 {
		magnitude = strconv.Itoa(1 - n)
	}
	if k == 1 {
		return digits + "e" + sign + magnitude
	}
	return digits[:1] + "." + digits[1:] + "e" + sign + magnitude
}

type previewBlueprintStructure struct {
	issues []PreviewBlueprintIssueV1
}

func (structure *previewBlueprintStructure) add(path []string, message string) {
	structure.issues = append(structure.issues, PreviewBlueprintIssueV1{Path: slices.Clone(path), Message: message})
}

func childPath(path []string, segment string) []string {
	return append(slices.Clone(path), segment)
}

func structurePreviewBlueprintContent(value any) (PreviewBlueprintContentV1, error) {
	structure := &previewBlueprintStructure{}
	var content PreviewBlueprintContentV1
	fields, ok := structure.object(nil, value, []string{"schema_version", "services", "app", "limits", "egress"}, nil)
	if ok {
		content.SchemaVersion, _ = structure.integer(childPath(nil, "schema_version"), fields["schema_version"])
		content.Services = structure.services(childPath(nil, "services"), fields["services"])
		content.App = structure.app(childPath(nil, "app"), fields["app"])
		content.Limits = structure.limits(childPath(nil, "limits"), fields["limits"])
		content.Egress = structure.egress(childPath(nil, "egress"), fields["egress"])
	}
	if len(structure.issues) > 0 {
		return PreviewBlueprintContentV1{}, &PreviewBlueprintContentErrorV1{Issues: structure.issues}
	}
	return content, nil
}

type previewBlueprintAbsent struct{}

func isPreviewBlueprintAbsent(value any) bool {
	_, absent := value.(previewBlueprintAbsent)
	return absent
}

type previewBlueprintFields map[string]any

func (fields previewBlueprintFields) has(key string) bool {
	value, ok := fields[key]
	return ok && !isPreviewBlueprintAbsent(value)
}

func (structure *previewBlueprintStructure) object(path []string, value any, required, optional []string) (previewBlueprintFields, bool) {
	if isPreviewBlueprintAbsent(value) {
		return nil, false
	}
	object, ok := value.(map[string]any)
	if !ok {
		structure.add(path, "Expected an object")
		return nil, false
	}
	fields := make(previewBlueprintFields, len(object))
	for key, item := range object {
		fields[key] = item
	}
	known := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		known[key] = true
		if _, present := object[key]; !present {
			structure.add(childPath(path, key), "Required")
			fields[key] = previewBlueprintAbsent{}
		}
	}
	for _, key := range optional {
		known[key] = true
	}
	var unknown []string
	for key := range object {
		if !known[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		slices.SortFunc(unknown, compareUTF16CodeUnits)
		structure.add(path, "Unrecognized keys: "+strings.Join(unknown, ", "))
	}
	return fields, true
}

func (structure *previewBlueprintStructure) str(path []string, value any) string {
	text, ok := value.(string)
	if !ok && !isPreviewBlueprintAbsent(value) {
		structure.add(path, "Expected a string")
	}
	return text
}

func (structure *previewBlueprintStructure) float(path []string, value any) (float64, bool) {
	if isPreviewBlueprintAbsent(value) {
		return 0, false
	}
	number, ok := value.(json.Number)
	if !ok {
		structure.add(path, "Expected a number")
		return 0, false
	}
	parsed, err := previewBlueprintNumber(number)
	if err != nil {
		structure.add(path, "Expected a finite number")
		return 0, false
	}
	return parsed, true
}

func (structure *previewBlueprintStructure) integer(path []string, value any) (int, bool) {
	number, ok := structure.float(path, value)
	if !ok {
		return 0, false
	}
	if math.Trunc(number) != number {
		structure.add(path, "Expected an integer")
		return 0, false
	}
	if math.Abs(number) > previewBlueprintMaxSafeInteger {
		structure.add(path, "Integers must be within the safe integer range")
		return 0, false
	}
	return int(number), true
}

func (structure *previewBlueprintStructure) stringList(path []string, value any) []string {
	if isPreviewBlueprintAbsent(value) {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		structure.add(path, "Expected an array")
		return nil
	}
	result := make([]string, len(items))
	for index, item := range items {
		result[index] = structure.str(childPath(path, strconv.Itoa(index)), item)
	}
	return result
}

func (structure *previewBlueprintStructure) services(path []string, value any) []PreviewBlueprintServiceV1 {
	if isPreviewBlueprintAbsent(value) {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		structure.add(path, "Expected an array")
		return nil
	}
	services := make([]PreviewBlueprintServiceV1, len(items))
	for index, item := range items {
		itemPath := childPath(path, strconv.Itoa(index))
		fields, ok := structure.object(itemPath, item, []string{"name", "role", "image"}, []string{"env", "health"})
		if !ok {
			continue
		}
		service := &services[index]
		service.Name = structure.str(childPath(itemPath, "name"), fields["name"])
		service.Role = structure.str(childPath(itemPath, "role"), fields["role"])
		service.Image = structure.str(childPath(itemPath, "image"), fields["image"])
		if fields.has("env") {
			service.Env = structure.env(childPath(itemPath, "env"), fields["env"])
		}
		if fields.has("health") {
			healthPath := childPath(itemPath, "health")
			healthFields, ok := structure.object(healthPath, fields["health"], []string{"command", "interval_seconds"}, nil)
			if ok {
				health := &PreviewBlueprintServiceHealthV1{}
				health.Command = structure.stringList(childPath(healthPath, "command"), healthFields["command"])
				health.IntervalSeconds, _ = structure.integer(childPath(healthPath, "interval_seconds"), healthFields["interval_seconds"])
				service.Health = health
			}
		}
	}
	return services
}

func (structure *previewBlueprintStructure) env(path []string, value any) map[string]PreviewBlueprintEnvEntryV1 {
	if isPreviewBlueprintAbsent(value) {
		return nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		structure.add(path, "Expected an object")
		return nil
	}
	for _, reserved := range []string{"__proto__", "constructor", "prototype"} {
		if _, present := object[reserved]; present {
			structure.add(path, "Invalid environment key")
			return nil
		}
	}
	env := make(map[string]PreviewBlueprintEnvEntryV1, len(object))
	for key, item := range object {
		entryPath := childPath(path, key)
		entryObject, ok := item.(map[string]any)
		if !ok {
			structure.add(entryPath, "Expected an object")
			continue
		}
		kindPath := childPath(entryPath, "kind")
		kind, _ := entryObject["kind"].(string)
		entry := PreviewBlueprintEnvEntryV1{Kind: PreviewBlueprintEnvKindV1(kind)}
		switch entry.Kind {
		case PreviewBlueprintEnvGenerated:
			fields, _ := structure.object(entryPath, item, []string{"kind", "generator"}, nil)
			entry.Generator = structure.str(childPath(entryPath, "generator"), fields["generator"])
		case PreviewBlueprintEnvFake:
			fields, _ := structure.object(entryPath, item, []string{"kind", "value"}, nil)
			entry.Value = structure.str(childPath(entryPath, "value"), fields["value"])
		case PreviewBlueprintEnvSecret, PreviewBlueprintEnvDisabled:
			fields, _ := structure.object(entryPath, item, []string{"kind"}, []string{"note"})
			if fields.has("note") {
				note := structure.str(childPath(entryPath, "note"), fields["note"])
				entry.Note = &note
			}
		default:
			structure.add(kindPath, "Invalid env kind: expected generated, secret, fake or disabled")
			continue
		}
		env[key] = entry
	}
	return env
}

func (structure *previewBlueprintStructure) app(path []string, value any) PreviewBlueprintAppV1 {
	var app PreviewBlueprintAppV1
	fields, ok := structure.object(path, value, []string{"image", "working_directory", "install", "build", "migrate", "seed", "start", "port", "health_checks", "env"}, nil)
	if !ok {
		return app
	}
	app.Image = structure.str(childPath(path, "image"), fields["image"])
	app.WorkingDirectory = structure.str(childPath(path, "working_directory"), fields["working_directory"])
	app.Install = structure.stringList(childPath(path, "install"), fields["install"])
	app.Build = structure.stringList(childPath(path, "build"), fields["build"])
	app.Migrate = structure.stringList(childPath(path, "migrate"), fields["migrate"])
	app.Seed = structure.stringList(childPath(path, "seed"), fields["seed"])
	app.Start = structure.str(childPath(path, "start"), fields["start"])
	app.Port, _ = structure.integer(childPath(path, "port"), fields["port"])
	checksPath := childPath(path, "health_checks")
	if items, ok := fields["health_checks"].([]any); ok {
		app.HealthChecks = make([]PreviewBlueprintHealthCheckV1, len(items))
		for index, item := range items {
			itemPath := childPath(checksPath, strconv.Itoa(index))
			checkFields, ok := structure.object(itemPath, item, []string{"id", "path", "expected_status"}, nil)
			if !ok {
				continue
			}
			check := &app.HealthChecks[index]
			check.ID = structure.str(childPath(itemPath, "id"), checkFields["id"])
			check.Path = structure.str(childPath(itemPath, "path"), checkFields["path"])
			check.ExpectedStatus, _ = structure.integer(childPath(itemPath, "expected_status"), checkFields["expected_status"])
		}
	} else if fields.has("health_checks") {
		structure.add(checksPath, "Expected an array")
	}
	if fields.has("env") {
		app.Env = structure.env(childPath(path, "env"), fields["env"])
	}
	return app
}

func (structure *previewBlueprintStructure) limits(path []string, value any) PreviewBlueprintLimitsV1 {
	var limits PreviewBlueprintLimitsV1
	fields, ok := structure.object(path, value, []string{"memory_mb", "cpus", "max_lifetime_minutes"}, nil)
	if !ok {
		return limits
	}
	limits.MemoryMB, _ = structure.integer(childPath(path, "memory_mb"), fields["memory_mb"])
	limits.CPUs, _ = structure.float(childPath(path, "cpus"), fields["cpus"])
	limits.MaxLifetimeMinutes, _ = structure.integer(childPath(path, "max_lifetime_minutes"), fields["max_lifetime_minutes"])
	return limits
}

func (structure *previewBlueprintStructure) egress(path []string, value any) PreviewBlueprintEgressV1 {
	var egress PreviewBlueprintEgressV1
	if isPreviewBlueprintAbsent(value) {
		return egress
	}
	object, ok := value.(map[string]any)
	if !ok {
		structure.add(path, "Expected an object")
		return egress
	}
	mode, _ := object["mode"].(string)
	egress.Mode = PreviewBlueprintEgressModeV1(mode)
	switch egress.Mode {
	case PreviewBlueprintEgressNone:
		structure.object(path, value, []string{"mode"}, nil)
	case PreviewBlueprintEgressAllow:
		fields, _ := structure.object(path, value, []string{"mode", "hosts"}, nil)
		if fields.has("hosts") {
			egress.Hosts = structure.stringList(childPath(path, "hosts"), fields["hosts"])
		}
	default:
		structure.add(childPath(path, "mode"), "Invalid egress mode: expected none or allow")
	}
	return egress
}
