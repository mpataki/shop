package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Server implements a minimal MCP server over stdio.
// It exposes a report_signal tool for agents to report their completion status.
type Server struct {
	agentName string
	signalDir string
}

func NewServer(agentName, signalDir string) *Server {
	return &Server{
		agentName: agentName,
		signalDir: signalDir,
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
			resp.Result = s.handleToolsCall(req.Params)
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
							"enum":        []string{"DONE", "BLOCKED", "NEEDS_HUMAN", "APPROVED", "CHANGES_REQUESTED", "CONTINUE", "STOP"},
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
		},
	}
}

func (s *Server) handleToolsCall(params json.RawMessage) map[string]any {
	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}

	if err := json.Unmarshal(params, &call); err != nil {
		return toolError("invalid parameters: " + err.Error())
	}

	if call.Name != "report_signal" {
		return toolError("unknown tool: " + call.Name)
	}

	// Write signal to disk
	data, err := json.MarshalIndent(call.Arguments, "", "  ")
	if err != nil {
		return toolError("failed to marshal signal: " + err.Error())
	}

	path := filepath.Join(s.signalDir, s.agentName+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return toolError("failed to write signal: " + err.Error())
	}

	status, _ := call.Arguments["status"].(string)
	return map[string]any{
		"content": []map[string]any{
			{
				"type": "text",
				"text": fmt.Sprintf("Signal reported: status=%s", status),
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
