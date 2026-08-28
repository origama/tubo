package cli

import (
	"fmt"
	"sort"
	"strings"
)

// flagSpec declares one flag a command accepts. Flags are the only accepted
// flag forms: every other --flag / -x is rejected instead of silently
// ignored. This is the single source of truth for flag names, aliases,
// requiredness and obsolete-flag hints; -h/--help is handled separately.
type flagSpec struct {
	name     string // long name without the leading --
	short    string // optional single-letter short form, without the leading -
	value    bool   // true when the flag consumes a value argument
	required bool   // true when the value flag must be present
	obsolete string // when set, presence fails with "use <this>"
	help     string
}

// commandSpec declares one executable command or command group (a group has
// children and no handler of its own). Every handler in dispatch is backed by
// exactly one leaf commandSpec; group specs exist only to resolve and help.
type commandSpec struct {
	path    []string // e.g. ["scope", "invite"]
	summary string
	usage   string
	detail  string

	args    []string // positional argument names, in order
	minArgs int
	maxArgs int // -1 = unlimited

	flags    []flagSpec
	children []*commandSpec
	hidden   bool
}

// flag returns the declared flagSpec for a long or short name, if any.
func (s *commandSpec) flag(name string) *flagSpec {
	for i := range s.flags {
		if s.flags[i].name == name {
			return &s.flags[i]
		}
		if s.flags[i].short != "" && s.flags[i].short == name {
			return &s.flags[i]
		}
	}
	return nil
}

// isGroup reports whether the spec has subcommands of its own.
func (s *commandSpec) isGroup() bool { return len(s.children) > 0 }

func (s *commandSpec) id() string { return joinPath(s.path) }

func joinPath(path []string) string { return strings.Join(path, " ") }

// nameArg renders a positional-name list ("<target>") or nothing.
func (s *commandSpec) usageArgs() string {
	if s.maxArgs != 0 || len(s.args) > 0 {
		return " " + strings.Join(s.args, " ")
	}
	return ""
}

