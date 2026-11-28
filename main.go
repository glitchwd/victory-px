package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
)

const diagBinary = "target/x86_64-pc-windows-msvc/diag/victory.exe"

var releaseVersion = "v0.2.0"

type config struct {
	dir, binary, title, wine         string
	forkName, saveFork, runtimeLabel string
	forks                            []forkBuild
	listForks                        bool
	seriesS, gamescope, saveLog      bool
	gamemode, dxvkAsync              bool
	checkUpdates                     bool
	display                          displayOptions
}

func parseFlags(args []string, out io.Writer, getenv func(string) string) (config, bool, error) {
	var cfg config
	var version, fsr bool
	var preset, gameSize, windowSize string
	flags := flag.NewFlagSet("victory-px", flag.ContinueOnError)
	flags.SetOutput(out)
	flags.Usage = func() {
		fmt.Fprintln(out, "Usage: victory-px [--fork NAME | --victory-dir DIR] --exe FILE [options]")
		fmt.Fprintln(out, "       victory-px --save-fork NAME --victory-dir DIR [--victory-bin FILE]")
		fmt.Fprintln(out, "       victory-px --list-forks")
		flags.PrintDefaults()
	}
	// Apply environment defaults after parsing so help cannot disclose their values.
	flags.StringVar(&cfg.dir, "victory-dir", "", "VIctory working directory (or VICTORY_DIR)")
	flags.StringVar(&cfg.title, "exe", "", "Linux path to the title executable (or VICTORY_EXE)")
	flags.StringVar(&cfg.binary, "victory-bin", "", "Linux path to victory.exe (defaults to the diag build under --victory-dir)")
	flags.StringVar(&cfg.forkName, "fork", "", "Start with the named saved VIctory build")
	flags.StringVar(&cfg.saveFork, "save-fork", "", "Save the VIctory directory and binary under NAME, then exit")
	flags.BoolVar(&cfg.listForks, "list-forks", false, "List saved VIctory builds and exit")
	flags.StringVar(&cfg.wine, "wine", "", "Wine executable (defaults to wine, then wine64 on PATH)")
	flags.StringVar(&cfg.runtimeLabel, "runtime-label", "", "Name of the selected Wine or Proton runtime for display")
	flags.BoolVar(&cfg.seriesS, "series-s", false, "Use the Series S console profile")
	flags.BoolVar(&cfg.gamescope, "gamescope", false, "Start the title inside Gamescope")
	flags.BoolVar(&cfg.gamemode, "gamemode", false, "Wrap the session with gamemoderun")
	flags.BoolVar(&cfg.dxvkAsync, "dxvk-async", false, "Enable DXVK_ASYNC=1 and DXVK_STATE_CACHE=1")
	flags.StringVar(&preset, "display-preset", "small", "Gamescope preset: small, upscale or 1080p")
	flags.StringVar(&gameSize, "game-size", "", "Override the Gamescope game resolution (WIDTHxHEIGHT)")
	flags.StringVar(&windowSize, "window-size", "", "Override the Gamescope output size (WIDTHxHEIGHT)")
	flags.BoolVar(&fsr, "fsr", false, "Override the preset's FSR setting")
	flags.BoolVar(&cfg.display.fullscreen, "fullscreen", false, "Start Gamescope in fullscreen")
	flags.IntVar(&cfg.display.fpsLimit, "fps-limit", 0, "Gamescope frame rate limit (0 for unlimited)")
	flags.BoolVar(&cfg.saveLog, "save-log", false, "Save a private local log with paths hidden")
	flags.BoolVar(&cfg.checkUpdates, "check-updates", false, "Check the selected checkout on GitHub at startup and every 15 minutes")
	flags.BoolVar(&version, "version", false, "Print version and exit")
	if err := flags.Parse(args); err != nil {
		return cfg, false, err
	}
	if flags.NArg() != 0 {
		err := errors.New("unexpected positional arguments; see --help")
		fmt.Fprintln(out, "victory-px:", err)
		return cfg, false, err
	}
	set := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	flagError := func(err error) (config, bool, error) {
		fmt.Fprintln(out, "victory-px:", err)
		return cfg, false, err
	}
	if set["fork"] && (set["victory-dir"] || set["victory-bin"] || set["save-fork"] || cfg.listForks) {
		return flagError(errors.New("--fork selects a saved build and cannot be combined with --victory-dir, --victory-bin, --save-fork or --list-forks"))
	}
	if cfg.listForks && set["save-fork"] {
		return flagError(errors.New("--save-fork and --list-forks cannot be combined"))
	}
	for _, option := range []string{"fork", "save-fork"} {
		if set[option] && !forkNamePattern.MatchString(flags.Lookup(option).Value.String()) {
			return flagError(fmt.Errorf("--%s needs a name of 1–40 letters, digits, dots, underscores or hyphens, starting with a letter or digit", option))
		}
	}
	if !cfg.gamescope {
		for _, name := range []string{"display-preset", "game-size", "window-size", "fsr", "fullscreen", "fps-limit"} {
			if set[name] {
				return flagError(fmt.Errorf("--%s requires --gamescope", name))
			}
		}
	}
	presetIndex := -1
	for i, p := range displayPresets {
		if p.name == preset {
			presetIndex = i
			break
		}
	}
	if presetIndex < 0 {
		return flagError(errors.New("--display-preset must be small, upscale or 1080p"))
	}
	cfg.display.applyPreset(presetIndex)
	for _, size := range []struct {
		flag, value string
		dst         *resolution
	}{
		{"game-size", gameSize, &cfg.display.game},
		{"window-size", windowSize, &cfg.display.window},
	} {
		if set[size.flag] {
			res, err := parseResolution(size.value)
			if err != nil {
				return flagError(fmt.Errorf("--%s: %w", size.flag, err))
			}
			*size.dst = res
		}
	}
	if set["fsr"] {
		cfg.display.fsr = fsr
	}
	if cfg.display.fpsLimit < 0 || cfg.display.fpsLimit > 1000 {
		return flagError(errors.New("--fps-limit must be between 0 and 1000"))
	}
	if !set["victory-dir"] {
		cfg.dir = getenv("VICTORY_DIR")
	}
	if !set["exe"] {
		cfg.title = getenv("VICTORY_EXE")
	}
	return cfg, version, nil
}

