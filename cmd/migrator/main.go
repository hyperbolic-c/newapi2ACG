// Command migrator runs and operates a New API → ACG migration task.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/app"
	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/observability"
	"github.com/hyperbolic-c/newapi2ACG/internal/state"
)

const usage = `usage: migrator <command> --config FILE [flags]

commands:
  validate   check the configuration file
  run        run the task (full, incremental and periodic sync)
  status     show phase, last error and checkpoint
  pause      pause a running task
  resume     resume a paused or failed task
  recover    resume a failed task; --resnapshot also discards the checkpoint`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := execute(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}

func execute(ctx context.Context, args []string, out, errs io.Writer, env app.Env) int {
	if len(args) == 0 {
		fmt.Fprintln(errs, usage)
		return 2
	}
	cmd := args[0]
	flags := flag.NewFlagSet(cmd, flag.ContinueOnError)
	flags.SetOutput(errs)
	path := flags.String("config", "configs/jobs/newapi-to-acg.yaml", "job configuration file")
	logLevel := flags.String("log-level", "info", "debug, info, warn or error (run)")
	metricsAddr := flags.String("metrics-addr", "", "serve /healthz and /debug/vars on this address (run)")
	resnapshot := flags.Bool("resnapshot", false, "discard the checkpoint so the next start does a full sync (recover)")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errs, usage)
		return 2
	}
	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	switch cmd {
	case "validate":
		return printJSON(out, errs, map[string]any{"status": "ok", "job": cfg.Job.ID, "datasets": cfg.DatasetNames(),
			"target": cfg.Target.Type, "mapping": cfg.Mapping.Profile, "config_hash": cfg.Hash()})
	case "run":
		return run(ctx, cfg, env, errs, *logLevel, *metricsAddr)
	case "status", "pause", "resume", "recover":
		return admin(ctx, cmd, cfg, env, out, errs, *resnapshot)
	}
	fmt.Fprintln(errs, usage)
	return 2
}

func run(ctx context.Context, cfg *config.Config, env app.Env, errs io.Writer, level, metricsAddr string) int {
	log := observability.NewLogger(errs, level)
	if !cfg.Job.Enabled {
		log.Info("job is disabled in the configuration; exiting", "job", cfg.Job.ID)
		return 0
	}
	metrics := observability.NewMetrics()
	metrics.Publish()
	if metricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/debug/vars", expvar.Handler())
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
		srv := &http.Server{Addr: metricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("metrics listener failed", "error", err.Error())
			}
		}()
		defer func() { _ = srv.Close() }()
	}
	a, err := app.Build(ctx, cfg, env, log, metrics)
	if err != nil {
		log.Error("startup failed", "error", err.Error())
		return 1
	}
	defer func() { _ = a.Close() }()
	if err := a.Coordinator.Run(ctx); err != nil {
		log.Error("task stopped", "error", err.Error())
		return 1
	}
	log.Info("shutdown complete")
	return 0
}

func admin(ctx context.Context, cmd string, cfg *config.Config, env app.Env, out, errs io.Writer, resnapshot bool) int {
	store, db, err := app.OpenState(ctx, cfg, env)
	if err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	defer func() { _ = db.Close() }()
	task, err := store.LoadTask(ctx, cfg.Job.ID)
	if err != nil {
		fmt.Fprintf(errs, "task %s: %v\n", cfg.Job.ID, err)
		return 1
	}
	switch cmd {
	case "status":
		cp, err := store.LoadCheckpoint(ctx, cfg.Job.ID)
		if err != nil {
			fmt.Fprintln(errs, err)
			return 1
		}
		return printJSON(out, errs, map[string]any{"task": task.ID, "phase": task.Phase, "last_error": task.LastError,
			"lease_owner": task.LeaseOwner, "lease_until": task.LeaseUntil, "updated_at": task.UpdatedAt, "checkpoint": cp})
	case "pause":
		err = store.SetPhaseAdmin(ctx, task.ID, state.Paused, "paused by operator")
	case "resume", "recover":
		if task.Phase != state.Paused && task.Phase != state.Failed {
			fmt.Fprintf(errs, "task is %s; only a paused or failed task can be resumed\n", task.Phase)
			return 1
		}
		if cmd == "recover" && resnapshot {
			if err := store.ResetCheckpoint(ctx, task.ID); err != nil {
				fmt.Fprintln(errs, err)
				return 1
			}
		}
		err = store.SetPhaseAdmin(ctx, task.ID, state.Initializing, "")
	}
	if err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	fmt.Fprintf(out, "%s: ok\n", cmd)
	return 0
}

func printJSON(out, errs io.Writer, v any) int {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	return 0
}
