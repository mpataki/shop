package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/mpataki/shop/internal/models"
	"github.com/mpataki/shop/internal/storage"
)

// Server implements a minimal MCP server over stdio.
// It exposes report_signal, get_context, and get_run_info tools for agents.
type Server struct {
	agentName string
	dbPath    string
	runID     int64
	execID    int64
}

func NewServer(agentName, dbPath string, runID, execID int64) *Server {
	return &Server{
		agentName: agentName,
		dbPath:    dbPath,
		runID:     runID,
		execID:    execID,
	}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Run starts the MCP server, reading JSON-RPC messages from stdin
// and writing responses to stdout. Exits when stdin is closed.
func (s *Server) Run() error {
	var store *storage.Storage
	if s.dbPath != "" {
		var err error
		store, err = storage.New(s.dbPath)
		if err != nil {
			// Log to stderr (not stdout — that's the MCP protocol channel)
			fmt.Fprintf(os.Stderr, "shop mcp-server: failed to open db: %v\n", err)
		} else {
			defer store.Close()
		}
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}

		// Notifications (no ID) don't get a response
		if req.ID == nil {
			continue
		}

		resp := response{JSONRPC: "2.0", ID: req.ID}

		switch req.Method {
		case "initialize":
			resp.Result = s.handleInitialize()
		case "tools/list":
			resp.Result = s.handleToolsList()
		case "tools/call":
			resp.Result = s.handleToolsCall(req.Params, store)
		default:
			resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
		}

		out, err := json.Marshal(resp)
		if err != nil {
			continue
		}
		fmt.Fprintf(os.Stdout, "%s\n", out)
	}

	return scanner.Err()
}

func (s *Server) handleInitialize() map[string]any {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "shop",
			"version": "1.0.0",
		},
	}
}

func (s *Server) handleToolsList() map[string]any {
	return map[string]any{
		"tools": []map[string]any{
			{
				"name":        "report_signal",
				"description": "Report the completion status of your task. You MUST call this exactly once when your work is complete.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"status": map[string]any{
							"type":        "string",
							"enum":        models.ValidAgentStatusStrings(),
							"description": "Your completion status",
						},
						"summary": map[string]any{
							"type":        "string",
							"description": "Summary of what you accomplished and key information for the next agent",
						},
						"reason": map[string]any{
							"type":        "string",
							"description": "Reason, if status is BLOCKED, NEEDS_HUMAN, or STOP",
						},
					},
					"required": []string{"status", "summary"},
				},
			},
			{
				"name":        "get_context",
				"description": "Get context from previous agents in this workflow run, including their summaries and statuses.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
			{
				"name":        "get_run_info",
				"description": "Get metadata about the current workflow run: run ID, workflow name, initial prompt, and current agent.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		},
	}
}

func (s *Server) handleToolsCall(params json.RawMessage, store *storage.Storage) map[string]any {
	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}

	if err := json.Unmarshal(params, &call); err != nil {
		return toolError("invalid parameters: " + err.Error())
	}

	switch call.Name {
	case "report_signal":
		return s.handleReportSignal(call.Arguments, store)
	case "get_context":
		return s.handleGetContext(store)
	case "get_run_info":
		return s.handleGetRunInfo(store)
	default:
		return toolError("unknown tool: " + call.Name)
	}
}

func (s *Server) handleReportSignal(args map[string]any, store *storage.Storage) map[string]any {
	statusStr, _ := args["status"].(string)
	status := models.SignalStatus(statusStr)
	if !status.IsValid() {
		return toolError(fmt.Sprintf("invalid status %q, must be one of: %v", statusStr, models.ValidAgentStatusStrings()))
	}

	if store == nil || s.execID == 0 {
		return toolError("MCP server not connected to database; cannot write signal")
	}

	if err := store.UpdateExecutionSignal(s.execID, args); err != nil {
		return toolError("failed to write signal to database: " + err.Error())
	}

	return map[string]any{
		"content": []map[string]any{
			{
				"type": "text",
				"text": fmt.Sprintf("Signal reported: status=%s", status),
			},
		},
	}
}

func (s *Server) handleGetContext(store *storage.Storage) map[string]any {
	if store == nil {
		return toolError("MCP server not connected to database")
	}

	run, err := store.GetRun(s.runID)
	if err != nil {
		return toolError("failed to get run: " + err.Error())
	}

	execs, err := store.GetExecutionsForRun(s.runID)
	if err != nil {
		return toolError("failed to get executions: " + err.Error())
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Run Context\n\n**Workflow:** %s\n**Task:** %s\n\n---\n\n", run.WorkflowName, run.InitialPrompt)

	for _, exec := range execs {
		if exec.ID == s.execID {
			continue // skip the current execution's own (not-yet-complete) signal
		}
		if exec.OutputSignal == nil {
			continue
		}
		agentStatus, _ := exec.OutputSignal["status"].(string)
		if agentStatus == "" {
			continue
		}
		if summary, ok := exec.OutputSignal["summary"].(string); ok && summary != "" {
			fmt.Fprintf(&sb, "## %s\n\n**Status:** %s\n\n%s\n\n---\n\n", exec.AgentName, agentStatus, summary)
		} else {
			// Fallback: dump full signal JSON so no information is lost
			signalJSON, _ := json.MarshalIndent(exec.OutputSignal, "", "  ")
			fmt.Fprintf(&sb, "## %s\n\n**Status:** %s\n\n```json\n%s\n```\n\n---\n\n", exec.AgentName, agentStatus, string(signalJSON))
		}
	}

	return map[string]any{
		"content": []map[string]any{
			{
				"type": "text",
				"text": sb.String(),
			},
		},
	}
}

func (s *Server) handleGetRunInfo(store *storage.Storage) map[string]any {
	if store == nil {
		return toolError("MCP server not connected to database")
	}

	run, err := store.GetRun(s.runID)
	if err != nil {
		return toolError("failed to get run: " + err.Error())
	}

	info := fmt.Sprintf("Run ID: %d\nWorkflow: %s\nStatus: %s\nCurrent Agent: %s\nInitial Prompt: %s",
		run.ID, run.WorkflowName, run.Status, run.CurrentAgent, run.InitialPrompt)

	return map[string]any{
		"content": []map[string]any{
			{
				"type": "text",
				"text": info,
			},
		},
	}
}

func toolError(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{
			{
				"type": "text",
				"text": "Error: " + msg,
			},
		},
		"isError": true,
	}
}
