package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/monitor"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var monitorCmd = &cobra.Command{
	Use:   "monitor",
	Short: "Live usage monitoring across profiles",
	Long: `Monitor profile usage in real-time with multiple output formats.

Output formats:
  table  - Rich ASCII table for interactive terminal use (default)
  brief  - One-line summary for tmux/status bar integration
  json   - Machine-readable JSON for scripting
  alerts - Alert-only mode for logging (outputs only when thresholds crossed)

Examples:
  caam monitor                              # Interactive monitor
  caam monitor --format brief --once        # tmux status bar integration
  caam monitor --format alerts --threshold 80  # Alert mode
  caam monitor --format json --once | jq .  # JSON output for scripting
  caam monitor --provider claude            # Monitor specific provider
  caam monitor --interval 10s               # Faster refresh rate

Keyboard shortcuts (table mode):
  r - Refresh immediately
  q - Quit`,
	RunE: runMonitor,
}

func init() {
	rootCmd.AddCommand(monitorCmd)

	monitorCmd.Flags().DurationP("interval", "i", 30*time.Second, "refresh interval")
	monitorCmd.Flags().StringSliceP("provider", "p", nil, "providers to monitor (default: all)")
	monitorCmd.Flags().StringP("format", "f", "table", "output format: table, brief, json, alerts")
	monitorCmd.Flags().Float64P("threshold", "t", 80.0, "alert threshold percentage")
	monitorCmd.Flags().BoolP("once", "1", false, "fetch once and exit")
	monitorCmd.Flags().Bool("no-emoji", false, "disable emoji in table output")
	monitorCmd.Flags().IntP("width", "w", 75, "table width")
}

func runMonitor(cmd *cobra.Command, args []string) error {
	interval, _ := cmd.Flags().GetDuration("interval")
	providers, _ := cmd.Flags().GetStringSlice("provider")
	format, _ := cmd.Flags().GetString("format")
	threshold, _ := cmd.Flags().GetFloat64("threshold")
	once, _ := cmd.Flags().GetBool("once")
	noEmoji, _ := cmd.Flags().GetBool("no-emoji")
	width, _ := cmd.Flags().GetInt("width")

	// Validate format
	format = strings.ToLower(format)
	switch format {
	case "table", "brief", "json", "alerts":
		// valid
	default:
		return fmt.Errorf("invalid format %q: must be table, brief, json, or alerts", format)
	}

	// Default providers
	if len(providers) == 0 {
		providers = []string{"claude", "codex", "gemini", "opencode", "cursor"}
	}

	// Create renderer based on format
	var renderer monitor.Renderer
	switch format {
	case "table":
		r := monitor.NewTableRenderer()
		r.Width = width
		r.ShowEmoji = !noEmoji
		renderer = r
	case "brief":
		renderer = monitor.NewBriefRenderer()
	case "json":
		renderer = monitor.NewJSONRenderer(true)
	case "alerts":
		renderer = monitor.NewAlertRenderer(threshold)
	}

	// Set up context with cancellation
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Set up monitor dependencies
	vaultPath := authfile.DefaultVaultPath()
	vault := authfile.NewVault(vaultPath)

	var db *caamdb.DB
	var pool *authpool.AuthPool
	var healthStore *health.Storage

	db, err := caamdb.Open()
	if err != nil {
		// Continue without DB - just log warning
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not open database: %v\n", err)
	} else {
		defer db.Close()
	}

	healthStore = health.NewStorage("")

	mon := monitor.NewMonitor(
		monitor.WithInterval(interval),
		monitor.WithProviders(providers),
		monitor.WithVault(vault),
		monitor.WithDB(db),
		monitor.WithHealthStore(healthStore),
		monitor.WithAuthPool(pool),
	)

	out := cmd.OutOrStdout()

	// Single fetch mode
	if once {
		if err := mon.Refresh(ctx); err != nil {
			return err
		}
		state := mon.GetState()
		fmt.Fprintln(out, renderer.Render(state))
		return nil
	}

	// Interactive live monitoring
	return runLiveMonitor(ctx, mon, renderer, out, format, interval)
}

func runLiveMonitor(ctx context.Context, mon *monitor.Monitor, renderer monitor.Renderer, out io.Writer, format string, interval time.Duration) error {
	// Initial fetch
	if err := mon.Refresh(ctx); err != nil {
		return err
	}

	// Set up keyboard input for table mode
	var inputCh <-chan byte
	if format == "table" && term.IsTerminal(int(os.Stdin.Fd())) {
		inputCh = setupKeyboardInput()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Clear screen and show initial output for table mode
	if format == "table" {
		clearScreen(out)
	}
	renderAndShow(mon, renderer, out, format)

	for {
		select {
		case <-ctx.Done():
			if format == "table" {
				fmt.Fprintln(out, "\nMonitor stopped.")
			}
			return nil

		case <-ticker.C:
			if err := mon.Refresh(ctx); err != nil {
				// Log error but continue
				if format == "table" {
					fmt.Fprintf(os.Stderr, "Refresh error: %v\n", err)
				}
			}
			renderAndShow(mon, renderer, out, format)

		case key := <-inputCh:
			switch key {
			case 'q', 'Q':
				if format == "table" {
					fmt.Fprintln(out, "\nMonitor stopped.")
				}
				return nil
			case 'r', 'R':
				if err := mon.Refresh(ctx); err != nil {
					fmt.Fprintf(os.Stderr, "Refresh error: %v\n", err)
				}
				renderAndShow(mon, renderer, out, format)
			}
		}
	}
}

func renderAndShow(mon *monitor.Monitor, renderer monitor.Renderer, out io.Writer, format string) {
	state := mon.GetState()
	output := renderer.Render(state)

	if format == "table" {
		clearScreen(out)
	}

	if output != "" {
		fmt.Fprintln(out, output)
	}
}

func clearScreen(out io.Writer) {
	// ANSI escape sequence to clear screen and move cursor to top-left
	fmt.Fprint(out, "\033[2J\033[H")
}

func setupKeyboardInput() <-chan byte {
	ch := make(chan byte, 1)

	// Try to put terminal in raw mode for key input
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return ch // Return empty channel if we can't get raw mode
	}

	go func() {
		defer term.Restore(int(os.Stdin.Fd()), oldState)
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				return
			}
			select {
			case ch <- buf[0]:
			default:
			}
		}
	}()

	return ch
}
