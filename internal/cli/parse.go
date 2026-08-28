package cli

import (
	"fmt"
	"strings"
)

// flags carries the parsed and validated arguments of one command. values are
// flag values by long name, bools are present boolean flags, and args are the
// positional arguments. Parsing is strict: unknown flags fail, required flags
// are enforced, extra positionals fail, obsolete flags fail with a hint, and
// --name=value is accepted wherever --name value is. Duplicate flags fail.
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
//
// Flag syntax is explicit and strict:
//
//	--long          long boolean flag
//	--long=value    long flag with inline value or explicit boolean
//	-x              single-letter short alias, only when declared as short
//
// A single-dash token with more than one character (-json, -name, -scope) is
// never a long flag and is rejected. A value flag whose value token is another
// flag fails as "requires a value" instead of consuming the flag.
func parseCommand(spec *commandSpec, args []string) (flags, error) {
	f := flags{values: map[string]string{}, bools: map[string]bool{}}
	if spec.isGroup() {
		return parseCommandGroup(spec, args)
	}
	seenBools := map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			f.args = append(f.args, arg)
			continue
		}
		if strings.HasPrefix(arg, "--") {
			body := arg[2:]
			if body == "" {
				return flags{}, unknownFlagError(spec, arg)
			}
			name, inline, hasInline := strings.Cut(body, "=")
			if name == "" {
				return flags{}, unknownFlagError(spec, arg)
			}
			fs := spec.longFlag(name)
			if fs == nil {
				return flags{}, unknownFlagError(spec, "--"+name)
			}
			if fs.obsolete != "" {
				return flags{}, obsoleteFlagError("--"+name, fs.obsolete)
			}
			if fs.value {
				if hasInline {
					if _, dup := f.values[fs.name]; dup {
						return flags{}, duplicateFlagError(spec, "--"+name)
					}
					f.values[fs.name] = inline
					continue
				}
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
					return flags{}, fmt.Errorf("invalid_input: %s: --%s requires a value; see '%s --help'", spec.id(), fs.name, spec.id())
				}
				i++
				if _, dup := f.values[fs.name]; dup {
					return flags{}, duplicateFlagError(spec, "--"+name)
				}
				f.values[fs.name] = args[i]
				continue
			}
			// Boolean long flag.
			if hasInline {
				switch inline {
				case "true":
					f.bools[fs.name] = true
				case "false":
					f.bools[fs.name] = false
				default:
					return flags{}, fmt.Errorf("invalid_input: %s: --%s expects true or false; see '%s --help'", spec.id(), fs.name, spec.id())
				}
			} else {
				f.bools[fs.name] = true
			}
			if seenBools[fs.name] {
				return flags{}, duplicateFlagError(spec, "--"+name)
			}
			seenBools[fs.name] = true
			continue
		}
		// Short flag: single dash, exactly one character, only when that
		// character is a declared short alias.
		body := arg[1:]
		if len(body) != 1 {
			return flags{}, unknownFlagError(spec, arg)
		}
		fs := spec.shortFlag(body)
		if fs == nil {
			return flags{}, unknownFlagError(spec, arg)
		}
		if fs.obsolete != "" {
			return flags{}, obsoleteFlagError("-"+body, fs.obsolete)
		}
		if fs.value {
			return flags{}, fmt.Errorf("invalid_input: %s: short flag -%s does not take a value; use --%s; see '%s --help'", spec.id(), body, fs.name, spec.id())
		}
		if seenBools[fs.name] {
			return flags{}, duplicateFlagError(spec, "-"+body)
		}
		seenBools[fs.name] = true
		f.bools[fs.name] = true
	}
	if err := enforceRequired(spec, f); err != nil {
		return flags{}, err
	}
	if len(f.args) < spec.minArgs || (spec.maxArgs >= 0 && len(f.args) > spec.maxArgs) {
		return flags{}, positionalError(spec, f.args)
	}
	return f, nil
}

// parseCommandGroup validates a group invocation: after a group path is
// resolved, no flags or positionals may remain (subcommand required), and any
// flag token is a misplacement error.
func parseCommandGroup(spec *commandSpec, args []string) (flags, error) {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return flags{}, fmt.Errorf("invalid_input: %s: flag %s before subcommand; see '%s --help'", spec.id(), arg, spec.id())
		}
		return flags{}, fmt.Errorf("invalid_input: %s: unknown subcommand %q; see '%s --help'", spec.id(), arg, spec.id())
	}
	return flags{}, fmt.Errorf("invalid_input: %s requires a subcommand; see '%s --help'", spec.id(), spec.id())
}

// enforceRequired checks that every required value flag is present and
// non-empty (--name= is rejected rather than silently accepted).
func enforceRequired(spec *commandSpec, f flags) error {
	for i := range spec.flags {
		fs := &spec.flags[i]
		if !fs.required {
			continue
		}
		if value, ok := f.values[fs.name]; !ok || value == "" {
			return fmt.Errorf("invalid_input: %s: --%s is required; see '%s --help'", spec.id(), fs.name, spec.id())
		}
	}
	return nil
}

func unknownFlagError(spec *commandSpec, flagToken string) error {
	return fmt.Errorf("invalid_input: %s: unknown flag %s; see '%s --help'", spec.id(), flagToken, spec.id())
}

func obsoleteFlagError(flagToken, hint string) error {
	return fmt.Errorf("invalid_input: %s is obsolete; %s", flagToken, hint)
}

func duplicateFlagError(spec *commandSpec, flagToken string) error {
	return fmt.Errorf("invalid_input: %s: duplicate flag %s", spec.id(), flagToken)
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
