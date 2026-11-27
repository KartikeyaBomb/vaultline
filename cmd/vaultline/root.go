package main

import (
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/KartikeyaBomb/vaultline/internal/config"
)

// app holds state shared by all subcommands, filled in before any of them run.
type app struct {
	configPath string
	logLevel   string
	dataDir    string

	cfg config.Config
	log *slog.Logger
}

func newRootCmd() *cobra.Command {
	a := &app{}
	root := &cobra.Command{
		Use:           "vaultline",
		Short:         "Fault-tolerant deduplicating snapshot store",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.init(cmd)
		},
	}
	f := root.PersistentFlags()
	f.StringVar(&a.configPath, "config", "", "path to a YAML config file")
	f.StringVar(&a.logLevel, "log-level", "", "log level: debug, info, warn, error (overrides config)")
	f.StringVar(&a.dataDir, "data-dir", "", "data directory (overrides config)")

	root.AddCommand(newVersionCmd(), newDebugCmd(a))
	return root
}

// init loads config, applies flag overrides, and sets up logging.
func (a *app) init(cmd *cobra.Command) error {
	cfg, err := config.Load(a.configPath)
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("log-level") {
		cfg.LogLevel = a.logLevel
	}
	if cmd.Flags().Changed("data-dir") {
		cfg.DataDir = a.dataDir
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	lvl, _ := cfg.SlogLevel()
	a.cfg = cfg
	a.log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(a.log)
	a.log.Debug("config loaded", "config", a.configPath, "data_dir", cfg.DataDir, "log_level", cfg.LogLevel)
	return nil
}
