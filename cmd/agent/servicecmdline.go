package main

import (
	"fmt"
	"path/filepath"

	"github.com/briggleman/kraken/internal/agent/config"
)

// The registered service command line is the authoritative answer to "how is
// this host's agent service actually configured?" — the SCM stores it as
// Config.BinaryPathName (`C:\kraken\bin\kraken-agent.exe --root C:\kraken`),
// and it is what the service process will parse when the SCM starts it.
//
// `--service start` and `--service status` used to answer that question from
// the flags the OPERATOR typed instead, which is only the same thing when they
// remembered to retype install.ps1's `--root`. Without it, start printed a log
// path under the operator's home directory that does not exist, and status
// reported DRIFT on the command-line row of a perfectly healthy install — both
// at the exact moment someone is trying to find out why a node did not come
// back (#273, seen during the #271 recovery drill on abyss-win).
//
// Everything in this file is OS-neutral so it is parsed, selected, and TESTED
// on every platform (CI is Linux); only the SCM reads that feed it live in
// service_windows.go.

// splitCommandLine splits a Windows command line into its argv, following the
// same rules CommandLineToArgvW applies — which is what the service process
// itself will do with this string.
//
// It is the inverse of syscall.EscapeArg (the escaper serviceCommandLine uses
// when it writes the string), so the round trip through the SCM is lossless:
// double quotes group an argument containing spaces, a run of backslashes is
// literal unless it is followed by a quote, and inside quotes a doubled quote
// is one literal quote.
//
// Written out here rather than calling windows.DecomposeCommandLine because
// that one is Windows-only (it hands the string to the OS) and the selection
// logic below it has to be testable on Linux.
func splitCommandLine(cmdLine string) []string {
	var argv []string
	var arg []byte
	inQuotes := false
	started := false // distinguishes an empty quoted arg ("") from no arg at all
	backslashes := 0

	// flushBackslashes emits n literal backslashes into the current argument.
	flushBackslashes := func(n int) {
		for ; n > 0; n-- {
			arg = append(arg, '\\')
		}
	}

	for i := 0; i < len(cmdLine); i++ {
		c := cmdLine[i]
		switch c {
		case '\\':
			backslashes++
			continue
		case '"':
			// 2n backslashes then a quote: n literal backslashes, and the
			// quote toggles quoting. 2n+1: n backslashes and a literal quote.
			flushBackslashes(backslashes / 2)
			if backslashes%2 == 1 {
				arg = append(arg, '"')
			} else if inQuotes && i+1 < len(cmdLine) && cmdLine[i+1] == '"' {
				// "" inside a quoted argument is an escaped quote that leaves
				// quoting on — the pre-2008 rule CommandLineToArgvW still honors.
				i++
				arg = append(arg, '"')
			} else {
				inQuotes = !inQuotes
			}
			backslashes = 0
			started = true
			continue
		case ' ', '\t':
			if !inQuotes {
				flushBackslashes(backslashes)
				backslashes = 0
				if started {
					argv = append(argv, string(arg))
					arg, started = nil, false
				}
				continue
			}
		}
		flushBackslashes(backslashes)
		backslashes = 0
		arg = append(arg, c)
		started = true
	}
	flushBackslashes(backslashes)
	if started {
		argv = append(argv, string(arg))
	}
	return argv
}

// registeredServiceArgs returns the ARGUMENTS of a registered service command
// line — argv[1:], the flags the service will be started with. ok is false when
// the command line is empty or yields no executable at all, which is the
// caller's signal to fall back to what the operator typed.
func registeredServiceArgs(cmdLine string) (args []string, ok bool) {
	argv := splitCommandLine(cmdLine)
	if len(argv) == 0 {
		return nil, false
	}
	return argv[1:], true
}

// registeredStateDir resolves the state directory the SERVICE uses, from the
// command line the SCM has registered for it.
//
// The args go through config.LoadArgs rather than config.Load so the operator's
// own environment and working directory cannot leak into the answer: the
// question is where the service writes, not where this shell would write.
func registeredStateDir(cmdLine string) (string, error) {
	args, ok := registeredServiceArgs(cmdLine)
	if !ok {
		return "", fmt.Errorf("service command line %q names no executable", cmdLine)
	}
	cfg, _, err := config.LoadArgs(args)
	if err != nil {
		return "", fmt.Errorf("parse service command line %q: %w", cmdLine, err)
	}
	return cfg.StateDir, nil
}

// startLogNotice renders the `logs: …` clause `--service start` prints, from
// the registered command line when it can be read and parsed (cmdLine is "" when
// the SCM config could not be read). The fallback says where the path came
// from, because a path derived from the typed flags is the one that sent
// operators to a file that does not exist.
func startLogNotice(cmdLine, typedStateDir string) string {
	if dir, err := registeredStateDir(cmdLine); err == nil {
		return fmt.Sprintf("logs: %s", filepath.Join(dir, "agent.log"))
	}
	return fmt.Sprintf("logs: %s (from the flags you typed; could not read the service command line)",
		filepath.Join(typedStateDir, "agent.log"))
}

// statusBaselineArgs picks the arguments `--service status` compares the
// registered command line against.
//
// Typed flags win whenever there are any: that is the "did install.ps1 register
// what I meant?" check, and silently substituting the registered flags would
// answer a question nobody asked. With no flags typed — a bare
// `--service status`, the way it gets run at 2am — the registered args are the
// baseline, so a healthy install reports no drift instead of a red row caused
// entirely by the operator's shorter command line.
func statusBaselineArgs(typed []string, registeredCmdLine string) (args []string, fromRegistered bool) {
	if len(typed) > 0 {
		return typed, false
	}
	if registered, ok := registeredServiceArgs(registeredCmdLine); ok {
		return registered, true
	}
	return typed, false
}
