package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/example/app-runner/internal/previewgateway"
)

type repeatedFlag []string

func (values *repeatedFlag) String() string {
	return strings.Join(*values, ",")
}

func (values *repeatedFlag) Set(value string) error {
	if value == "" {
		return errors.New("value must not be empty")
	}
	*values = append(*values, value)
	return nil
}

func parsePreviewGatewayFlags(args []string) (previewgateway.Config, error) {
	set := flag.NewFlagSet("preview-gateway", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var cfg previewgateway.Config
	var allow repeatedFlag
	set.StringVar(&cfg.IngressListen, "ingress-listen", "", "")
	set.StringVar(&cfg.IngressTarget, "ingress-target", "", "")
	set.StringVar(&cfg.EgressListen, "egress-listen", "", "")
	set.Var(&allow, "allow", "")
	if err := set.Parse(args); err != nil {
		return previewgateway.Config{}, err
	}
	if set.NArg() != 0 {
		return previewgateway.Config{}, errors.New("preview-gateway takes no positional arguments")
	}
	if cfg.IngressListen == "" || cfg.IngressTarget == "" {
		return previewgateway.Config{}, errors.New("preview-gateway requires --ingress-listen and --ingress-target")
	}
	if len(allow) > 0 && cfg.EgressListen == "" {
		return previewgateway.Config{}, errors.New("preview-gateway --allow requires --egress-listen")
	}
	cfg.Allow = allow
	return cfg, nil
}

func cmdPreviewGateway(args []string) error {
	cfg, err := parsePreviewGatewayFlags(args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return previewgateway.Run(ctx, cfg, net.DefaultResolver, &net.Dialer{Timeout: 10 * time.Second}, logger)
}
