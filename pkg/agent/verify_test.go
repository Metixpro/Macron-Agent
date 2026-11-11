package agent

import (
	"strings"
	"testing"
)

func TestVerifyGate(t *testing.T) {
	v := newVerifyGate()

	// 1. Initial state: no files modified -> does not need verification
	needs, _ := v.NeedsVerification("Everything is done and ready.")
	if needs {
		t.Error("expected no verification needed when no files were modified")
	}

	// 2. Non-verifiable files (e.g. .txt, .md) modified -> does not trigger code verification gate
	v.SetRound(1)
	v.RecordFileModification("notes.txt")
	v.RecordFileModification("README.md")
	needs, _ = v.NeedsVerification("Task is completed.")
	if needs {
		t.Error("expected no verification for non-code files")
	}

	// 3. Modifying code file (.py) -> triggers verification gate on completion claim
	v.SetRound(2)
	v.RecordFileModification("src/calculator.py")

	// Model not claiming done yet (e.g. mid-task thought) -> does not trigger
	needs, _ = v.NeedsVerification("Now I will implement tests.")
	if needs {
		t.Error("expected no verification triggered when not claiming completion")
	}

	// Model claiming done without running tests -> BLOCKED by gate
	needs, prompt := v.NeedsVerification("I have implemented the calculator. Done!")
	if !needs {
		t.Fatal("expected verification gate to block completion of unverified code")
	}
	if !strings.Contains(prompt, "calculator.py") {
		t.Errorf("prompt missing unverified file name: %s", prompt)
	}

	// 4. Running tests with failure (exit code 1) -> still not verified
	v.SetRound(3)
	v.RecordExecution("bash_execute", "python3 src/calculator.py", 1)
	needs, _ = v.NeedsVerification("All done!")
	if !needs {
		t.Fatal("failed execution should not satisfy verification gate")
	}

	// 5. Running tests with success (exit code 0) -> verified!
	v.SetRound(4)
	v.RecordExecution("bash_execute", "python3 -m unittest src/calculator.py", 0)
	needs, _ = v.NeedsVerification("All tests pass and the calculator is ready.")
	if needs {
		t.Fatal("successful execution should satisfy verification gate")
	}

	// 6. Persian completion claims (e.g. «انجام شد»)
	v.SetRound(5)
	v.RecordFileModification("src/service.go")
	needs, _ = v.NeedsVerification("کد رو نوشتم و کار تمام شد.")
	if !needs {
		t.Fatal("expected verification gate to catch Persian completion claims for unverified code")
	}
}
