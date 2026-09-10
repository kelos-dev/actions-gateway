package controller

import (
	"testing"

	actionsv1alpha1 "github.com/kelos-dev/open-actions/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestWorkflowJobExecutionEvents(t *testing.T) {
	before := &actionsv1alpha1.WorkflowJob{ObjectMeta: metav1.ObjectMeta{Name: "build", ResourceVersion: "1"}}
	for _, test := range []struct {
		name   string
		change func(*actionsv1alpha1.WorkflowJob)
		want   bool
	}{
		{name: "unchanged", change: func(*actionsv1alpha1.WorkflowJob) {}},
		{name: "report recorded", change: func(job *actionsv1alpha1.WorkflowJob) {
			job.Status.Source = &actionsv1alpha1.WorkflowJobSourceStatus{GitHub: &actionsv1alpha1.GitHubWorkflowJobStatus{
				CommitStatus: &actionsv1alpha1.GitHubCommitStatus{State: actionsv1alpha1.GitHubCommitStatusStatePending, ReportDigest: "digest"},
			}}
		}},
		{name: "completed", want: true, change: func(job *actionsv1alpha1.WorkflowJob) { job.Status.Result = actionsv1alpha1.WorkflowJobResultSuccess }},
		{name: "completed with report recorded", want: true, change: func(job *actionsv1alpha1.WorkflowJob) {
			job.Status.Result = actionsv1alpha1.WorkflowJobResultSuccess
			job.Status.Source = &actionsv1alpha1.WorkflowJobSourceStatus{GitHub: &actionsv1alpha1.GitHubWorkflowJobStatus{
				CommitStatus: &actionsv1alpha1.GitHubCommitStatus{State: actionsv1alpha1.GitHubCommitStatusStateSuccess, ReportDigest: "digest"},
			}}
		}},
		{name: "ready", want: true, change: func(job *actionsv1alpha1.WorkflowJob) {
			job.Status.Conditions = []metav1.Condition{{Type: actionsv1alpha1.WorkflowJobConditionReady, Status: metav1.ConditionTrue}}
		}},
		{name: "cancellation requested", want: true, change: func(job *actionsv1alpha1.WorkflowJob) {
			job.Status.Conditions = []metav1.Condition{{Type: actionsv1alpha1.WorkflowJobConditionCancellationRequested, Status: metav1.ConditionTrue}}
		}},
		{name: "runner assigned", want: true, change: func(job *actionsv1alpha1.WorkflowJob) {
			job.Status.RunnerRef = &corev1.LocalObjectReference{Name: "runner"}
		}},
		{name: "outputs", want: true, change: func(job *actionsv1alpha1.WorkflowJob) { job.Status.Outputs = map[string]string{"artifact": "ready"} }},
		{name: "deleting", want: true, change: func(job *actionsv1alpha1.WorkflowJob) { now := metav1.Now(); job.DeletionTimestamp = &now }},
		{name: "owner changed", want: true, change: func(job *actionsv1alpha1.WorkflowJob) {
			job.OwnerReferences = []metav1.OwnerReference{{Name: "ci", UID: "run-uid"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			after := before.DeepCopy()
			after.ResourceVersion = "2"
			test.change(after)
			if got := workflowJobExecutionChanged(event.UpdateEvent{ObjectOld: before, ObjectNew: after}); got != test.want {
				t.Fatalf("execution event = %t, want %t", got, test.want)
			}
		})
	}
}
