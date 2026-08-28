package cli

import (
	"fmt"
	"strings"
)

// flags carries the parsed and validated arguments of one command. values are
// flag values by long name, bools are present boolean flags, and args are the
// positional arguments. Parsing is strict: unknown flags fail, required flags
// are enforced, extra positionals fail, obsolete flags fail with a hint, and
// --name=value is accepted wherever --name value is.
type flags struct {
	values map[string]string
	bools  map[string]bool
	args   []string
}

// required returns the value of a required value flag. The parser already
// enforces required flags declared in the spec; this is a defensive backstop
// for handlers that are also invoked directly by tests.
func (f flags) required(name string) (string, error) {
	value := f.values[name]
	if value == "" {
		return "", fmt.Errorf("--%s is required", name)
	}
	return value, nil
}

func (f flags) bool(name string) bool { return f.bools[name] }

// parseCommand validates args against a command spec and returns the parsed
// flags. It is the one place flag spelling, requiredness, obsolete-flag hints,
// duplicate rejection, and positional-arity checks are enforced.
func parseCommand(spec *commandSpec, args []string) (flags, error) {
	f := flags{values: map[string]string{}, bools: map[string]bool{}}
	if spec.isGroup() {
		for _, arg := range args {
			if strings.HasPrefix(arg, "-") {
				name, _, _ := strings.Cut(arg, "=")
				if spec.flag(strings.TrimLeft(name, "-")) == nil {
					return flags{}, fmt.Errorf("invalid_input: %s: unknown flag %s; see '%s --help'", spec.id(), name, spec.id())
				}
				continue
			}
			return flags{}, fmt.Errorf("invalid_input: %s: unknown subcommand %q; see '%s --help'", spec.id(), arg, spec.id())
		}
		return flags{}, fmt.Errorf("invalid_input: %s requires a subcommand; see '%s --help'", spec.id(), spec.id())
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			f.args = append(f.args, arg)
			continue
		}
		name, inline, hasInline := strings.Cut(arg, "=")
		fs := spec.flag(strings.TrimLeft(name, "-"))
		if fs == nil {
			return flags{}, fmt.Errorf("invalid_input: %s: unknown flag %s; see '%s --help'", spec.id(), name, spec.id())
		}
		if fs.obsolete != "" {
			return flags{}, fmt.Errorf("invalid_input: %s is obsolete; %s", name, fs.obsolete)
		}
		if fs.value {
			value := inline
			if !hasInline {
				if i+1 >= len(args) {
					return flags{}, fmt.Errorf("invalid_input: %s: %s requires a value; see '%s --help'", spec.id(), name, spec.id())
				}
				i++
				value = args[i]
			}
			if _, dup := f.values[fs.name]; dup {
				return flags{}, fmt.Errorf("invalid_input: %s: duplicate flag %s", spec.id(), name)
			}
			f.values[fs.name] = value
			continue
		}
		if hasInline {
			switch inline {
			case "true":
				f.bools[fs.name] = true
			case "false":
				f.bools[fs.name] = false
			default:
				return flags{}, fmt.Errorf("invalid_input: %s: %s expects true or false", spec.id(), name)
			}
			continue
		}
		f.bools[fs.name] = true
	}
	for i := range spec.flags {
		fs := &spec.flags[i]
		if fs.required {
			if _, ok := f.values[fs.name]; !ok {
				return flags{}, fmt.Errorf("invalid_input: %s: --%s is required; see '%s --help'", spec.id(), fs.name, spec.id())
			}
		}
	}
	if len(f.args) < spec.minArgs || (spec.maxArgs >= 0 && len(f.args) > spec.maxArgs) {
		return flags{}, positionalError(spec, f.args)
	}
	requiredEmpty := false
	for i := range spec.flags {
		if spec.flags[i].required {
			if value, ok := f.values[spec.flags[i].name]; !ok || value == "" {
				requiredEmpty = true
			}
		}
	}
	if requiredEmpty {
		return flags{}, fmt.Errorf("invalid_input: %s: --%s is required; see '%s --help'", spec.id(), requiredFlagName(spec), spec.id())
	}
	return f, nil
}

// requiredFlagName returns the first missing required flag name, for error
// messaging; required flags are enforced by the requiredEmpty pass above.
func requiredFlagName(spec *commandSpec) string {
	for i := range spec.flags {
		if spec.flags[i].required {
			return spec.flags[i].name
		}
	}
	return ""
}

// positionalError gives a concrete missing/unexpected-argument message.
func positionalError(spec *commandSpec, got []string) error {
	if len(got) < spec.minArgs {
		missing := "argument"
		if spec.maxArgs != 0 && len(spec.args) > 0 {
			missing = strings.Join(spec.args[len(got):], " ")
		}
		return fmt.Errorf("invalid_input: %s: missing %s; see '%s --help'", spec.id(), missing, spec.id())
	}
	return fmt.Errorf("invalid_input: %s: unexpected argument %q; see '%s --help'", spec.id(), got[spec.maxArgs], spec.id())
}
