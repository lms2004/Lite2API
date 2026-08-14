package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/lms2004/lite2api/internal/config"
	"github.com/lms2004/lite2api/internal/gateway"
)

var version = "dev"

func main() {
	configPath := flag.String("config", config.DefaultConfigPath, "path to config.json")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	g, err := gateway.New(*configPath)
	if err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
	g.LogState()
	if err := g.Run(context.Background()); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
