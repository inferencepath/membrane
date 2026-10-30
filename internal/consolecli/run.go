package consolecli

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/inferencepath/membrane/internal/server/console"
)

// Run executes a `membrane config` subcommand (export, diff, or apply) and
// returns the exit code: 0 on success, 1 on a runtime or validation failure, 2
// on a usage error. Results go to stdout; errors and status go to stderr.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	sub := args[0]
	switch sub {
	case "export", "diff", "apply":
		return runSub(sub, args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "config: unknown subcommand %q\n%s\n", sub, usage)
		return 2
	}
}

const usage = `usage: membrane config <export|diff|apply> [flags]

  export   write the live configuration bundle (default: stdout)
  diff     validate a bundle and show the per-kind change set (no write)
  apply    validate, write atomically, and hot-reload a bundle

flags:
  -f, --file <path>      bundle file (export: output; diff/apply: input; '-' or
                         unset means stdout/stdin)
      --dry-run          (apply) validate and diff only; do not write
      --console-url <u>  Admin API base URL (default ` + DefaultConsoleURL + `)
      --token-ref <ref>  admin token reference (env:/file:); never a literal`

// commonFlags are shared by all subcommands.
type commonFlags struct {
	file       string
	dryRun     bool
	consoleURL string
	tokenRef   string
}

func bindFlags(fs *flag.FlagSet, withDryRun bool) *commonFlags {
	cf := &commonFlags{}
	fs.StringVar(&cf.file, "f", "", "bundle file ('-'/unset = stdout/stdin)")
	fs.StringVar(&cf.file, "file", "", "bundle file ('-'/unset = stdout/stdin)")
	fs.StringVar(&cf.consoleURL, "console-url", DefaultConsoleURL, "Admin API base URL")
	fs.StringVar(&cf.tokenRef, "token-ref", "", "admin token reference (env:/file:)")
	if withDryRun {
		fs.BoolVar(&cf.dryRun, "dry-run", false, "validate and diff only; do not write")
	}
	return cf
}

func runSub(sub string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cf := bindFlags(fs, sub == "apply")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	token, err := resolveToken(cf.tokenRef)
	if err != nil {
		fmt.Fprintf(stderr, "config %s: %v\n", sub, err)
		return 1
	}
	client := NewClient(cf.consoleURL, token)

	switch sub {
	case "export":
		return doExport(client, cf, stdout, stderr)
	case "diff":
		return doDiff(client, cf, stdout, stderr)
	case "apply":
		return doApply(client, cf, stdout, stderr)
	}
	return 2 // unreachable
}

// resolveToken turns a token reference into its value, which is never printed.
// An empty ref means loopback-only access.
func resolveToken(ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	return console.ResolveTokenRef(ref)
}

func doExport(c *Client, cf *commonFlags, stdout, stderr io.Writer) int {
	raw, version, err := c.Export()
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if err := writeOutput(cf.file, raw, stdout); err != nil {
		fmt.Fprintf(stderr, "export: write output: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "exported configuration (version %s)\n", short(version))
	return 0
}

func doDiff(c *Client, cf *commonFlags, stdout, stderr io.Writer) int {
	bundle, err := readInput(cf.file, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "diff: read bundle: %v\n", err)
		return 1
	}
	diff, err := c.Diff(bundle)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	printDiff(stdout, diff)
	if !diff.Valid {
		return 1
	}
	return 0
}

func doApply(c *Client, cf *commonFlags, stdout, stderr io.Writer) int {
	bundle, err := readInput(cf.file, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "apply: read bundle: %v\n", err)
		return 1
	}

	// A dry run is a diff that never writes.
	if cf.dryRun {
		diff, derr := c.Diff(bundle)
		if derr != nil {
			fmt.Fprintf(stderr, "%v\n", derr)
			return 1
		}
		printDiff(stdout, diff)
		fmt.Fprintln(stderr, "dry-run: no changes written")
		if !diff.Valid {
			return 1
		}
		return 0
	}

	// Send the live version so the apply is rejected if the server changed in
	// the meantime.
	_, version, verr := c.Export()
	if verr != nil {
		fmt.Fprintf(stderr, "apply: fetch current version: %v\n", verr)
		return 1
	}

	res, err := c.Apply(bundle, version)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	switch {
	case res.Applied:
		fmt.Fprintf(stdout, "applied and reloaded (version %s)\n", short(res.Version))
		return 0
	case res.Unchanged:
		fmt.Fprintln(stdout, "no changes to apply")
		return 0
	default:
		fmt.Fprintf(stderr, "apply rejected: %s\n", res.Reason)
		return 1
	}
}

func printDiff(w io.Writer, diff console.BundleDiff) {
	if !diff.Valid {
		fmt.Fprintf(w, "invalid bundle: %s\n", diff.Error)
		return
	}
	if !diff.Changed {
		fmt.Fprintln(w, "no changes: the bundle matches the live configuration")
		return
	}
	fmt.Fprintln(w, "changes:")
	for _, k := range diff.Kinds {
		mark := "   "
		if k.Changed {
			mark = " ~ "
		}
		fmt.Fprintf(w, "%s%s\n", mark, k.Kind)
	}
}

// I/O helpers, replaceable in tests.

// stdin is the default diff and apply input.
var stdin io.Reader = os.Stdin

func readInput(path string, in io.Reader) ([]byte, error) {
	if path == "" || path == "-" {
		return io.ReadAll(in)
	}
	return os.ReadFile(path)
}

func writeOutput(path string, data []byte, out io.Writer) error {
	if path == "" || path == "-" {
		_, err := out.Write(data)
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func short(v string) string {
	if len(v) > 12 {
		return v[:12] + "…"
	}
	return v
}
