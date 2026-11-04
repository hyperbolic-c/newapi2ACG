// Command migrator validates migration configuration and runs sync jobs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
)

const usage = "usage: migrator validate --config-dir DIR | migrator run --config-dir DIR [--job NAME]"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := execute(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func execute(ctx context.Context, args []string, out, errs io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errs, usage)
		return 2
	}
	switch args[0] {
	case "validate":
		return validateCommand(args[1:], out, errs)
	case "run":
		return runCommand(ctx, args[1:], errs, defaultRunEnv())
	}
	fmt.Fprintln(errs, usage)
	return 2
}

func validateCommand(args []string, out, errs io.Writer) int {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(errs)
	dir := flags.String("config-dir", "configs", "configuration directory")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errs, "validate accepts no positional arguments")
		return 2
	}
	bundle, err := config.LoadDir(*dir)
	if err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	if err := bundle.ValidateReferences(); err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	result := struct {
		Status    string `json:"status"`
		Hash      string `json:"config_hash"`
		Jobs      int    `json:"jobs"`
		Sources   int    `json:"sources"`
		Instances int    `json:"instances"`
		Targets   int    `json:"targets"`
		Lookups   int    `json:"lookups"`
	}{"structural_validation_passed", bundle.Hash, len(bundle.Jobs), len(bundle.Sources), len(bundle.Instances), len(bundle.Targets), len(bundle.Lookups)}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		fmt.Fprintln(errs, "cannot write validation result")
		return 1
	}
	return 0
}
