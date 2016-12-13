package alerts

import (
	"fmt"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/model/alertrecord"
	"github.com/evergreen-ci/evergreen/model/task"
)

/* Task trigger Implementations */

// TaskFailed is a trigger that queues an alert whenever a task fails, regardless of any alerts
// generated by previous runs of the task or other tasks within the version/variant/task type.
type TaskFailed struct{}

func (tf TaskFailed) Id() string        { return alertrecord.TaskFailedId }
func (trig TaskFailed) Display() string { return "any task fails" }

func (trig TaskFailed) ShouldExecute(ctx triggerContext) (bool, error) {
	if ctx.task.Status != evergreen.TaskFailed {
		return false, nil
	}
	return true, nil
}

func (trig TaskFailed) CreateAlertRecord(_ triggerContext) *alertrecord.AlertRecord { return nil }

// FirstFailureInVersion is a trigger that queues an alert whenever a task fails for the first time
// within a version. After one failure has triggered an alert for this event, subsequent failures
// will not trigger additional alerts.
type FirstFailureInVersion struct{}

func (trig FirstFailureInVersion) Id() string      { return alertrecord.FirstVersionFailureId }
func (trig FirstFailureInVersion) Display() string { return "the first task failure occurs" }
func (trig FirstFailureInVersion) CreateAlertRecord(ctx triggerContext) *alertrecord.AlertRecord {
	return newAlertRecord(ctx, alertrecord.FirstVersionFailureId)
}

func (trig FirstFailureInVersion) ShouldExecute(ctx triggerContext) (bool, error) {
	if ctx.task.Status != evergreen.TaskFailed {
		return false, nil
	}
	rec, err := alertrecord.FindOne(alertrecord.ByFirstFailureInVersion(ctx.task.Project, ctx.task.Version))
	if err != nil {
		return false, err
	}
	return rec == nil, nil
}

// FirstFailureInVersion is a trigger that queues an alert whenever a task fails for the first time
// within a variant. After one failure has triggered an alert for this event, subsequent failures
// will not trigger additional alerts.
type FirstFailureInVariant struct{}

func (trig FirstFailureInVariant) Id() string { return alertrecord.FirstVariantFailureId }
func (trig FirstFailureInVariant) Display() string {
	return "the first failure within each variant occurs"
}
func (trig FirstFailureInVariant) CreateAlertRecord(ctx triggerContext) *alertrecord.AlertRecord {
	return newAlertRecord(ctx, alertrecord.FirstVariantFailureId)
}
func (trig FirstFailureInVariant) ShouldExecute(ctx triggerContext) (bool, error) {
	if ctx.task.Status != evergreen.TaskFailed {
		return false, nil
	}
	rec, err := alertrecord.FindOne(alertrecord.ByFirstFailureInVariant(ctx.task.Version, ctx.task.BuildVariant))
	if err != nil {
		return false, nil
	}
	return rec == nil, nil
}

// FirstFailureInVersion is a trigger that queues an alert whenever a task fails for the first time
// for a task of a given name within a version. For example:
// "compile" fails on linux-64;   ShouldExecute returns true
// "compile" fails on windows;    ShouldExecute returns false because one was already sent for compile.
// "unit-tests" fails on windows; ShouldExecute returns true because nothing was sent yet for unit-tests.
// "unit-tests" fails on linux-64; ShouldExecute returns false
type FirstFailureInTaskType struct{}

func (trig FirstFailureInTaskType) Id() string { return alertrecord.FirstTaskTypeFailureId }
func (trig FirstFailureInTaskType) Display() string {
	return "the first failure for each task name occurs"
}
func (trig FirstFailureInTaskType) CreateAlertRecord(ctx triggerContext) *alertrecord.AlertRecord {
	return newAlertRecord(ctx, alertrecord.FirstTaskTypeFailureId)
}
func (trig FirstFailureInTaskType) ShouldExecute(ctx triggerContext) (bool, error) {
	if ctx.task.Status != evergreen.TaskFailed {
		return false, nil
	}
	rec, err := alertrecord.FindOne(alertrecord.ByFirstFailureInTaskType(ctx.task.Version, ctx.task.DisplayName))
	if err != nil {
		return false, nil
	}
	return rec == nil, nil
}

// TaskFailTransition is a trigger that queues an alert iff the following conditions are met:
// 1) A task fails and the previous completion of this task on the same variant was passing or
// the task has never run before
// 2) The most recent alert for this trigger, if existing, was stored when the 'last passing task'
// at the time was older than the 'last passing task' for the newly failed task.
// 3) The previous run was a failure, and there has been Multipler*Batchtime time since
// the previous alert was sent.
type TaskFailTransition struct{}

