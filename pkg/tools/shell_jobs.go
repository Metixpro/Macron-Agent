package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const ToolShellJobs = "shell_jobs"

var jobFirstLook = 3 * time.Second

func (r *Registry) startBackground(ctx context.Context, sessionID, cmd string) string {
	job, err := r.Machine.StartJob(ctx, sessionID, cmd)
	if err != nil {
		return "error: " + err.Error()
	}
	deadline := time.Now().Add(jobFirstLook)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if j, ok := findJob(r.Machine.Jobs(sessionID), job.ID); ok && !j.Running {
			job = j
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	out := r.jobTail(sessionID, job, 40)
	if !job.Running {
		return fmt.Sprintf("Background job %d ended at once (exit %d): %s\n%s", job.ID, job.Code, cmd, out)
	}
	return fmt.Sprintf("Started background job %d (pid %d): %s\nIt keeps running in this session's shell (until %s kills it, or an hour without use). "+
		"Later bash_execute and run_code calls reach it on localhost and share /tmp; its output goes to %s.\nFirst output:\n%s",
		job.ID, job.PID, cmd, ToolShellJobs, job.Log, out)
}

func (r *Registry) shellJobs(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	jobs := r.Machine.Jobs(sessionID)
	n := intArg(args, "job", 0, 0, 1<<20)
	if n == 0 && len(jobs) > 0 {
		n = jobs[len(jobs)-1].ID
	}
	switch strArg(args, "action") {
	case "output":
		j, ok := findJob(jobs, n)
		if !ok {
			return "error: no such job; " + jobList(jobs, r.Machine.ShellEnded(sessionID)), nil
		}
		return jobLine(j) + "\n" + r.jobTail(sessionID, j, intArg(args, "lines", 80, 1, 500)), nil
	case "kill":
		if _, ok := findJob(jobs, n); !ok {
			return "error: no such job; " + jobList(jobs, r.Machine.ShellEnded(sessionID)), nil
		}
		if err := r.Machine.KillJob(ctx, sessionID, n); err != nil {
			return "error: " + err.Error(), nil
		}
		return fmt.Sprintf("Stopped job %d and what it started.", n), nil
	}
	return jobList(jobs, r.Machine.ShellEnded(sessionID)), nil
}

func findJob(jobs []machine.Job, n int) (machine.Job, bool) {
	for _, j := range jobs {
		if j.ID == n {
			return j, true
		}
	}
	return machine.Job{}, false
}

func jobLine(j machine.Job) string {
	state := fmt.Sprintf("running for %s", time.Since(j.Started).Round(time.Second))
	if !j.Running {
		state = fmt.Sprintf("ended (exit %d) %s ago", j.Code, time.Since(j.Ended).Round(time.Second))
	}
	return fmt.Sprintf("job %d · %s · %s", j.ID, state, textutil.TruncateRunes(textutil.OneLine(j.Command), 120, "…"))
}

func jobList(jobs []machine.Job, ended string) string {
	if len(jobs) == 0 {
		if ended != "" {
			return "No background jobs (the shell ended: " + ended + ")."
		}
		return "No background jobs. bash_execute with background=true starts one."
	}
	lines := make([]string, len(jobs))
	for i, j := range jobs {
		lines[i] = jobLine(j)
	}
	s := strings.Join(lines, "\n")
	if ended != "" {
		s += "\n(the shell ended: " + ended + ")"
	}
	return s
}

func (r *Registry) jobTail(sessionID string, j machine.Job, lines int) string {
	data, cut, err := r.Machine.ReadTail(sessionID, j.Log, maxShownOutput)
	if err != nil {
		return "(no output: " + friendlyFSError(err) + ")"
	}
	text := strings.TrimRight(ansiRe.ReplaceAllString(strings.ReplaceAll(strings.ToValidUTF8(string(data), ""), "\r\n", "\n"), ""), "\n")
	if all := strings.Split(text, "\n"); len(all) > lines {
		text, cut = strings.Join(all[len(all)-lines:], "\n"), true
	}
	if text == "" {
		return "(nothing yet)"
	}
	if cut {
		text = "…\n" + text
	}
	return text
}
