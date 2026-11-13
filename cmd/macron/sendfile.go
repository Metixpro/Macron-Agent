package main

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
)

func (t *terminal) FilePath(name string) (string, fs.FileInfo, error) {
	name = workspaceName(name)
	if name == "" {
		return "", nil, fmt.Errorf("no file name")
	}
	f, info, err := t.app.Machine.Open(t.session, name)
	if err != nil {
		return "", nil, err
	}
	f.Close()
	abs, _, err := t.app.Machine.HostPath(t.session, name)
	if err != nil {
		return "", nil, err
	}
	return abs, info, nil
}

func (t *terminal) sendFileTool() agent.LocalTool {
	return agent.LocalTool{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name: toolSendFile,
			Description: "Hand the user a finished file of any kind (picture, PDF, document, sheet, audio, video, archive, code): it appears in the chat " +
				"as a card they open or save. A ![caption](file) line in your answer does the same without a step. Never drafts or files you only needed for the work.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"filename": map[string]any{"type": "string"},
				"caption":  map[string]any{"type": "string", "description": "A line under it in the user's language (≤300)"},
			}, "required": []string{"filename"}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			name, _ := args["filename"].(string)
			abs, info, err := t.FilePath(name)
			switch {
			case strings.TrimSpace(name) == "":
				return agent.ToolOutput{Text: "error: give the name of the file"}, nil
			case err != nil:
				return agent.ToolOutput{Text: "error: " + workspaceName(name) + " cannot be sent (missing, not a regular file, or not readable)"}, nil
			case info.Size() == 0:
				return agent.ToolOutput{Text: "error: the file is empty"}, nil
			}
			return agent.ToolOutput{Text: fmt.Sprintf("%s (%s) is in the chat now; the user opens or saves it from there.\n[file: %s]", workspaceName(name), sizeText(info.Size()), abs)}, nil
		},
	}
}
