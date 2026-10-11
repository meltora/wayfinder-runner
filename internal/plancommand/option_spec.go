package plancommand

import (
	"fmt"
	"strings"
)

type longOptionKind int

const (
	longFlag longOptionKind = iota
	longValue
	longOptionalValue
)

type optionSpec struct {
	program       string
	shortFlags    string
	shortValues   string
	shortOptional string
	numeric       bool
	clusters      bool
	long          map[string]longOptionKind
}

func longOptions(flags, values, optional string) map[string]longOptionKind {
	options := map[string]longOptionKind{}
	for kind, names := range map[longOptionKind]string{longFlag: flags, longValue: values, longOptionalValue: optional} {
		for _, name := range strings.Fields(names) {
			options[name] = kind
		}
	}
	return options
}

func (s optionSpec) pathArguments(args []string) ([]string, error) {
	var paths []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return append(paths, args[i+1:]...), nil
		case strings.HasPrefix(arg, "--"):
			value, err := s.longArgument(arg)
			if err != nil {
				return nil, err
			}
			paths = append(paths, value)
		case len(arg) > 1 && arg[0] == '-':
			value, takesNext, err := s.shortArgument(arg)
			if err != nil {
				return nil, err
			}
			if takesNext {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("%s option %s requires a value", s.program, arg)
				}
				i++
				value = args[i]
			}
			paths = append(paths, value)
		default:
			paths = append(paths, arg)
		}
	}
	return paths, nil
}

func (s optionSpec) longArgument(arg string) (string, error) {
	name, value, hasValue := strings.Cut(arg[2:], "=")
	kind, ok := s.long[name]
	switch {
	case !ok:
		return "", fmt.Errorf("%s option --%s is not allowed in planning jobs (only exact, listed long options are accepted)", s.program, name)
	case kind == longFlag && hasValue:
		return "", fmt.Errorf("%s option --%s does not take a value", s.program, name)
	case kind == longValue && !hasValue:
		return "", fmt.Errorf("%s option --%s must be written as --%s=value", s.program, name, name)
	}
	return value, nil
}

func (s optionSpec) shortArgument(arg string) (string, bool, error) {
	if s.numeric && isDigits(arg[1:]) {
		return "", false, nil
	}
	for j := 1; j < len(arg); j++ {
		flag := arg[j]
		rest := arg[j+1:]
		switch {
		case strings.IndexByte(s.shortValues, flag) >= 0:
			return rest, rest == "", nil
		case strings.IndexByte(s.shortOptional, flag) >= 0:
			return rest, false, nil
		case strings.IndexByte(s.shortFlags, flag) < 0:
			return "", false, fmt.Errorf("%s option -%c is not allowed in planning jobs", s.program, flag)
		case !s.clusters && rest != "":
			return "", false, fmt.Errorf("%s option %s is not allowed in planning jobs (combine no short options)", s.program, arg)
		}
	}
	return "", false, nil
}

func isDigits(text string) bool {
	if text == "" {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}
	return true
}
