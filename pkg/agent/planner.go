package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

const plannerSystemPrompt = `You are a high-level software architect and task planner.
Given a user's objective, break it down into an optimal Directed Acyclic Graph (DAG) of actionable steps.
For each step, specify:
- "id": short slug like "step_1", "build_api", "run_tests"
- "title": concise action title (3-7 words)
- "goal": clear instruction of what to produce or verify in this step
- "depends_on": array of step IDs that must complete before this step can run (empty if can start immediately)
- "toolsets": toolsets needed, chosen from ["code", "web", "documents", "media", "images", "browser"]
- "risk": "low" for read/test/scaffold, "high" for destructive actions, external communication, or system changes

Respond with ONLY a valid JSON object matching this schema:
{
  "plan_title": "Short title",
  "steps": [
    {
      "id": "step_1",
      "title": "Title",
      "goal": "Detailed goal",
      "depends_on": [],
      "toolsets": ["code"],
      "risk": "low"
    }
  ]
}`

type rawPlanStep struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Goal      string   `json:"goal"`
	DependsOn []string `json:"depends_on"`
	Toolsets  []string `json:"toolsets"`
	Risk      string   `json:"risk"`
}

type rawPlanOutput struct {
	PlanTitle string        `json:"plan_title"`
	Steps     []rawPlanStep `json:"steps"`
}

type PlanResult struct {
	Title            string   `json:"title"`
	DAG              *TaskDAG `json:"dag"`
	RequiresApproval bool     `json:"requires_approval"`
	HighRiskSteps    []string `json:"high_risk_steps,omitempty"`
}

// PlanObjective runs a planning phase that generates a structured TaskDAG before execution.
func (a *Agent) PlanObjective(ctx context.Context, sessionID, goal string) (*PlanResult, error) {
	req := llm.Request{
		Messages: []llm.Message{
			{Role: "system", Content: plannerSystemPrompt},
			{Role: "user", Content: "Objective:\n" + goal},
		},
	}

	pctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	resp, err := a.LLM.Complete(pctx, req)
	if err != nil {
		return nil, fmt.Errorf("planner completion: %w", err)
	}

	content := strings.TrimSpace(resp.Message.Content)
	// Strip optional markdown code fence
	if start := strings.Index(content, "{"); start >= 0 {
		if end := strings.LastIndex(content, "}"); end > start {
			content = content[start : end+1]
		}
	}

	var raw rawPlanOutput
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		// Fallback: create a single-step DAG from the goal
		dag := NewDAG()
		_ = dag.AddNode(&DAGNode{
			ID:       "task_1",
			Title:    "Execute task",
			Goal:     goal,
			Status:   NodePending,
			Toolsets: []string{"code", "web"},
		})
		return &PlanResult{Title: "Task", DAG: dag}, nil
	}

	dag := NewDAG()
	var highRisk []string
	for _, s := range raw.Steps {
		id := strings.TrimSpace(s.ID)
		if id == "" {
			continue
		}
		node := &DAGNode{
			ID:        id,
			Title:     s.Title,
			Goal:      s.Goal,
			DependsOn: s.DependsOn,
			Toolsets:  s.Toolsets,
			Status:    NodePending,
		}
		if err := dag.AddNode(node); err != nil {
			continue
		}
		if strings.EqualFold(s.Risk, "high") {
			highRisk = append(highRisk, fmt.Sprintf("%s: %s", id, s.Title))
		}
	}

	return &PlanResult{
		Title:            raw.PlanTitle,
		DAG:              dag,
		RequiresApproval: len(highRisk) > 0,
		HighRiskSteps:    highRisk,
	}, nil
}

// PlanApprovalPrompt formats the plan review question for the user/owner.
func (p *PlanResult) PlanApprovalPrompt(lang string) string {
	if p == nil || p.DAG == nil {
		return ""
	}
	isFa := strings.HasPrefix(strings.ToLower(lang), "fa")
	var sb strings.Builder
	if isFa {
		sb.WriteString(fmt.Sprintf("📋 **برنامه پیشنهادی برای اجرا (%d مرحله):**\n\n", len(p.DAG.Order)))
		sb.WriteString(p.DAG.Render())
		if len(p.HighRiskSteps) > 0 {
			sb.WriteString(fmt.Sprintf("\n⚠️ **مراحل با ریسک بالا که نیاز به تایید دارند:**\n- %s\n", strings.Join(p.HighRiskSteps, "\n- ")))
		}
		sb.WriteString("\nآیا این برنامه را تایید می‌کنید؟ [بله / خیر / ویرایش]")
	} else {
		sb.WriteString(fmt.Sprintf("📋 **Proposed Execution Plan (%d steps):**\n\n", len(p.DAG.Order)))
		sb.WriteString(p.DAG.Render())
		if len(p.HighRiskSteps) > 0 {
			sb.WriteString(fmt.Sprintf("\n⚠️ **High-risk steps requiring confirmation:**\n- %s\n", strings.Join(p.HighRiskSteps, "\n- ")))
		}
		sb.WriteString("\nApprove execution plan? [yes / no / edit]")
	}
	return sb.String()
}
