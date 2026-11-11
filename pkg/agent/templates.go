package agent

import (
	"fmt"
	"strings"
)

// WorkflowTemplate defines a reusable pattern of DAG steps for common complex jobs.
type WorkflowTemplate struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Tags        []string          `json:"tags"`
	BuildDAG    func(target string) *TaskDAG `json:"-"`
}

var StandardTemplates = map[string]*WorkflowTemplate{
	"code_review": {
		ID:          "code_review",
		Name:        "Code Review",
		Description: "Multi-dimension review: inspect changes, security audit, bug detection, and formatted report",
		Tags:        []string{"code review", "review", "بررسی کد", "کد ریویو", "ریویو"},
		BuildDAG: func(target string) *TaskDAG {
			dag := NewDAG()
			_ = dag.AddNode(&DAGNode{
				ID: "inspect", Title: "Inspect changed files",
				Goal: fmt.Sprintf("Read and analyze modified code in %s", target),
				Toolsets: []string{"code"},
			})
			_ = dag.AddNode(&DAGNode{
				ID: "security", Title: "Security audit",
				Goal: "Check for security vulnerabilities, injection flaws, and leaked credentials",
				DependsOn: []string{"inspect"},
				Toolsets: []string{"code"},
			})
			_ = dag.AddNode(&DAGNode{
				ID: "correctness", Title: "Logic and bug analysis",
				Goal: "Check edge cases, type safety, error handling, and performance",
				DependsOn: []string{"inspect"},
				Toolsets: []string{"code"},
			})
			_ = dag.AddNode(&DAGNode{
				ID: "report", Title: "Synthesize review report",
				Goal: "Compile verified findings into actionable review with line references",
				DependsOn: []string{"security", "correctness"},
				Toolsets: []string{"code"},
			})
			return dag
		},
	},
	"deep_research": {
		ID:          "deep_research",
		Name:        "Deep Research",
		Description: "Multi-source research: query breakdown, multi-engine search, fact check, and formatted report",
		Tags:        []string{"deep research", "research", "تحقیق", "پژوهش", "تحقیق عمیق"},
		BuildDAG: func(target string) *TaskDAG {
			dag := NewDAG()
			_ = dag.AddNode(&DAGNode{
				ID: "breakdown", Title: "Decompose research questions",
				Goal: fmt.Sprintf("Analyze topic %s into key inquiry questions and data requirements", target),
				Toolsets: []string{"web"},
			})
			_ = dag.AddNode(&DAGNode{
				ID: "search_web", Title: "Search web and academic sources",
				Goal: "Gather comprehensive source documents, statistics, and domain publications",
				DependsOn: []string{"breakdown"},
				Toolsets: []string{"web"},
			})
			_ = dag.AddNode(&DAGNode{
				ID: "fact_check", Title: "Cross-verify facts and dates",
				Goal: "Verify primary source claims, confirm dates, and cross-reference numbers",
				DependsOn: []string{"search_web"},
				Toolsets: []string{"web"},
			})
			_ = dag.AddNode(&DAGNode{
				ID: "synthesis", Title: "Synthesize final analytical paper",
				Goal: "Write thorough, structured report with complete source references",
				DependsOn: []string{"fact_check"},
				Toolsets: []string{"documents", "web"},
			})
			return dag
		},
	},
	"refactor": {
		ID:          "refactor",
		Name:        "Safe Refactor",
		Description: "Refactoring workflow: baseline snapshot, design, atomic changes, and test verification",
		Tags:        []string{"refactor", "ریفکتور", "بازنویسی", "بهبود کد"},
		BuildDAG: func(target string) *TaskDAG {
			dag := NewDAG()
			_ = dag.AddNode(&DAGNode{
				ID: "baseline", Title: "Establish baseline & run existing tests",
				Goal: fmt.Sprintf("Run existing test suite and inspect %s to ensure passing baseline", target),
				Toolsets: []string{"code"},
			})
			_ = dag.AddNode(&DAGNode{
				ID: "design", Title: "Plan refactoring architecture",
				Goal: "Define target interfaces, modular separations, and step sequence",
				DependsOn: []string{"baseline"},
				Toolsets: []string{"code"},
			})
			_ = dag.AddNode(&DAGNode{
				ID: "apply", Title: "Apply refactoring changes",
				Goal: "Execute code refactoring in clean, modular edits",
				DependsOn: []string{"design"},
				Toolsets: []string{"code"},
			})
			_ = dag.AddNode(&DAGNode{
				ID: "verify", Title: "Verify zero regression with test suite",
				Goal: "Run all tests, verify exit code 0, and confirm clean behavior",
				DependsOn: []string{"apply"},
				Toolsets: []string{"code"},
			})
			return dag
		},
	},
}

// MatchTemplate inspects the prompt and returns a matching workflow template if found.
func MatchTemplate(prompt string) *WorkflowTemplate {
	lower := strings.ToLower(prompt)
	for _, t := range StandardTemplates {
		for _, tag := range t.Tags {
			if strings.Contains(lower, tag) {
				return t
			}
		}
	}
	return nil
}
