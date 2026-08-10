// Command telescope is a terminal log viewer.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oteldb/telescope/internal/ui"
)

func main() {
	p := tea.NewProgram(ui.New(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "telescope:", err)
		os.Exit(1)
	}
}
