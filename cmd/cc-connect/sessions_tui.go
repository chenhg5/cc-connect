package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Styles
var (
	userStyle      = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("62")).Foreground(lipgloss.Color("230"))
	assistantStyle = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("22")).Foreground(lipgloss.Color("230"))
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	headerStyle    = lipgloss.NewStyle().Bold(true)
	footerStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

type viewState int

const (
	viewList viewState = iota
	viewDetail
)

const (
	detailHeaderLines = 3
	detailFooterLines = 1
)

type sessionsModel struct {
	state    viewState
	table    table.Model
	viewport viewport.Model
	records  []sessionRecord
	selected int
	width    int
	height   int
	ready    bool
}

func newSessionsModel(records []sessionRecord) sessionsModel {
	m := sessionsModel{
		state:   viewList,
		records: records,
	}
	return m
}

func (m sessionsModel) Init() tea.Cmd {
	return nil
}

func (m sessionsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		if !m.ready {
			m.table = m.buildTable()
			m.ready = true
		} else {
			m.table = m.buildTable()
		}

		if m.state == viewDetail {
			vpHeight := m.height - detailHeaderLines - detailFooterLines
			if vpHeight < 1 {
				vpHeight = 1
			}
			m.viewport.Width = m.width
			m.viewport.Height = vpHeight
			m.viewport.SetContent(renderDetailContent(m.records[m.selected]))
		}

		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit

		case "enter":
			if m.state == viewList && len(m.records) > 0 {
				m.selected = m.table.Cursor()
				m.state = viewDetail
				m.viewport = m.buildDetailViewport()
			}
			return m, nil

		case "esc", "backspace":
			if m.state == viewDetail {
				m.state = viewList
			}
			return m, nil
		}
	}

	var cmd tea.Cmd
	if m.state == viewList {
		m.table, cmd = m.table.Update(msg)
	} else {
		m.viewport, cmd = m.viewport.Update(msg)
	}
	return m, cmd
}

func (m sessionsModel) View() string {
	if !m.ready {
		return "Loading..."
	}

	if m.state == viewDetail {
		return m.viewDetail()
	}
	return m.viewList()
}

func (m sessionsModel) viewList() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Session Browser"))
	b.WriteString("\n\n")
	b.WriteString(m.table.View())
	b.WriteString("\n")
	b.WriteString(footerStyle.Render("  ↑/↓ navigate • enter view • q quit"))
	return b.String()
}

func (m sessionsModel) viewDetail() string {
	record := m.records[m.selected]

	var b strings.Builder
	b.WriteString(headerStyle.Render(fmt.Sprintf("Session: %s (%s)", record.GlobalID, record.Name)))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("Platform: %s | User: %s | Group: %s | Messages: %d",
		record.Platform, displayUser(record), displayGroup(record), record.Messages)))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", m.width)))
	b.WriteString("\n")
	b.WriteString(m.viewport.View())
	b.WriteString("\n")
	b.WriteString(footerStyle.Render(fmt.Sprintf(
		"  ESC back • ↑/↓ scroll • q quit    %.0f%%",
		m.viewport.ScrollPercent()*100,
	)))
	return b.String()
}

func (m sessionsModel) buildTable() table.Model {
	columns := m.calcColumns()

	rows := make([]table.Row, len(m.records))
	for i, r := range m.records {
		rows[i] = table.Row{
			fmt.Sprintf("%d", i+1),
			truncate(r.Project, columns[1].Width),
			truncate(r.Platform, columns[2].Width),
			truncate(displayUser(r), columns[3].Width),
			truncate(displayGroup(r), columns[4].Width),
			fmt.Sprintf("%d", r.Messages),
			r.LastActive.Format("2006-01-02 15:04"),
		}
	}

	height := m.height - 4
	if height < 1 {
		height = 1
	}

	t := table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(height),
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

	return t
}

func (m sessionsModel) calcColumns() []table.Column {
	// Fixed-width columns
	const (
		colNum       = 4
		colMsgs      = 6
		colLastTime  = 19
		fixedTotal   = colNum + colMsgs + colLastTime // 29
		separators   = 7                               // padding between 7 columns
	)

	remaining := m.width - fixedTotal - separators
	if remaining < 20 {
		remaining = 20
	}

	// Distribute: Project 28%, Platform 12%, User 20%, Group/Chat 40%
	colProject := remaining * 28 / 100
	colPlatform := remaining * 12 / 100
	colUser := remaining * 20 / 100
	colGroupChat := remaining - colProject - colPlatform - colUser

	if colProject < 8 {
		colProject = 8
	}
	if colPlatform < 8 {
		colPlatform = 8
	}
	if colUser < 6 {
		colUser = 6
	}
	if colGroupChat < 8 {
		colGroupChat = 8
	}

	return []table.Column{
		{Title: "#", Width: colNum},
		{Title: "Project", Width: colProject},
		{Title: "Platform", Width: colPlatform},
		{Title: "User", Width: colUser},
		{Title: "Group/Chat", Width: colGroupChat},
		{Title: "Msgs", Width: colMsgs},
		{Title: "Last Activity", Width: colLastTime},
	}
}

func (m sessionsModel) buildDetailViewport() viewport.Model {
	vpHeight := m.height - detailHeaderLines - detailFooterLines
	if vpHeight < 1 {
		vpHeight = 1
	}

	vp := viewport.New(m.width, vpHeight)
	vp.SetContent(renderDetailContent(m.records[m.selected]))
	return vp
}

func renderDetailContent(record sessionRecord) string {
	if len(record.History) == 0 {
		return dimStyle.Render("No messages.")
	}

	var b strings.Builder
	var lastDate string

	for _, entry := range record.History {
		date := entry.Timestamp.Format("2006-01-02")
		if date != lastDate {
			if lastDate != "" {
				b.WriteString("\n")
			}
			sep := fmt.Sprintf("─── %s ───", date)
			b.WriteString(dimStyle.Render(sep))
			b.WriteString("\n\n")
			lastDate = date
		}

		timeStr := dimStyle.Render(entry.Timestamp.Format("15:04"))

		var roleTag string
		switch entry.Role {
		case "user":
			roleTag = userStyle.Render(" user ")
		case "assistant":
			roleTag = assistantStyle.Render(" assistant ")
		default:
			roleTag = dimStyle.Render(fmt.Sprintf(" %s ", entry.Role))
		}

		// Wrap content lines
		content := entry.Content
		// Indent continuation lines (time=5 + space=2 + role≈12 + space=2 ≈ 21)
		const indent = 21
		lines := strings.Split(content, "\n")
		prefix := strings.Repeat(" ", indent)

		var contentParts []string
		for i, line := range lines {
			if i == 0 {
				contentParts = append(contentParts, line)
			} else {
				contentParts = append(contentParts, prefix+line)
			}
		}

		b.WriteString(fmt.Sprintf("%s  %s  %s\n", timeStr, roleTag, strings.Join(contentParts, "\n")))
	}

	return b.String()
}

func runSessionsTUI(dataDir string) {
	records, err := loadAllSessions(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(records) == 0 {
		fmt.Println("No sessions found.")
		return
	}

	m := newSessionsModel(records)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
