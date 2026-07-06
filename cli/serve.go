package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/channel"
	"github.com/tamnd/tomo/pkg/channel/telegram"
	"github.com/tamnd/tomo/pkg/channel/webchat"
	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/store"
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

			newAgent := func() (*agent.Agent, error) {
				a, _, err := buildAgent(cfg, model, nil)
				return a, err
			}
			router := channel.NewRouter(st, engine, auditor, newAgent)

			channels := []channel.Channel{&webchat.WebChat{Addr: addr}}
			if tg := cfg.Channels.Telegram; tg.Token != "" {
				channels = append(channels, &telegram.Telegram{Token: tg.Token, Allow: tg.AllowChats})
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "tomo serving on http://%s\n", addr)
			for _, ch := range channels {
				fmt.Fprintf(out, "  channel: %s\n", ch.Name())
			}
			return runChannels(cmd, router, channels)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8765", "web chat listen address")
	cmd.Flags().StringVarP(&model, "model", "m", "", "provider/model (default from config)")
	return cmd
}

// runChannels starts every channel and blocks until the context is cancelled.
// The first channel error is returned after all have stopped.
func runChannels(cmd *cobra.Command, router *channel.Router, channels []channel.Channel) error {
	ctx := cmd.Context()
	var wg sync.WaitGroup
	errs := make(chan error, len(channels))
	for _, ch := range channels {
		wg.Add(1)
		go func(c channel.Channel) {
			defer wg.Done()
			if err := c.Run(ctx, router.HandlerFor(c.Name())); err != nil {
				errs <- fmt.Errorf("%s: %w", c.Name(), err)
			}
		}(ch)
	}
	wg.Wait()
	close(errs)
	return <-errs
}
