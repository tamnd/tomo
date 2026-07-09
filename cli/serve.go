package cli

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/channel"
	"github.com/tamnd/tomo/pkg/config"
	// Channel drivers register themselves by name in init(). The serve command
	// reaches them through the registry, so it never names one directly; adding
	// a channel is adding an import here (or letting the scaffold do it).
	_ "github.com/tamnd/tomo/pkg/channel/discord"
	_ "github.com/tamnd/tomo/pkg/channel/imessage"
	_ "github.com/tamnd/tomo/pkg/channel/slack"
	_ "github.com/tamnd/tomo/pkg/channel/telegram"
	_ "github.com/tamnd/tomo/pkg/channel/webchat"
	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/schedule"
	"github.com/tamnd/tomo/pkg/store"
	"github.com/tamnd/tomo/pkg/voice"
)

func newServeCmd() *cobra.Command {
	var addr, model string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run tomo as a daemon: web chat plus every configured channel",
		Long: "serve starts the gateway. The web chat is always on (loopback by\n" +
			"default); Telegram and other channels start when configured.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}

			st, err := store.Open(filepath.Join(cfg.DataDir, "tomo.db"))
			if err != nil {
				return err
			}
			defer st.Close()

			if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
				return err
			}
			auditor, err := policy.OpenFileAuditor(filepath.Join(cfg.DataDir, "audit.log"))
			if err != nil {
				return err
			}
			defer auditor.Close()

			out := cmd.OutOrStdout()
			mcpTools, closeMCP := dialMCP(cmd.Context(), cfg, out)
			defer closeMCP()

			work, err := buildWorkforce(cfg, model, mcpTools)
			if err != nil {
				return err
			}
			var transcriber voice.Transcriber
			if v := cfg.Voice; v.Model != "" {
				transcriber = &voice.Whisper{Bin: v.Bin, Model: v.Model, FFmpeg: v.FFmpeg}
			}
			var synth voice.Synthesizer
			if v := cfg.Voice; v.TTSModel != "" {
				synth = &voice.Speaker{Bin: v.TTSBin, Model: v.TTSModel, FFmpeg: v.FFmpeg}
			}
			router := channel.NewRouter(st, work, auditor, transcriber, synth)
			defer router.WaitIdle()

			channels, err := openChannels(addr, cfg.Channels)
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "tomo serving on http://%s\n", addr)
			for _, ch := range channels {
				fmt.Fprintf(out, "  channel: %s\n", ch.Name())
			}
			if names := work.Names(); len(names) > 1 {
				fmt.Fprintf(out, "  workers: %s\n", strings.Join(names, ", "))
			}
			if transcriber != nil {
				fmt.Fprintf(out, "  voice in: whisper (%s)\n", cfg.Voice.Model)
			}
			if synth != nil {
				fmt.Fprintf(out, "  voice out: piper (%s)\n", cfg.Voice.TTSModel)
			}

			if hb := cfg.Heartbeat; hb.Enabled {
				if _, err := st.EnsureJob("heartbeat", hb.Every, heartbeatPrompt(hb.File), hb.Channel, hb.Chat); err != nil {
					return err
				}
				fmt.Fprintf(out, "  heartbeat: %s over %s\n", hb.Every, hb.File)
			}

			// The scheduler pushes background results to whichever channels can
			// post on their own.
			posters := map[string]schedule.Poster{}
			for _, ch := range channels {
				if p, ok := ch.(schedule.Poster); ok {
					posters[ch.Name()] = p
				}
			}
			sched := schedule.New(st, router.Background, posters)

			return runChannels(cmd, router, sched, channels)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8765", "web chat listen address")
	cmd.Flags().StringVarP(&model, "model", "m", "", "provider/model (default from config)")
	return cmd
}

// openChannels turns the config's channel map into live channels through the
// driver registry. The web chat is always opened, with the --addr flag folded
// into whatever the config set, so the front door is on even with no config.
// Every other name in the config is opened by its registered driver; a driver
// that returns nil (configured but off) is skipped. Names are opened in sorted
// order so the startup banner is stable.
func openChannels(addr string, cfg config.Channels) ([]channel.Channel, error) {
	web, err := channel.Open("web", mergeAddr(cfg["web"], addr))
	if err != nil {
		return nil, err
	}
	channels := []channel.Channel{web}

	names := make([]string, 0, len(cfg))
	for name := range cfg {
		if name == "web" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ch, err := channel.Open(name, channel.Settings(cfg[name]))
		if err != nil {
			return nil, fmt.Errorf("channel %s: %w", name, err)
		}
		if ch != nil {
			channels = append(channels, ch)
		}
	}
	return channels, nil
}

// mergeAddr layers the --addr flag over the web channel's config block so a
// flag wins over a config value, and a config value wins over the default.
func mergeAddr(web map[string]any, addr string) channel.Settings {
	s := channel.Settings{}
	maps.Copy(s, web)
	if addr != "" {
		s["addr"] = addr
	}
	return s
}

// heartbeatPrompt is the instruction the heartbeat job runs each beat. It
// points the agent at the checklist and asks it to stay silent when there is
// nothing worth saying, so a quiet beat delivers nothing.
func heartbeatPrompt(file string) string {
	return "This is your periodic heartbeat, running on your own with no one watching. " +
		"Read the checklist at " + file + " and take care of anything that is due or actionable now, " +
		"using your tools. Actions that would need approval are declined while unattended, so skip those. " +
		"If nothing needs doing, reply with nothing at all. Otherwise keep it to a short note of what you did."
}

// runChannels starts every channel plus the scheduler and blocks until the
// context is cancelled. The first error is returned after all have stopped.
func runChannels(cmd *cobra.Command, router *channel.Router, sched *schedule.Scheduler, channels []channel.Channel) error {
	ctx := cmd.Context()
	var wg sync.WaitGroup
	errs := make(chan error, len(channels)+1)
	for _, ch := range channels {
		wg.Go(func() {
			if err := ch.Run(ctx, router.HandlerFor(ch.Name())); err != nil {
				errs <- fmt.Errorf("%s: %w", ch.Name(), err)
			}
		})
	}
	wg.Go(func() {
		if err := sched.Run(ctx); err != nil {
			errs <- fmt.Errorf("scheduler: %w", err)
		}
	})
	wg.Wait()
	close(errs)
	return <-errs
}
