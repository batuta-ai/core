module github.com/batuta-ai/core

go 1.26.4

require (
	charm.land/bubbletea/v2 v2.0.9
	github.com/charmbracelet/x/exp/teatest/v2 v2.0.0-20260906004030-3986e9119cf9
	github.com/charmbracelet/x/term v0.2.2
	github.com/pelletier/go-toml/v2 v2.4.3
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/aymanbagabas/go-udiff v0.3.1 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260703014108-f5a850f9c2b7 // indirect
	github.com/charmbracelet/x/ansi v0.11.7 // indirect
	github.com/charmbracelet/x/exp/golden v0.0.0-20251109135125-8916d276318f // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.0 // indirect
	github.com/mattn/go-runewidth v0.0.23 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
)

// v1.0.0 and v1.0.1 were published before the beta line; the module stays
// pre-release (v1.1.0-beta.N) until the API stabilizes.
retract [v1.0.0, v1.0.1]
