/*
SPDX-FileCopyrightText: Red Hat

SPDX-License-Identifier: Apache-2.0
*/

package internal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"runtime/debug"
	"slices"

	ctlrutils "github.com/openshift-kni/oran-o2ims/internal/controllers/utils"
	"github.com/openshift-kni/oran-o2ims/internal/logging"

	"github.com/spf13/cobra"
)

// ToolBuilder contains the data and logic needed to create an instance of the command line
// tool. Don't create instances of this directly, use the NewTool function instead.
type ToolBuilder struct {
	logger *slog.Logger
	sub    []func() *cobra.Command
	args   []string
	in     io.Reader
	out    io.Writer
	err    io.Writer
}

// Tool is an instance of the command line tool. Don't create instances of this directly, use the
// NewTool function instead.
type Tool struct {
	logger      *slog.Logger
	loggerOwned bool
	cmd         *cobra.Command
	sub         []func() *cobra.Command
	args        []string
	in          io.Reader
	out         io.Writer
	err         io.Writer
}

// NewTool creates a builder that can then be used to configure and create an instance of the
// command line tool.
func NewTool() *ToolBuilder {
	return &ToolBuilder{}
}

// SetLogger sets the logger that the tool will use to write messages to the log. This is optional,
// and if not specified a new one will be created that writes JSON messages to a file `o2ims.log`
// file inside the tool cache directory.
func (b *ToolBuilder) SetLogger(value *slog.Logger) *ToolBuilder {
	b.logger = value
	return b
}

// AddCommand adds a sub-command.
func (b *ToolBuilder) AddCommand(value func() *cobra.Command) *ToolBuilder {
	b.sub = append(b.sub, value)
	return b
}

// AddCommands adds a list of sub-commands.
func (b *ToolBuilder) AddCommands(values ...func() *cobra.Command) *ToolBuilder {
	b.sub = append(b.sub, values...)
	return b
}

// AddArg adds one command line argument.
func (b *ToolBuilder) AddArg(value string) *ToolBuilder {
	b.args = append(b.args, value)
	return b
}

// AddArgs adds a list of command line arguments.
func (b *ToolBuilder) AddArgs(values ...string) *ToolBuilder {
	b.args = append(b.args, values...)
	return b
}

// SetArgs sets the list of command line arguments.
func (b *ToolBuilder) SetArgs(values ...string) *ToolBuilder {
	b.args = slices.Clone(values)
	return b
}

// SetIn sets the standard input stream. This is mandatory.
func (b *ToolBuilder) SetIn(value io.Reader) *ToolBuilder {
	b.in = value
	return b
}

// SetOut sets the standard output stream. This is mandatory.
func (b *ToolBuilder) SetOut(value io.Writer) *ToolBuilder {
	b.out = value
	return b
}

// SetErr sets the standard error output stream. This is mandatory.
func (b *ToolBuilder) SetErr(value io.Writer) *ToolBuilder {
	b.err = value
	return b
}

// Build uses the data stored in the buider to create a new instance of the command line tool.
func (b *ToolBuilder) Build() (result *Tool, err error) {
	// Check parameters:
	if len(b.args) == 0 {
		err = errors.New(
			"at least one command line argument (usually the name of the binary) is " +
				"required",
		)
		return
	}
	if b.in == nil {
		err = errors.New("standard input stream is mandatory")
		return
	}
	if b.out == nil {
		err = errors.New("standard output stream is mandatory")
		return
	}
	if b.err == nil {
		err = errors.New("standard error output stream is mandatory")
		return
	}

	// Create and populate the object:
	result = &Tool{
		logger: b.logger,
		sub:    slices.Clone(b.sub),
		args:   slices.Clone(b.args),
		in:     b.in,
		out:    b.out,
		err:    b.err,
	}
	return
}

// Run runs the tool.
func (t *Tool) Run(ctx context.Context) error {
	// Create the main command:
	err := t.createCommand()
	if err != nil {
		return fmt.Errorf("failed to create default logger: %w", err)
	}

	// Create a default logger that we can use while we haven't yet parsed the command line
	// flags that contain the logging configuration.
	if t.logger == nil {
		t.logger, err = t.createDefaultLogger()
		if err != nil {
			return err
		}
		t.loggerOwned = true
	}

	// Execute the main command:
	t.logger.InfoContext(
		ctx,
		"Command",
		"args", t.args,
	)
	t.cmd.SetArgs(t.args[1:])
	err = t.cmd.ExecuteContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to run command with args %v: %w, ", t.args, err)
	}

	return nil
}

func (t *Tool) run(cmd *cobra.Command, args []string) error {
	var err error

	// Replace the default logger with one configured according to the command line options:
	if t.loggerOwned {
		t.logger, err = t.createConfiguredLogger()
		if err != nil {
			return err
		}
	}

	// Populate the context:
	ctx := cmd.Context()
	ctx = ToolIntoContext(ctx, t)
	ctx = LoggerIntoContext(ctx, t.logger)
	cmd.SetContext(ctx)

	// Write build information:
	t.writeBuildInfo(ctx)

	// Security validation checks
	t.validateSecurityParameters(ctx)

	return nil
}

func (t *Tool) createCommand() error {
	// Create the main command:
	t.cmd = &cobra.Command{
		Use:               "oran-o2ims",
		Long:              "O2 IMS",
		PersistentPreRunE: t.run,
		SilenceErrors:     true,
		SilenceUsage:      true,
	}

	// Add flags that apply to all the commands:
	flags := t.cmd.PersistentFlags()
	logging.AddFlags(flags)

	// Add sub-commands:
	for _, sub := range t.sub {
		cmd := sub()
		if cmd == nil {
			return fmt.Errorf("failed to create sub-command")
		}
		t.cmd.AddCommand(cmd)
	}

	return nil
}

func (t *Tool) createDefaultLogger() (result *slog.Logger, err error) {
	result, err = logging.NewLogger().
		SetOut(t.out).
		SetErr(t.err).
		Build()
	return
}

func (t *Tool) createConfiguredLogger() (result *slog.Logger, err error) {
	result, err = logging.NewLogger().
		SetFlags(t.cmd.Flags()).
		SetOut(t.out).
		SetErr(t.err).
		Build()
	return
}

func (t *Tool) writeBuildInfo(ctx context.Context) {
	// Retrieve the information:
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		t.logger.InfoContext(ctx, "Build information isn't available")
		return
	}

	// Extract the information that we need:
	logFields := []any{
		"go", buildInfo.GoVersion,
		"os", runtime.GOOS,
		"arch", runtime.GOARCH,
	}
	for _, buildSetting := range buildInfo.Settings {
		switch buildSetting.Key {
		case "vcs.revision":
			logFields = append(logFields, "revision", buildSetting.Value)
		case "vcs.time":
			logFields = append(logFields, "time", buildSetting.Value)
		}
	}

	// Write the information:
	t.logger.InfoContext(ctx, "Build", logFields...)
}

// validateSecurityParameters validates
func (t *Tool) validateSecurityParameters(ctx context.Context) {
	value := ctlrutils.GetTLSSkipVerify()
	if value {
		t.logger.WarnContext(ctx, fmt.Sprintf("TLS certificate verification skipped by environment variable '%s'; this configuration is not recommended for production systems",
			ctlrutils.TLSSkipVerifyEnvName))
	}
}

// In returns the input stream of the tool.
func (t *Tool) In() io.Reader {
	return t.in
}

// Out returns the output stream of the tool.
func (t *Tool) Out() io.Writer {
	return t.out
}

// Err returns the error output stream of the tool.
func (t *Tool) Err() io.Writer {
	return t.err
}
