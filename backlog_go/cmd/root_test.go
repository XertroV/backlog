package cmd

import (
	"strings"
	"testing"
)

func TestResolveBuildVersionEmptyFallsBackToDevDefault(t *testing.T) {
	// Not parallel: mutates package-level BuildVersion.
	prev := BuildVersion
	t.Cleanup(func() { BuildVersion = prev })

	BuildVersion = "   "
	if got := resolveBuildVersion(); got != "0.0.0" {
		t.Fatalf("empty BuildVersion = %q, want 0.0.0", got)
	}

	BuildVersion = "1.2.3"
	if got := resolveBuildVersion(); got != "1.2.3" {
		t.Fatalf("injected BuildVersion = %q, want 1.2.3", got)
	}
}

func TestNewRootCommandDefaults(t *testing.T) {
	t.Parallel()

	command := NewRootCommand()
	if command == nil {
		t.Fatal("NewRootCommand() returned nil")
	}
	if command.name != "backlog" {
		t.Fatalf("name = %q, expected backlog", command.name)
	}
	if command.version != "0.0.0" {
		t.Fatalf("version = %q, expected 0.0.0 (local builds without release ldflags)", command.version)
	}
	if command.Name() != "backlog" {
		t.Fatalf("Name() = %q, expected backlog", command.Name())
	}
	if command.Version() != "0.0.0" {
		t.Fatalf("Version() = %q, expected 0.0.0 (local builds without release ldflags)", command.Version())
	}

	if command.Usage() == "" {
		t.Fatal("usage should not be empty")
	}

	if !command.IsKnownCommand("init") {
		t.Fatal("init should be known command")
	}

	if len(command.Commands()) == 0 {
		t.Fatal("commands should not be empty")
	}

	usage := command.Usage()
	if !strings.Contains(usage, "next") || !strings.Contains(usage, "Show next available task on critical path.") {
		t.Fatalf("usage %q missing command descriptions", usage)
	}
	if !strings.Contains(usage, "\n  howto") {
		t.Fatalf("usage %q missing howto command entry", usage)
	}
	if strings.Contains(usage, "\n  ls ") {
		t.Fatalf("usage %q should not list ls alias separately", usage)
	}
	if strings.Contains(usage, "\n  tl ") {
		t.Fatalf("usage %q should not list tl alias separately", usage)
	}
	if strings.Contains(usage, "\n  r ") {
		t.Fatalf("usage %q should not list r alias separately", usage)
	}
	if !strings.Contains(usage, "list (alias: ls)") {
		t.Fatalf("usage %q missing list alias inline", usage)
	}
	if !strings.Contains(usage, "timeline (alias: tl)") {
		t.Fatalf("usage %q missing timeline alias inline", usage)
	}
	if !strings.Contains(usage, "report (alias: r)") {
		t.Fatalf("usage %q missing report alias inline", usage)
	}
	commandsSection := strings.SplitN(usage, "Commands:\n", 2)
	if len(commandsSection) != 2 {
		t.Fatalf("usage %q missing commands section", usage)
	}
	firstCommandLine := strings.SplitN(commandsSection[1], "\n", 2)[0]
	if !strings.Contains(firstCommandLine, "howto") {
		t.Fatalf("first command line %q does not prioritize howto", firstCommandLine)
	}

	for _, name := range command.Commands() {
		desc := command.commandDescriptions[name]
		if strings.TrimSpace(desc) == "" {
			t.Fatalf("missing description for command %q", name)
		}
	}
}