func (cfg config) validate() (config, error) {
	if cfg.dir != "" || cfg.binary != "" {
		if err := cfg.selectFork(forkBuild{Name: cfg.forkName, Dir: cfg.dir, Binary: cfg.binary}); err != nil {
			return cfg, err
		}
	}
	if cfg.title != "" {
		if err := cfg.selectTitle(cfg.title); err != nil {
			return cfg, err
		}
	}
	var err error
	if cfg.wine != "" {
		cfg.wine, err = exec.LookPath(cfg.wine)
		if err != nil {
			return cfg, fmt.Errorf("--wine: %w", err)
		}
	} else {
		for _, name := range []string{"wine", "wine64"} {
			if cfg.wine, err = exec.LookPath(name); err == nil {
				break
			}
		}
		if err != nil {
			return cfg, errors.New("Wine was not found; install Wine or set --wine")
		}
	}
	if cfg.wine, err = filepath.Abs(cfg.wine); err != nil {
		return cfg, fmt.Errorf("resolve Wine executable: %w", err)
	}
	if err := checkWineDrive(); err != nil {
		return cfg, err
	}
	if cfg.gamescope {
		if _, err := exec.LookPath("gamescope"); err != nil {
			return cfg, fmt.Errorf("--gamescope: %w", err)
		}
	}
	return cfg, nil
}

func (cfg *config) selectTitle(path string) error {
	if _, err := winePath(path); err != nil {
		return fmt.Errorf("--exe expects a Linux path: %w", err)
	}
	title, err := regularFile(path)
	if err != nil {
		return fmt.Errorf("--exe: %w", err)
	}
	cfg.title = title
	return nil
}

