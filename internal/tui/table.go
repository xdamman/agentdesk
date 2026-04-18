package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TableAction is a keypress-triggered action shown in the table footer. When
// pressed on a selected row, the model quits and exposes both the row's column-0
// value via Selected() and the action Key via Action().
type TableAction struct {
	Key   string // keypress, e.g. "a"
	Label string // human label, e.g. "accept"
}

// TableModel is a reusable read-only table TUI.
type TableModel struct {
	Title    string
	Table    table.Model
	Actions  []TableAction
	selected string
	action   string
}

func NewTable(title string, columns []table.Column, rows []table.Row) TableModel {
	t := table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(minInt(12, max(3, len(rows)+1))),
	)
	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240")).
		BorderBottom(true).
		Bold(true)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("57")).
		Bold(false)
	t.SetStyles(s)
	return TableModel{Title: title, Table: t}
}

// WithActions returns the model with action keybindings attached.
func (m TableModel) WithActions(actions ...TableAction) TableModel {
	m.Actions = actions
	return m
}

// Selected returns the column-0 value of the highlighted row at exit.
func (m TableModel) Selected() string { return m.selected }

// Action returns the action key that caused exit ("" if Enter or quit).
func (m TableModel) Action() string { return m.action }

func (m TableModel) Init() tea.Cmd { return nil }

func (m TableModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		keyStr := k.String()
		switch keyStr {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "enter":
			if row := m.Table.SelectedRow(); row != nil {
				m.selected = row[0]
			}
			return m, tea.Quit
		}
		for _, a := range m.Actions {
			if keyStr == a.Key {
				if row := m.Table.SelectedRow(); row != nil {
					m.selected = row[0]
					m.action = a.Key
					return m, tea.Quit
				}
			}
		}
	}
	var cmd tea.Cmd
	m.Table, cmd = m.Table.Update(msg)
	return m, cmd
}

var tableTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).MarginBottom(1).Render
var tableHintStyle = lipgloss.NewStyle().Faint(true).MarginTop(1).Render

func (m TableModel) View() string {
	hint := "↑/↓ to move · Enter to select · q to quit"
	if len(m.Actions) > 0 {
		parts := make([]string, 0, len(m.Actions))
		for _, a := range m.Actions {
			parts = append(parts, "["+a.Key+"] "+a.Label)
		}
		hint = strings.Join(parts, " · ") + "   " + hint
	}
	return tableTitleStyle(m.Title) + "\n" + m.Table.View() + "\n" + tableHintStyle(hint)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
