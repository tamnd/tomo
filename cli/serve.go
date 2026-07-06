package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/channel"
	"github.com/tamnd/tomo/pkg/channel/discord"
	"github.com/tamnd/tomo/pkg/channel/imessage"
	"github.com/tamnd/tomo/pkg/channel/slack"
	"github.com/tamnd/tomo/pkg/channel/telegram"
	"github.com/tamnd/tomo/pkg/channel/webchat"
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

			engine := policy.New(policy.Config{
				Read: cfg.Policy.Read, Net: cfg.Policy.Net,
				Write: cfg.Policy.Write, Exec: cfg.Policy.Exec, Rules: cfg.Policy.Rules,
			})
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
			// Tools that come from an MCP server are not tomo's own code, so they
			// default to ask even when their class would otherwise run.
			for _, t := range mcpTools {
				engine.MarkExternal(t.Name)
			}

			newAgent := func() (*agent.Agent, error) {
				a, _, err := buildAgent(cfg, model, nil, mcpTools...)
				return a, err
			}
			var transcriber voice.Transcriber
			if v := cfg.Voice; v.Model != "" {
				transcriber = &voice.Whisper{Bin: v.Bin, Model: v.Model, FFmpeg: v.FFmpeg}
			}
			var synth voice.Synthesizer
			if v := cfg.Voice; v.TTSModel != "" {
				synth = &voice.Speaker{Bin: v.TTSBin, Model: v.TTSModel, FFmpeg: v.FFmpeg}
			}
			router := channel.NewRouter(st, engine, auditor, newAgent, transcriber, synth)
			cur, err := buildCurator(cfg, model)
			if err != nil {
				return err
			}
			router.Curate(cur)
			defer router.WaitIdle()

			channels := []channel.Channel{&webchat.WebChat{Addr: addr}}
			if tg := cfg.Channels.Telegram; tg.Token != "" {
				channels = append(channels, &telegram.Telegram{Token: tg.Token, Allow: tg.AllowChats})
			}
			if dc := cfg.Channels.Discord; dc.Token != "" {
				channels = append(channels, &discord.Discord{Token: dc.Token, Allow: dc.AllowChannels})
			}
			if sl := cfg.Channels.Slack; sl.AppToken != "" {
				channels = append(channels, &slack.Slack{AppToken: sl.AppToken, BotToken: sl.BotToken, Allow: sl.AllowChannels})
			}
			if im := cfg.Channels.IMessage; im.Enabled {
				channels = append(channels, &imessage.IMessage{Allow: im.AllowHandles, DBPath: im.DBPath})
			}

			fmt.Fprintf(out, "tomo serving on http://%s\n", addr)
			for _, ch := range channels {
				fmt.Fprintf(out, "  channel: %s\n", ch.Name())
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
