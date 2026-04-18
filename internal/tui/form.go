package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Field struct {
	Label       string
	Placeholder string
	Value       string // prefill
	Required    bool
	Validate    func(string) error
}

type FormModel struct {
	Title     string
	fields    []Field
	inputs    []textinput.Model
	focus     int
	cancelled bool
	submitted bool
	err       string
}

func NewForm(title string, fields []Field) FormModel {
	inputs := make([]textinput.Model, len(fields))
	for i, f := range fields {
		ti := textinput.New()
		ti.Placeholder = f.Placeholder
		ti.CharLimit = 120
		ti.Width = 50
		ti.SetValue(f.Value)
		if i == 0 {
			ti.Focus()
		}
		inputs[i] = ti
	}
	return FormModel{Title: title, fields: fields, inputs: inputs}
}

// Values returns the submitted values in field order. Empty if cancelled.
func (m FormModel) Values() []string {
	if m.cancelled || !m.submitted {
		return nil
	}
	out := make([]string, len(m.inputs))
	for i, in := range m.inputs {
		out[i] = strings.TrimSpace(in.Value())
	}
	return out
}

func (m FormModel) Cancelled() bool { return m.cancelled }

func (m FormModel) Init() tea.Cmd { return textinput.Blink }

func (m FormModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "tab", "down":
			m.advance(1)
			return m, nil
		case "shift+tab", "up":
			m.advance(-1)
			return m, nil
		case "enter":
			if m.focus == len(m.inputs)-1 {
				if err := m.validate(); err != nil {
					m.err = err.Error()
					return m, nil
				}
				m.submitted = true
				return m, tea.Quit
			}
			m.advance(1)
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
	return m, cmd
}

func (m *FormModel) advance(delta int) {
	m.inputs[m.focus].Blur()
	m.focus = (m.focus + delta + len(m.inputs)) % len(m.inputs)
	m.inputs[m.focus].Focus()
}

func (m FormModel) validate() error {
	for i, f := range m.fields {
		v := strings.TrimSpace(m.inputs[i].Value())
		if f.Required && v == "" {
			return fmt.Errorf("%s is required", f.Label)
		}
		if f.Validate != nil {
			if err := f.Validate(v); err != nil {
				return fmt.Errorf("%s: %v", f.Label, err)
			}
		}
	}
	return nil
}

var (
	formTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render
	formLabel = lipgloss.NewStyle().Bold(true).Render
	formFaint = lipgloss.NewStyle().Faint(true).Render
	formError = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render
)

func (m FormModel) View() string {
	if m.cancelled || m.submitted {
		return ""
	}
	var b strings.Builder
	b.WriteString(formTitle(m.Title))
	b.WriteString("\n\n")
	for i, f := range m.fields {
		b.WriteString(formLabel(f.Label))
		b.WriteString("\n")
		b.WriteString(m.inputs[i].View())
		b.WriteString("\n\n")
	}
	if m.err != "" {
		b.WriteString(formError(m.err))
		b.WriteString("\n")
	}
	b.WriteString(formFaint("Tab/↓ next · Shift+Tab/↑ prev · Enter submits last field · Esc cancels"))
	return b.String()
}
