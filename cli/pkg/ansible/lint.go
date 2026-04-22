package ansible

import "fmt"

// LintIssue is one finding from LintPlaybook.
type LintIssue struct {
	PlayName string
	TaskName string
	Module   string
	Rule     string
	Message  string
}

func (i LintIssue) Error() string {
	return fmt.Sprintf("[%s] play=%q task=%q module=%q: %s", i.Rule, i.PlayName, i.TaskName, i.Module, i.Message)
}

// LintPlaybook reports rule violations on pb. Rules:
// shell-needs-idempotence-marker (one of Args.creates/removes/when/changed_when);
// task-needs-name (no unnamed tasks); unarchive-needs-creates.
func LintPlaybook(pb *Playbook) []LintIssue {
	if pb == nil {
		return nil
	}
	var issues []LintIssue
	for _, play := range pb.Plays {
		for _, group := range [][]Task{play.PreTasks, play.Tasks, play.PostTasks} {
			for _, task := range group {
				issues = append(issues, lintTask(play.Name, task)...)
			}
		}
	}
	return issues
}

func lintTask(playName string, task Task) []LintIssue {
	var issues []LintIssue

	if task.Name == "" {
		issues = append(issues, LintIssue{
			PlayName: playName,
			TaskName: task.Name,
			Module:   task.Module,
			Rule:     "task-needs-name",
			Message:  "task must declare a Name (unnamed tasks produce unreadable Ansible output on failure)",
		})
	}

	if isShellModule(task.Module) {
		hasCreates := stringArg(task.Args, "creates") != ""
		hasRemoves := stringArg(task.Args, "removes") != ""
		hasWhen := task.When != ""
		hasChangedWhen := task.ChangedWhen != ""
		if !hasCreates && !hasRemoves && !hasWhen && !hasChangedWhen {
			issues = append(issues, LintIssue{
				PlayName: playName,
				TaskName: task.Name,
				Module:   task.Module,
				Rule:     "shell-needs-idempotence-marker",
				Message:  "shell task must declare at least one of args.creates, args.removes, when, or changed_when (idempotence guardrail)",
			})
		}
	}

	if task.Module == "ansible.builtin.unarchive" {
		if stringArg(task.Args, "creates") == "" {
			issues = append(issues, LintIssue{
				PlayName: playName,
				TaskName: task.Name,
				Module:   task.Module,
				Rule:     "unarchive-needs-creates",
				Message:  "unarchive task must set args.creates to a file that exists post-extract (re-runs otherwise clobber in-place state under dest)",
			})
		}
	}

	return issues
}

func isShellModule(module string) bool {
	return module == "shell" || module == "command" ||
		module == "ansible.builtin.shell" || module == "ansible.builtin.command"
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}