// commands is the single, declarative command spec tree. All parsing, help,
// and validation derive from it. Keep it flat: group specs are prefixes of
// their children.
var commands = []*commandSpec{
	// Group declarations (help/parser nodes, no handlers of their own).
	{path: []string{"get"}, summary: "list resources"},
	{path: []string{"watch"}, summary: "watch resource events"},
	{path: []string{"peers"}, summary: "manage peer aliases"},
	{path: []string{"config"}, summary: "validate or print configuration"},
	{path: []string{"supervise"}, summary: "install, inspect, or remove launchd supervision"},
	{path: []string{"scope"}, summary: "create, invite, join, or use Scopes"},
	{path: []string{"authority"}, summary: "scope authority administration"},
	{path: []string{"service"}, summary: "define local Services"},
	{path: []string{"pipe"}, summary: "define Pipes"},
	{path: []string{"join"}, summary: "configure overlay reachability"},
	{path: []string{"keygen"}, summary: "generate keys"},

	{
		path:    []string{"attach"},
		summary: "create or reuse a Service and its Pipe, then run it",
		usage:   "tubo attach <target> --name <name> [-d] [--json]",
		args:    []string{"<target>"},
		minArgs: 1,
		maxArgs: 1,
		flags: []flagSpec{
			{name: "name", value: true, required: true, help: "friendly name for the Service and Pipe"},
			{name: "d", help: "run the Pipe detached; print readiness"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
		detail: `attach defines a local Service for <target> (http://…, tcp://host:port,
or a unix path) and defines a Pipe targeting it. With -d the Pipe is started
detached and the command returns once it is ready; without -d the Pipe runs in
the foreground until Ctrl-C. Re-attaching the same name reuses existing
definitions. Share a ready Pipe with: tubo share pipe/<name>.`,
	},
	{
		path:    []string{"connect"},
		summary: "redeem an exact invite and run a local Pipe",
		usage:   "tubo connect --token <invite> --local <host:port> [--name <name>] [-d] [--json]",
		flags: []flagSpec{
			{name: "token", value: true, required: true, help: "exact invite produced by tubo share"},
			{name: "local", value: true, required: true, help: "local listen address (host:port)"},
			{name: "name", value: true, help: "friendly name for the local Pipe (default: deterministic name from the remote Pipe ID)"},
			{name: "d", help: "run the Pipe detached; print readiness"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
		detail: `connect redeems a one-time exact invite, verifies the resulting Access
Lease, defines a local ingress Pipe, and runs it. With -d the Pipe is started
detached and the command returns once ready; without -d it runs in the
foreground until Ctrl-C. Stop the local Pipe with: tubo stop pipe/<name>.`,
	},
	{
		path:    []string{"share"},
		summary: "mint a one-time invite for an owned Pipe",
		usage:   "tubo share <pipe-ref> [--json]",
		args:    []string{"<pipe-ref>"},
		minArgs: 1,
		maxArgs: 1,
		flags: []flagSpec{
			{name: "json", help: "emit one JSON object on stdout"},
		},
		detail: `share prints the raw invite on stdout so it can be captured directly:

  INVITE="$(tubo share pipe/origin)"

The invite is one-time, short-lived, and grants the holder a short-lived
Access Lease for exactly that Pipe; the target Pipe must be a running,
owned local Pipe. Behavioral notes and next steps go to stderr.`,
	},
	{
		path:    []string{"service", "create"},
		summary: "define a local Service",
		usage:   "tubo service create <name> --target <url> [--json]",
		args:    []string{"<name>"},
		minArgs: 1,
		maxArgs: 1,
		flags: []flagSpec{
			{name: "target", value: true, required: true, help: "http://…, tcp://host:port, or unix path"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"pipe", "create"},
		summary: "define a Pipe",
		usage:   "tubo pipe create <name> --ingress <tubo|local://host:port> --target <ref> [--scope cluster/namespace] [--visibility <policy>] [--access <policy>] [--json]",
		args:    []string{"<name>"},
		minArgs: 1,
		maxArgs: 1,
		flags: []flagSpec{
			{name: "ingress", value: true, required: true, help: "tubo (network ingress) or local://host:port"},
			{name: "target", value: true, required: true, help: "service/<name> or pipe/<ref>"},
			{name: "scope", value: true, help: "scope for a tubo ingress (required for tubo)"},
			{name: "visibility", value: true, help: "visibility policy for a tubo ingress"},
			{name: "access", value: true, help: "access policy for a tubo ingress"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"start"},
		summary: "start a Pipe runtime",
		usage:   "tubo start <pipe-ref> [-d] [--json]",
		args:    []string{"<pipe-ref>"},
		minArgs: 1,
		maxArgs: 1,
		flags: []flagSpec{
			{name: "d", help: "run detached; print readiness"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"stop"},
		summary: "stop a running Pipe or infrastructure runtime",
		usage:   "tubo stop <ref> [--json]",
		args:    []string{"<ref>"},
		minArgs: 1,
		maxArgs: 1,
		flags: []flagSpec{
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"restart"},
		summary: "restart a Pipe runtime",
		usage:   "tubo restart <pipe-ref> [--json]",
		args:    []string{"<pipe-ref>"},
		minArgs: 1,
		maxArgs: 1,
		flags: []flagSpec{
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"rm"},
		summary: "remove a resource or stale runs",
		usage:   "tubo rm <ref> [--force] [--json] | tubo rm --stale [--json]",
		args:    []string{"<service/name|pipe/name>"},
		minArgs: 0,
		maxArgs: 1,
		flags: []flagSpec{
			{name: "force", help: "stop and remove a running Pipe"},
			{name: "stale", help: "remove stale runtime records instead of a resource"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"logs"},
		summary: "show runtime logs",
		usage:   "tubo logs <ref> [--tail N] [-f] [--json]",
		args:    []string{"<ref>"},
		minArgs: 1,
		maxArgs: 1,
		flags: []flagSpec{
			{name: "tail", value: true, help: "number of trailing lines (default 200)"},
			{name: "f", short: "f", help: "follow the log"},
			{name: "follow", help: "follow the log"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"inspect"},
		summary: "inspect one resource in detail",
		usage:   "tubo inspect <resource> [--json]",
		args:    []string{"<resource>"},
		minArgs: 1,
		maxArgs: 1,
		flags: []flagSpec{
			{name: "json", help: "emit full structured JSON on stdout"},
		},
		detail: `inspect is the detailed human view of one resource: pipe/<name>,
service/<name>, scope/<scope>, overlay/<name>, session/<id>, peer/<id>,
relay/default, or authority/<cluster>. The default form is labeled plain
text; --json emits the full structured view.`,
	},
	{
		path:    []string{"get", "pipes"},
		summary: "list Pipes (local definitions and verified network records)",
		usage:   "tubo get pipes [--scope cluster/namespace] [--refresh|--cached-only] [--wide] [--json]",
		flags: []flagSpec{
			{name: "scope", value: true, help: "filter to one scope"},
			{name: "refresh", help: "force refreshing discovery caches"},
			{name: "cached-only", help: "never refresh or open discovery sockets"},
			{name: "wide", help: "show the full-width table"},
			{name: "json", help: "emit one JSON object on stdout"},
			{name: "live", obsolete: "use --refresh", help: ""},
		},
		detail: `get pipes is cache-first and shows verified network records plus local
Pipe definitions. Status is the local runtime lifecycle or "available" for
remote records. Use --wide for the full table; use inspect pipe/<name> for a
single Pipe in detail.`,
	},
	{
		path:    []string{"get", "services"},
		summary: "list local Services",
		usage:   "tubo get services [--json]",
		flags:   []flagSpec{{name: "json", help: "emit one JSON object on stdout"}},
	},
	{
		path:    []string{"get", "scopes"},
		summary: "list Scopes and memberships",
		usage:   "tubo get scopes [--json]",
		flags:   []flagSpec{{name: "json", help: "emit one JSON object on stdout"}},
	},
	{
		path:    []string{"get", "peers"},
		summary: "list peers (observed and aliased)",
		usage:   "tubo get peers [--json]",
		flags:   []flagSpec{{name: "json", help: "emit one JSON object on stdout"}},
	},
	{
		path:    []string{"get", "overlays"},
		summary: "show overlay configuration",
		usage:   "tubo get overlays [--json]",
		flags:   []flagSpec{{name: "json", help: "emit one JSON object on stdout"}},
	},
	{
		path:    []string{"watch", "pipes"},
		summary: "watch verified Pipe projection events",
		usage:   "tubo watch pipes [--scope cluster/namespace] [--interval 2s] [--json]",
		flags: []flagSpec{
			{name: "scope", value: true, help: "watch one scope only"},
			{name: "interval", value: true, help: "poll interval (at least 500ms)"},
			{name: "json", help: "emit NDJSON events"},
		},
	},
	{
		path:    []string{"ps"},
		summary: "show a compact runtime overview",
		usage:   "tubo ps [--json]",
		flags:   []flagSpec{{name: "json", help: "emit one JSON object on stdout"}},
		detail: `ps is the compact lifecycle view: what is running, its primary resource,
lifecycle status, PID, session count and basic path/address. Live network and
byte metrics belong to top.`,
	},
	{
		path:    []string{"top"},
		summary: "show live runtime and network metrics",
		usage:   "tubo top [--interval 1s] [--once] [--json]",
		flags: []flagSpec{
			{name: "interval", value: true, help: "refresh interval (at least 100ms)"},
			{name: "once", help: "refresh once and exit"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"peers", "alias"},
		summary: "label a peer with a friendly name",
		usage:   "tubo peers alias <peer-id> --name <label> [--note <text>] [--json]",
		args:    []string{"<peer-id>"},
		minArgs: 1,
		maxArgs: 1,
		flags: []flagSpec{
			{name: "name", value: true, required: true, help: "label"},
			{name: "note", value: true, help: "optional note"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"config", "validate"},
		summary: "validate overlay configuration",
		usage:   "tubo config validate [--json]",
		flags:   []flagSpec{{name: "json", help: "emit one JSON object on stdout"}},
	},
	{
		path:    []string{"config", "print"},
		summary: "print masked overlay configuration",
		usage:   "tubo config print [--json]",
		flags:   []flagSpec{{name: "json", help: "emit one JSON object on stdout"}},
	},
	{
		path:    []string{"doctor"},
		summary: "run local health checks",
		usage:   "tubo doctor [--json]",
		flags:   []flagSpec{{name: "json", help: "emit one JSON object on stdout"}},
	},
	{
		path:    []string{"supervise", "install"},
		summary: "install launchd supervision for a resource",
		usage:   "tubo supervise install <pipe/name|authority/cluster> [--scope cluster/namespace] [--json]",
		args:    []string{"<pipe/name|authority/cluster>"},
		minArgs: 1,
		maxArgs: 1,
		flags: []flagSpec{
			{name: "scope", value: true, help: "scope for authority supervision"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"supervise", "status"},
		summary: "show supervision status",
		usage:   "tubo supervise status <ref> [--json]",
		args:    []string{"<ref>"},
		minArgs: 1,
		maxArgs: 1,
		flags:   []flagSpec{{name: "json", help: "emit one JSON object on stdout"}},
	},
	{
		path:    []string{"supervise", "uninstall"},
		summary: "remove launchd supervision",
		usage:   "tubo supervise uninstall <ref> [--json]",
		args:    []string{"<ref>"},
		minArgs: 1,
		maxArgs: 1,
		flags:   []flagSpec{{name: "json", help: "emit one JSON object on stdout"}},
	},
	{
		path:    []string{"scope", "create"},
		summary: "create a Scope",
		usage:   "tubo scope create <cluster/namespace> [--json]",
		args:    []string{"<cluster/namespace>"},
		minArgs: 1,
		maxArgs: 1,
		flags:   []flagSpec{{name: "json", help: "emit one JSON object on stdout"}},
	},
	{
		path:    []string{"scope", "invite"},
		summary: "mint a membership invite",
		usage:   "tubo scope invite <cluster/namespace> --role <member|viewer|publisher|forwarder> [--expires <duration>] [--json]",
		args:    []string{"<cluster/namespace>"},
		minArgs: 1,
		maxArgs: 1,
		flags: []flagSpec{
			{name: "role", value: true, required: true, help: "member, viewer, publisher, or forwarder"},
			{name: "expires", value: true, help: "invite lifetime (default 24h)"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"scope", "join"},
		summary: "redeem a membership invite",
		usage:   "tubo scope join --token <invite> [--json]",
		flags: []flagSpec{
			{name: "token", value: true, required: true, help: "membership invite"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"scope", "use"},
		summary: "set the current Scope",
		usage:   "tubo scope use <cluster/namespace> [--json]",
		args:    []string{"<cluster/namespace>"},
		minArgs: 1,
		maxArgs: 1,
		flags:   []flagSpec{{name: "json", help: "emit one JSON object on stdout"}},
	},
	{
		path:    []string{"authority", "start"},
		summary: "start a Scope authority",
		usage:   "tubo authority start <cluster> --scope <cluster/namespace> [--json]",
		args:    []string{"<cluster>"},
		minArgs: 1,
		maxArgs: 1,
		flags: []flagSpec{
			{name: "scope", value: true, required: true, help: "authority scope"},
			{name: "d", help: "unused; use tubo supervise install authority/<cluster>"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
		detail: `authority start runs a scope authority in the foreground. Detached
authority launch and recovery are provided by supervision:
  tubo supervise install authority/<cluster> --scope <cluster/namespace>`,
	},
	{
		path:    []string{"relay"},
		summary: "run a relay in the foreground",
		usage:   "tubo relay --listen <multiaddr> --public-addr <addr> [--swarm-key <file>] [--json]",
		flags: []flagSpec{
			{name: "listen", value: true, required: true, help: "listen multiaddr"},
			{name: "public-addr", value: true, required: true, help: "publicly reachable address"},
			{name: "swarm-key", value: true, help: "optional swarm key file"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"join", "overlay"},
		summary: "configure overlay reachability",
		usage:   "tubo join overlay --relay <multiaddr> --swarm-key <file> [--force-reachability private|public] [--json]",
		flags: []flagSpec{
			{name: "relay", value: true, required: true, help: "relay multiaddr"},
			{name: "swarm-key", value: true, required: true, help: "swarm key file"},
			{name: "force-reachability", value: true, help: "private or public (default private)"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"revoke"},
		summary: "revoke an invite, session, access, or publish grant",
		usage:   "tubo revoke <invite|session|pipe-access|pipe-publish> <id> [--json]",
		args:    []string{"<type>", "<id>"},
		minArgs: 2,
		maxArgs: 2,
		flags:   []flagSpec{{name: "json", help: "emit one JSON object on stdout"}},
	},
	{
		path:    []string{"keygen", "swarm"},
		summary: "generate a swarm key file",
		usage:   "tubo keygen swarm --out <file> [--json]",
		flags: []flagSpec{
			{name: "out", value: true, required: true, help: "output file path"},
			{name: "json", help: "emit one JSON object on stdout"},
		},
	},
	{
		path:    []string{"version"},
		summary: "print version information",
		usage:   "tubo version [--json]",
		flags:   []flagSpec{{name: "json", help: "emit one JSON object on stdout"}},
	},
	{
		path:    []string{"_run-pipe"},
		summary: "internal Pipe runner (do not call directly)",
		usage:   "tubo _run-pipe <pipe-ref>",
		args:    []string{"<pipe-ref>"},
		minArgs: 1,
		maxArgs: 1,
		hidden:  true,
	},
}

// leafPaths is every executable command path, in declaration order. dispatch
// must cover exactly these paths; a test cross-checks that against the spec
// tree so new commands cannot drift.
var leafPaths = []string{
	"attach",
	"connect",
	"share",
	"service create",
	"pipe create",
	"start",
	"stop",
	"restart",
	"rm",
	"logs",
	"inspect",
	"get pipes",
	"get services",
	"get scopes",
	"get peers",
	"get overlays",
	"watch pipes",
	"ps",
	"top",
	"peers alias",
	"config validate",
	"config print",
	"doctor",
	"supervise install",
	"supervise status",
	"supervise uninstall",
	"scope create",
	"scope invite",
	"scope join",
	"scope use",
	"authority start",
	"relay",
	"join overlay",
	"revoke",
	"keygen swarm",
	"version",
	"_run-pipe",
}

// specFor returns the commandSpec for a command path, or nil.
func specFor(path string) *commandSpec {
	for _, spec := range commands {
		if spec.id() == path {
			return spec
		}
	}
	return nil
}

// groupSpecs returns the child command specs of a group, sorted by name.
func groupSpecs(group *commandSpec) []*commandSpec {
	out := make([]*commandSpec, 0, len(group.children))
	out = append(out, group.children...)
	sort.Slice(out, func(i, j int) bool { return out[i].id() < out[j].id() })
	return out
}

// childrenOf returns the immediate child specs of a group path (used by help
// and the resolver). Returns nil for unknown paths.
func childrenOf(path string) []*commandSpec {
	group := specFor(path)
	if group == nil {
		return nil
	}
	children := make([]*commandSpec, 0)
	for _, spec := range commands {
		if len(spec.path) == len(group.path)+1 && strings.HasPrefix(spec.id(), path+" ") {
			children = append(children, spec)
		}
	}
	sort.Slice(children, func(i, j int) bool { return children[i].id() < children[j].id() })
	return children
}

// buildGroups links parent/child specs after declaration.
func buildGroups() {
	byID := map[string]*commandSpec{}
	for _, spec := range commands {
		byID[spec.id()] = spec
	}
	for _, spec := range commands {
		parentID := ""
		if len(spec.path) > 1 {
			parentID = joinPath(spec.path[:len(spec.path)-1])
		}
		if parent, ok := byID[parentID]; ok {
			parent.children = append(parent.children, spec)
		}
	}
}

func init() { buildGroups() }

// resolveCommand walks args and returns the deepest matching commandSpec plus
// the remaining args after the command path. Flags are skipped while walking;
// non-flag words that do not extend the path stop the walk.
func resolveCommand(args []string) (*commandSpec, []string, error) {
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("missing command; run `tubo help`")
	}
	base := args[0]
	if strings.HasPrefix(base, "-") {
		return nil, nil, fmt.Errorf("invalid_input: unknown command %q; run `tubo help`", base)
	}
	// Prefer the exact one-word spec for the base; fall back to the longest
	// declared path starting with base (covers a base whose group node is not
	// declared). Word specs always have an exact [base] group or leaf node.
	matched := specFor(base)
	if matched == nil {
		for _, spec := range commands {
			if spec.path[0] == base && (matched == nil || len(spec.path) > len(matched.path)) {
				matched = spec
			}
		}
	}
	if matched == nil {
		return nil, nil, fmt.Errorf("invalid_input: unknown command %q; run `tubo help`", base)
	}
	depth := len(matched.path)
	consumed := 1
	// Extend the match by consecutive non-flag words that equal a deeper path
	// word of some candidate.
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			continue
		}
		var next *commandSpec
		for _, spec := range commands {
			if spec.path[0] != base {
				continue
			}
			if len(spec.path) > depth && spec.path[depth] == arg && (next == nil || len(next.path) < len(spec.path)) {
				next = spec
			}
		}
		if next == nil {
			break
		}
		matched = next
		depth = len(next.path)
		consumed = i + 1
	}
	return matched, args[consumed:], nil
}

// hasHelpFlag reports whether the args request contextual help.
func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}
