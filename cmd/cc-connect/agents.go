package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

type agentsOptions struct {
	list       bool
	project    string
	jsonOutput bool
}

type agentSessionRecord struct {
	Project string
	core.AgentSessionInfo
}

func parseAgentsArgs(args []string) (agentsOptions, error) {
	opts := agentsOptions{list: true}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "list":
			opts.list = true
		case "--project", "-p":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return agentsOptions{}, fmt.Errorf("agents: --project requires a value")
			}
			i++
			opts.project = args[i]
		case "--json":
			opts.jsonOutput = true
		case "--help", "-h":
			return agentsOptions{}, flag.ErrHelp
		default:
			return agentsOptions{}, fmt.Errorf("agents: unknown argument %q", args[i])
		}
	}
	return opts, nil
}

func runAgents(args []string, configPathOverride string) {
	opts, err := parseAgentsArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		printAgentsUsage()
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		printAgentsUsage()
		os.Exit(1)
	}

	configPath := resolveConfigPath(configPathOverride)
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config (%s): %v\n", configPath, err)
		os.Exit(1)
	}
	config.ConfigPath = configPath

	records, err := collectCodexAgentSessions(context.Background(), cfg, opts.project)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
	if opts.jsonOutput {
		out, err := renderAgentSessionsJSON(records)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error rendering sessions:", err)
			os.Exit(1)
		}
		fmt.Print(out)
		return
	}
	fmt.Print(renderAgentSessionsTable(records))
}

func collectCodexAgentSessions(ctx context.Context, cfg *config.Config, projectFilter string) ([]agentSessionRecord, error) {
	if cfg == nil {
		return nil, fmt.Errorf("agents: config is nil")
	}
	var records []agentSessionRecord
	var codexProjects int
	var matchedProject bool
	for _, project := range cfg.Projects {
		if projectFilter != "" && project.Name != projectFilter {
			continue
		}
		if projectFilter != "" {
			matchedProject = true
		}
		if !strings.EqualFold(strings.TrimSpace(project.Agent.Type), "codex") {
			continue
		}
		codexProjects++
		agent, err := core.CreateAgent(project.Agent.Type, buildAgentOptions(cfg.DataDir, project))
		if err != nil {
			return nil, fmt.Errorf("agents: create project %q: %w", project.Name, err)
		}
		lister, ok := agent.(core.AllAgentSessionsLister)
		if !ok {
			_ = agent.Stop()
			return nil, fmt.Errorf("agents: project %q does not support all-session listing", project.Name)
		}
		sessions, err := lister.ListAllSessions(ctx)
		_ = agent.Stop()
		if err != nil {
			return nil, fmt.Errorf("agents: list project %q: %w", project.Name, err)
		}
		for _, session := range sessions {
			records = append(records, agentSessionRecord{Project: project.Name, AgentSessionInfo: session})
		}
	}
	if projectFilter != "" && !matchedProject {
		return nil, fmt.Errorf("agents: project %q not found", projectFilter)
	}
	if codexProjects == 0 {
		if projectFilter != "" {
			return nil, fmt.Errorf("agents: project %q is not configured with agent type codex", projectFilter)
		}
		return nil, fmt.Errorf("agents: no project configured with agent type codex")
	}
	return mergeAgentSessions(records), nil
}

func mergeAgentSessions(records []agentSessionRecord) []agentSessionRecord {
	byID := make(map[string]agentSessionRecord, len(records))
	for _, record := range records {
		if previous, ok := byID[record.ID]; !ok || record.ModifiedAt.After(previous.ModifiedAt) {
			byID[record.ID] = record
		}
	}
	merged := make([]agentSessionRecord, 0, len(byID))
	for _, record := range byID {
		merged = append(merged, record)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].ModifiedAt.After(merged[j].ModifiedAt)
	})
	return merged
}

func renderAgentSessionsJSON(records []agentSessionRecord) (string, error) {
	type jsonRecord struct {
		Project      string    `json:"project,omitempty"`
		ID           string    `json:"id"`
		Summary      string    `json:"summary,omitempty"`
		WorkDir      string    `json:"work_dir,omitempty"`
		MessageCount int       `json:"message_count"`
		ModifiedAt   time.Time `json:"modified_at"`
	}
	out := make([]jsonRecord, 0, len(records))
	for _, record := range records {
		out = append(out, jsonRecord{
			Project:      record.Project,
			ID:           record.ID,
			Summary:      record.Summary,
			WorkDir:      record.WorkDir,
			MessageCount: record.MessageCount,
			ModifiedAt:   record.ModifiedAt,
		})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

func renderAgentSessionsTable(records []agentSessionRecord) string {
	if len(records) == 0 {
		return "No Codex sessions found.\n"
	}
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Project\tID\tSummary\tWorkDir\tMessages\tLast Activity")
	for _, record := range records {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
			record.Project,
			record.ID,
			truncate(record.Summary, 60),
			record.WorkDir,
			record.MessageCount,
			record.ModifiedAt.Format("2006-01-02 15:04"),
		)
	}
	_ = w.Flush()
	return buf.String()
}

func printAgentsUsage() {
	fmt.Fprintln(os.Stderr, "Usage: cc-connect agents [list] [--project NAME] [--json]")
	fmt.Fprintln(os.Stderr, "List all top-level Codex sessions across working directories.")
}
