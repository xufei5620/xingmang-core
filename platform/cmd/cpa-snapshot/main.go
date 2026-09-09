package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/xufei5620/xingmang-platform/internal/platform/buildinfo"
	"github.com/xufei5620/xingmang-platform/internal/platform/cpasnapshot"
)

const (
	defaultSourcePath = "/root/cpa-stack/cpam-data/usage.sqlite"
	defaultTargetPath = "/var/lib/xingmang/cpa-snapshot/published/usage.sqlite"
	defaultGroupID    = 10001
)

type commandConfig struct {
	action  string
	source  string
	target  string
	groupID int
}

type commandDeps struct {
	publish func(context.Context, cpasnapshot.Options) (cpasnapshot.Metadata, error)
	verify  func(context.Context, string) (cpasnapshot.Metadata, error)
	version func() (string, string)
}

func main() {
	deps := commandDeps{
		publish: cpasnapshot.Publish,
		verify:  cpasnapshot.Verify,
		version: func() (string, string) { return buildinfo.Version, buildinfo.Commit },
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, deps); err != nil {
		fmt.Fprintf(os.Stderr, "cpa-snapshot: operation failed: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer, deps commandDeps) error {
	config, err := parseCommand(args)
	if err != nil {
		return err
	}
	if output == nil {
		return errors.New("cpa-snapshot: command dependencies are unavailable")
	}
	if config.action == "version" {
		if deps.version == nil {
			return errors.New("cpa-snapshot: version dependency is unavailable")
		}
		version, commit := deps.version()
		_, err = fmt.Fprintf(output, "%s %s\n", version, commit)
		return err
	}
	if deps.publish == nil || deps.verify == nil {
		return errors.New("cpa-snapshot: command dependencies are unavailable")
	}
	var metadata cpasnapshot.Metadata
	status := "verified"
	switch config.action {
	case "publish":
		metadata, err = deps.publish(ctx, cpasnapshot.Options{
			SourcePath: config.source,
			TargetPath: config.target,
			GroupID:    config.groupID,
		})
		status = "published"
	case "verify":
		metadata, err = deps.verify(ctx, config.target)
	default:
		return errors.New("cpa-snapshot: unsupported action")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]string{
		"status":      status,
		"generation":  metadata.Generation,
		"observed_at": metadata.ObservedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	})
}

func parseCommand(args []string) (commandConfig, error) {
	if len(args) == 0 {
		return commandConfig{}, errors.New("cpa-snapshot: action must be publish, verify, or version")
	}
	config := commandConfig{
		action: args[0], source: defaultSourcePath, target: defaultTargetPath,
		groupID: defaultGroupID,
	}
	set := flag.NewFlagSet("cpa-snapshot "+config.action, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	switch config.action {
	case "publish":
		set.StringVar(&config.source, "source", defaultSourcePath, "active CPA SQLite source")
		set.StringVar(&config.target, "target", defaultTargetPath, "published snapshot path")
		set.IntVar(&config.groupID, "group-id", defaultGroupID, "published snapshot reader group")
	case "verify":
		set.StringVar(&config.target, "target", defaultTargetPath, "published snapshot path")
	case "version":
	default:
		return commandConfig{}, errors.New("cpa-snapshot: action must be publish, verify, or version")
	}
	if err := set.Parse(args[1:]); err != nil || set.NArg() != 0 {
		return commandConfig{}, errors.New("cpa-snapshot: invalid arguments")
	}
	config.source = cleanCommandPath(config.source)
	config.target = cleanCommandPath(config.target)
	if config.action == "publish" && (!absoluteCommandPath(config.source) || strings.ContainsAny(config.source, "\x00?#%")) {
		return commandConfig{}, errors.New("cpa-snapshot: source must be an absolute safe path")
	}
	if !absoluteCommandPath(config.target) || strings.ContainsAny(config.target, "\x00?#%") || path.Base(filepath.ToSlash(config.target)) != "usage.sqlite" {
		return commandConfig{}, errors.New("cpa-snapshot: target must be an absolute usage.sqlite path")
	}
	if config.action == "publish" && (config.groupID < 1 || config.groupID > 60000) {
		return commandConfig{}, errors.New("cpa-snapshot: group-id is outside the allowed range")
	}
	return config, nil
}

func cleanCommandPath(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "/") {
		return path.Clean(value)
	}
	return filepath.Clean(value)
}

func absoluteCommandPath(value string) bool {
	return strings.HasPrefix(value, "/") || filepath.IsAbs(value)
}
