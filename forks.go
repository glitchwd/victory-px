package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var forkNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,39}$`)

const (
	nvidiaRTForkName = "nvidia-rt"
	nvidiaRTRepo     = "Kemerd/VIctory-NVIDIA-RT"
)

type forkBuild struct {
	Name   string `json:"-"`
	Dir    string `json:"directory"`
	Binary string `json:"binary"`
}

func (build forkBuild) label() string {
	if build.Name == "" {
		return "Command line"
	}
	if build.Name == nvidiaRTForkName {
		return "VIctory NVIDIA-RT"
	}
	return build.Name
}

func (build forkBuild) releaseRepository() string {
	if build.Name == nvidiaRTForkName {
		return nvidiaRTRepo
	}
	return ""
}

func (cfg *config) selectFork(build forkBuild) error {
	if build.Dir == "" {
		return errors.New("set --victory-dir (or VICTORY_DIR)")
	}
	dir, err := filepath.Abs(build.Dir)
	if err != nil {
		return fmt.Errorf("--victory-dir: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("--victory-dir: %w", err)
	}
	if !info.IsDir() {
		return errors.New("--victory-dir must be a directory")
	}
	if build.Binary == "" {
		build.Binary = filepath.Join(dir, diagBinary)
	}
	binary, err := regularFile(build.Binary)
	if err != nil {
		return fmt.Errorf("VIctory binary: %w; build VIctory or set --victory-bin", err)
	}
	cfg.forkName, cfg.dir, cfg.binary = build.Name, dir, binary
	return nil
}

func forkConfigDir() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "victory-px", "forks"), nil
}

func loadForkBuilds() ([]forkBuild, error) {
	dir, err := forkConfigDir()
	if err != nil {
		return nil, err
	}
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var builds []forkBuild
	for _, file := range files {
		name, ok := strings.CutSuffix(file.Name(), ".json")
		if !ok || !file.Type().IsRegular() || !forkNamePattern.MatchString(name) {
			continue
		}
		build := forkBuild{Name: name}
		data, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err == nil {
			err = json.Unmarshal(data, &build)
		}
		if err == nil && (!filepath.IsAbs(build.Dir) || !filepath.IsAbs(build.Binary)) {
			err = errors.New("expected absolute directory and binary paths")
		}
		if err != nil {
			return nil, fmt.Errorf("read fork %q: %w", name, err)
		}
		builds = append(builds, build)
	}
	return builds, nil
}

func saveForkBuild(build forkBuild) error {
	if !forkNamePattern.MatchString(build.Name) {
		return errors.New("invalid fork name")
	}
	var cfg config
	if err := cfg.selectFork(build); err != nil {
		return err
	}
	build.Dir, build.Binary = cfg.dir, cfg.binary
	dir, err := forkConfigDir()
	if err != nil {
		return err
	}
	return writeLocalConfig(filepath.Join(dir, build.Name+".json"), build)
}

func gameConfigPath() (string, error) {
	dir, err := forkConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(dir), "game.json"), nil
}

func loadGamePath() string {
	path, err := gameConfigPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var game string
	if json.Unmarshal(data, &game) != nil || !filepath.IsAbs(game) {
		return ""
	}
	var cfg config
	if cfg.selectTitle(game) != nil {
		return ""
	}
	return cfg.title
}

func writeLocalConfig(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".config-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	writeErr := json.NewEncoder(file).Encode(value)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
