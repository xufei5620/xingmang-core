package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"invoice-system/backend/internal/document"
	"invoice-system/backend/internal/pdfscanner"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "check the isolated scanner socket and qpdf policy")
	socket := flag.String("socket", pdfscanner.DefaultSocketPath, "Unix socket path")
	tokenFile := flag.String("token-file", pdfscanner.DefaultTokenFile, "ephemeral capability file")
	tempDirectory := flag.String("temp-dir", pdfscanner.DefaultTempDirectory, "private tmpfs scan directory")
	flag.Parse()

	client := document.SidecarScanner{SocketPath: *socket, TokenFile: *tokenFile, Timeout: 5 * time.Second}
	if *healthcheck {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Ping(ctx); err != nil {
			slog.Error("PDF scanner healthcheck failed")
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &pdfscanner.Server{
		SocketPath: *socket, TokenFile: *tokenFile, TempDirectory: *tempDirectory,
		MaxBytes: document.DefaultMaxPDFBytes, Timeout: 18 * time.Second,
		MaxConcurrent: 2, Scanner: document.QPDFPolicyScanner{},
	}
	if err := server.Run(ctx); err != nil {
		slog.Error("isolated PDF scanner stopped", "error", err)
		os.Exit(1)
	}
}
