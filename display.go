package main

import (
	"fmt"
	"strconv"
	"strings"
)

type resolution struct {
	width, height int
}

func (r resolution) String() string {
	return fmt.Sprintf("%dx%d", r.width, r.height)
}

func parseResolution(s string) (resolution, error) {
	w, h, ok := strings.Cut(s, "x")
	width, widthErr := strconv.ParseUint(w, 10, 16)
	height, heightErr := strconv.ParseUint(h, 10, 16)
	if !ok || widthErr != nil || heightErr != nil || width == 0 || height == 0 {
		return resolution{}, fmt.Errorf("expected a resolution WIDTHxHEIGHT with positive dimensions")
	}
	return resolution{int(width), int(height)}, nil
}

type displayOptions struct {
	game, window    resolution
	fsr, fullscreen bool
	fpsLimit        int
}

var displayPresets = [...]struct {
	name         string
	game, window resolution
	fsr          bool
}{
	{"small", resolution{1280, 720}, resolution{1280, 720}, false},
	{"upscale", resolution{1280, 720}, resolution{1920, 1080}, true},
	{"1080p", resolution{1920, 1080}, resolution{1920, 1080}, false},
}

func (d *displayOptions) applyPreset(index int) {
	p := displayPresets[index]
	d.game, d.window, d.fsr = p.game, p.window, p.fsr
}

func (d displayOptions) presetIndex() int {
	for i, p := range displayPresets {
		if d.game == p.game && d.window == p.window && d.fsr == p.fsr {
			return i
		}
	}
	return -1
}

func (d displayOptions) gamescopeArgs() []string {
	args := []string{
		"-w", strconv.Itoa(d.game.width), "-h", strconv.Itoa(d.game.height),
		"-W", strconv.Itoa(d.window.width), "-H", strconv.Itoa(d.window.height),
	}
	if d.fsr {
		args = append(args, "-F", "fsr")
	}
	if d.fullscreen {
		args = append(args, "-f")
	}
	if d.fpsLimit > 0 {
		args = append(args, "--framerate-limit", strconv.Itoa(d.fpsLimit))
	}
	return args
}

var displaySizes = [...]resolution{
	{960, 540}, {1280, 720}, {1280, 800}, {1600, 900}, {1920, 1080}, {1920, 1200},
}

func nextDisplaySize(current resolution) resolution {
	for i, size := range displaySizes {
		if size == current {
			return displaySizes[(i+1)%len(displaySizes)]
		}
	}
	return displaySizes[0]
}

func (d *displayOptions) change(key string) {
	switch key {
	case "p":
		d.applyPreset((d.presetIndex() + 1) % len(displayPresets))
	case "i":
		d.game = nextDisplaySize(d.game)
	case "o":
		d.window = nextDisplaySize(d.window)
	case "a":
		d.fsr = !d.fsr
	case "l":
		switch d.fpsLimit {
		case 0:
			d.fpsLimit = 30
		case 30:
			d.fpsLimit = 60
		default:
			d.fpsLimit = 0
		}
	case "enter":
		d.fullscreen = !d.fullscreen
	}
}