// failureLimitMultiplier is a magic scalar for determining how often to resend transition failures.
// If a failure reoccurs after 3*batchTime amount of time, we will resend transition emails.
const failureLimitMultiplier = 3

func (trig TaskFailTransition) Id() string { return alertrecord.TaskFailTransitionId }
func (trig TaskFailTransition) Display() string {
	return "a previously passing task fails"
}
func (trig TaskFailTransition) ShouldExecute(ctx triggerContext) (bool, error) {
	if ctx.task.Status != evergreen.TaskFailed {
		return false, nil
	}
	if ctx.previousCompleted == nil {
		return true, nil
	}
	if ctx.previousCompleted.Status == evergreen.TaskSucceeded {
		// the task transitioned to failure - but we will only trigger an alert if we haven't recorded
		// a sent alert for a transition after the same previously passing task.
		q := alertrecord.ByLastFailureTransition(ctx.task.DisplayName, ctx.task.BuildVariant, ctx.task.Project)
		lastAlerted, err := alertrecord.FindOne(q)
		if err != nil {
			return false, err
		}

		if lastAlerted == nil || (lastAlerted.RevisionOrderNumber < ctx.previousCompleted.RevisionOrderNumber) {
			// Either this alert has never been triggered before, or it was triggered for a
			// transition from failure after an older success than this one - so we need to
			// execute this trigger again.
			return true, nil
		}
	}
	if ctx.previousCompleted.Status == evergreen.TaskFailed {
		// check if enough time has passed since our last transition alert
		q := alertrecord.ByLastFailureTransition(ctx.task.DisplayName, ctx.task.BuildVariant, ctx.task.Project)
		lastAlerted, err := alertrecord.FindOne(q)
		if err != nil {
			return false, err
		}
		if lastAlerted == nil || lastAlerted.TaskId == "" {
			return false, nil
		}
		return reachedFailureLimit(lastAlerted.TaskId)
	}
	return false, nil
}

func (trig TaskFailTransition) CreateAlertRecord(ctx triggerContext) *alertrecord.AlertRecord {
	rec := newAlertRecord(ctx, alertrecord.TaskFailTransitionId)
	// For pass/fail transition bookkeeping, we store the revision order number of the
	// previous (passing) task, not the currently passing task.
	rec.RevisionOrderNumber = -1
	if ctx.previousCompleted != nil {
		rec.RevisionOrderNumber = ctx.previousCompleted.RevisionOrderNumber
	}
	return rec
}

// reachedFailureLimit returns true if task for the previous failure transition alert
// happened too long ago, as determined by some magic math.
func reachedFailureLimit(taskId string) (bool, error) {
	t, err := task.FindOne(task.ById(taskId))
	if err != nil {
		return false, err
	}
	if t == nil {
		return false, fmt.Errorf("task %v not found", taskId)
	}
	pr, err := model.FindOneProjectRef(t.Project)
	if err != nil {
		return false, err
	}
	if pr == nil {
		return false, fmt.Errorf("project ref %v not found", t.Project)
	}
	p, err := model.FindProject(t.Revision, pr)
	if err != nil {
		return false, err
	}
	if p == nil {
		return false, fmt.Errorf("project %v not found for revision %v", t.Project, t.Revision)
	}
	v := p.FindBuildVariant(t.BuildVariant)
	if v == nil {
		return false, fmt.Errorf("build variant %v does not exist in project", t.BuildVariant)
	}
	batchTime := pr.GetBatchTime(v)
	reached := time.Since(t.FinishTime) > (time.Duration(batchTime) * time.Minute * failureLimitMultiplier)
	return reached, nil

}

type LastRevisionNotFound struct{}

func (lrnf LastRevisionNotFound) Id() string      { return alertrecord.TaskFailedId }
func (lrnf LastRevisionNotFound) Display() string { return "any task fails" }

func (lrnf LastRevisionNotFound) ShouldExecute(ctx triggerContext) (bool, error) {
	if ctx.task.Status != evergreen.TaskFailed {
		return false, nil
	}
	return true, nil
}

func (lrnf LastRevisionNotFound) CreateAlertRecord(ctx triggerContext) *alertrecord.AlertRecord {
	rec := newAlertRecord(ctx, alertrecord.LastRevisionNotFound)
	return rec
}