func regularFile(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a regular file", path)
	}
	return path, nil
}

func winePath(path string) (string, error) {
	if strings.ContainsAny(path, "\\:") {
		return "", errors.New("Wine paths cannot contain a literal backslash or colon")
	}
	return `Z:` + strings.ReplaceAll(path, "/", `\`), nil
}

func checkWineDrive() error {
	prefix := os.Getenv("WINEPREFIX")
	if prefix == "" {
		dir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("find default Wine prefix: %w", err)
		}
		prefix = filepath.Join(dir, ".wine")
	}
	if !filepath.IsAbs(prefix) {
		return errors.New("WINEPREFIX must be an absolute Linux path")
	}
	devices := filepath.Join(prefix, "dosdevices")
	if _, err := os.Stat(devices); errors.Is(err, os.ErrNotExist) {
		return nil // Wine creates the default drive mapping when it initializes a prefix.
	} else if err != nil {
		return fmt.Errorf("inspect Wine prefix: %w", err)
	}
	mapped, err := os.Stat(filepath.Join(devices, "z:"))
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("the Wine prefix needs a Z: drive mapped to /")
	}
	if err != nil {
		return fmt.Errorf("inspect Wine Z: drive: %w", err)
	}
	root, err := os.Stat("/")
	if err != nil {
		return err
	}
	if !os.SameFile(root, mapped) {
		return errors.New("the Wine prefix needs a Z: drive mapped to /")
	}
	return nil
}

func version() string {
	if releaseVersion != "" {
		return releaseVersion
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	var usage strings.Builder
	cfg, showVersion, err := parseFlags(args, &usage, os.Getenv)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(stdout, usage.String())
		return 0
	case err != nil:
		fmt.Fprint(stderr, usage.String())
		return 2
	case showVersion:
		fmt.Fprintln(stdout, "victory-px", version())
		return 0
	}
	if cfg.saveFork != "" {
		err = saveForkBuild(forkBuild{Name: cfg.saveFork, Dir: cfg.dir, Binary: cfg.binary})
		if err != nil {
			fmt.Fprintln(stderr, "victory-px:", err)
			return 2
		}
		fmt.Fprintln(stdout, "Saved fork", cfg.saveFork)
		return 0
	}
	cfg.forks, err = loadForkBuilds()
	if err != nil {
		fmt.Fprintln(stderr, "victory-px:", err)
		if cfg.listForks || cfg.forkName != "" || cfg.dir == "" {
			return 2
		}
	}
	if cfg.listForks {
		for _, build := range cfg.forks {
			fmt.Fprintln(stdout, build.Name)
		}
		return 0
	}
	if cfg.forkName != "" {
		var found bool
		for _, build := range cfg.forks {
			if build.Name == cfg.forkName {
				cfg.dir, cfg.binary = build.Dir, build.Binary
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(stderr, "victory-px: unknown fork %q; run --list-forks\n", cfg.forkName)
			return 2
		}
	} else if cfg.dir == "" && cfg.binary == "" && len(cfg.forks) > 0 {
		for _, build := range cfg.forks {
			if err = cfg.selectFork(build); err == nil {
				break
			}
		}
	}
	if cfg.title == "" {
		cfg.title = loadGamePath()
	}
	cfg, err = cfg.validate()
	if err != nil {
		fmt.Fprintln(stderr, "victory-px:", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	var workers sync.WaitGroup
	defer func() {
		stop()
		workers.Wait()
	}()
	m := newModel(ctx, &workers, cfg)
	_, err = tea.NewProgram(m, tea.WithContext(ctx), tea.WithoutSignalHandler(), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	if err != nil && !(ctx.Err() != nil && errors.Is(err, tea.ErrProgramKilled)) {
		fmt.Fprintln(stderr, "victory-px:", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}
