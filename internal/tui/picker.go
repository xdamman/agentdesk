package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Picker is a minimal single-select list for short option lists.
type Picker struct {
	Title     string
	Items     []string
	cursor    int
	selected  string
	cancelled bool
}

func NewPicker(title string, items []string) Picker {
	return Picker{Title: title, Items: items}
}

func (m Picker) Selected() string  { return m.selected }
func (m Picker) Cancelled() bool   { return m.cancelled }
func (m Picker) Init() tea.Cmd     { return nil }

func (m Picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "ctrl+c", "esc", "q":
			m.cancelled = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.Items)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.Items) > 0 {
				m.selected = m.Items[m.cursor]
			}
			return m, tea.Quit
		}
	}
	return m, nil
}

var (
	pickerTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render
	pickerActive = lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57")).Render
	pickerHint   = lipgloss.NewStyle().Faint(true).Render
)

func (m Picker) View() string {
	if m.selected != "" || m.cancelled {
		return ""
	}
	var b strings.Builder
	b.WriteString(pickerTitle(m.Title))
	b.WriteString("\n\n")
	for i, it := range m.Items {
		prefix := "  "
		line := it
		if i == m.cursor {
			prefix = "▸ "
			line = pickerActive(" " + it + " ")
		}
		b.WriteString(prefix)
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(pickerHint("↑/↓ to move · Enter to select · q/Esc to cancel"))
	return b.String()
}
