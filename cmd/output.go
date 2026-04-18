package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/bubbles/table"
	"github.com/mattn/go-isatty"
)

// isTTY returns true if stdout is a terminal.
func isTTY() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}

// interactive returns true when we should render the Bubble Tea UI:
// stdout is a TTY and the caller didn't pass --json.
func interactive(jsonFlag bool) bool {
	return !jsonFlag && isTTY()
}

// printPlainTable writes a tab-aligned plain-text table to stdout.
func printPlainTable(cols []table.Column, rows []table.Row) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = c.Title
	}
	fmt.Fprintln(w, strings.Join(headers, "\t"))
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	w.Flush()
}

// printJSON writes pretty-printed JSON to stdout.
func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
