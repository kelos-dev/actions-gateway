package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	actionsv1alpha1 "github.com/kelos-dev/open-actions/api/v1alpha1"
	"github.com/kelos-dev/open-actions/internal/runner"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type rerunMatrixJob struct {
	ID     string                             `json:"id"`
	Matrix *actionsv1alpha1.WorkflowJobMatrix `json:"matrix"`
	Values map[string]any                     `json:"values"`
}

func (r *WorkflowRunReconciler) selectRerunWorkflowPlan(ctx context.Context, run *actionsv1alpha1.WorkflowRun, planned []plannedWorkflowJob, deferred []deferredJobPlan) ([]plannedWorkflowJob, []deferredJobPlan, error) {
	if run.Spec.Rerun == nil || len(run.Spec.Rerun.JobIDs) == 0 {
		return planned, deferred, nil
	}
	if len(deferred) == 0 {
		selected, err := selectRerunWorkflowJobs(run, planned)
		if err != nil {
			err = &terminalPlanningError{cause: fmt.Errorf("select jobs for WorkflowRun %q: %w", run.Name, err)}
		}
		return selected, nil, err
	}
	plannedByID := make(map[string]plannedWorkflowJob, len(planned))
	for _, job := range planned {
		plannedByID[job.id] = job
	}
	deferredByID := make(map[string]deferredJobPlan, len(deferred))
	for _, plan := range deferred {
		deferredByID[plan.JobID] = plan
	}
	selectedLogicalIDs := make(map[string]bool)
	selectedJobs := make(map[string][]actionsv1alpha1.WorkflowJob)
	err := r.resolveRerunHistory(ctx, run, nil, func(history map[string]actionsv1alpha1.WorkflowJob) error {
		for _, id := range run.Spec.Rerun.JobIDs {
			if job, ok := plannedByID[id]; ok {
				logicalID := id
				if job.matrix != nil {
					logicalID = job.matrix.LogicalJobID
				}
				selectedLogicalIDs[logicalID] = true
				continue
			}
			job, ok := history[id]
			if !ok {
				return fmt.Errorf("selected job %q has no retained WorkflowJob", id)
			}
			logicalID := id
			if job.Spec.Matrix != nil {
				logicalID = job.Spec.Matrix.LogicalJobID
			}
			if _, ok := deferredByID[logicalID]; !ok {
				return fmt.Errorf("selected job %q is not present in the workflow", id)
			}
			selectedLogicalIDs[logicalID] = true
		}
		for _, id := range run.Spec.Rerun.JobIDs {
			if _, ok := plannedByID[id]; ok {
				continue
			}
			job := history[id]
			logicalID := id
			if job.Spec.Matrix != nil {
				logicalID = job.Spec.Matrix.LogicalJobID
			}
			selectedJobs[logicalID] = append(selectedJobs[logicalID], job)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	selected := make([]plannedWorkflowJob, 0, len(planned))
	selectedIDs := make(map[string]bool, len(run.Spec.Rerun.JobIDs))
	for _, id := range run.Spec.Rerun.JobIDs {
		selectedIDs[id] = true
	}
	for _, job := range planned {
		if selectedIDs[job.id] {
			selected = append(selected, job)
		}
	}
	selectedDeferred := make([]deferredJobPlan, 0, len(selectedJobs))
	for _, plan := range deferred {
		jobs := selectedJobs[plan.JobID]
		if len(jobs) == 0 {
			continue
		}
		reexpand := false
		for _, dependency := range plan.Job.Needs {
			reexpand = reexpand || selectedLogicalIDs[dependency]
		}
		// Expanded IDs are positional; expanding a partial selection again could target different combinations.
		if jobs[0].Spec.Matrix != nil && len(jobs) != int(jobs[0].Spec.Matrix.JobTotal) {
			reexpand = false
		}
		if !reexpand {
			sort.Slice(jobs, func(i, j int) bool { return jobs[i].Spec.JobID < jobs[j].Spec.JobID })
			for _, job := range jobs {
				if job.Spec.Matrix == nil {
					continue
				}
				matrix, err := r.rerunMatrixJob(ctx, run, &job)
				if err != nil {
					return nil, nil, err
				}
				plan.RerunJobs = append(plan.RerunJobs, matrix)
			}
		}
		selectedDeferred = append(selectedDeferred, plan)
	}
	return selected, selectedDeferred, nil
}

func (r *WorkflowRunReconciler) rerunMatrixJob(ctx context.Context, run *actionsv1alpha1.WorkflowRun, job *actionsv1alpha1.WorkflowJob) (rerunMatrixJob, error) {
	configMap := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: job.Namespace, Name: childName(job.Name, "plan")}
	if err := r.APIReader.Get(ctx, key, configMap); err != nil {
		err = fmt.Errorf("load matrix plan for WorkflowJob %q in WorkflowRun %q: %w", job.Name, run.Name, err)
		if apierrors.IsNotFound(err) {
			err = &terminalPlanningError{cause: err}
		}
		return rerunMatrixJob{}, err
	}
	if !metav1.IsControlledBy(configMap, job) || configMap.Immutable == nil || !*configMap.Immutable {
		return rerunMatrixJob{}, &terminalPlanningError{cause: fmt.Errorf("matrix plan ConfigMap %q for WorkflowRun %q is not immutable and controlled by WorkflowJob %q", configMap.Name, run.Name, job.Name)}
	}
	plan := &runner.Plan{}
	decoder := json.NewDecoder(strings.NewReader(configMap.Data[jobPlanKey]))
	decoder.UseNumber()
	if err := decoder.Decode(plan); err != nil {
		return rerunMatrixJob{}, &terminalPlanningError{cause: fmt.Errorf("decode matrix plan for WorkflowJob %q in WorkflowRun %q: %w", job.Name, run.Name, err)}
	}
	if plan.JobID != job.Spec.Matrix.LogicalJobID || !rerunMatrixValuesMatch(plan.Matrix, job.Spec.Matrix.Values) {
		return rerunMatrixJob{}, &terminalPlanningError{cause: fmt.Errorf("matrix plan for WorkflowJob %q in WorkflowRun %q does not match its matrix identity", job.Name, run.Name)}
	}
	return rerunMatrixJob{ID: job.Spec.JobID, Matrix: job.Spec.Matrix.DeepCopy(), Values: plan.Matrix}, nil
}

func rerunMatrixValuesMatch(values map[string]any, expected map[string]string) bool {
	if len(values) != len(expected) {
		return false
	}
	for name, value := range values {
		identity, ok := expected[name]
		if !ok {
			return false
		}
		if fmt.Sprint(value) == identity {
			continue
		}
		// Expression numbers use float64 formatting, which can differ from JSON encoding.
		// Compare that representation without changing the precise stored value.
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		floatValue, err := number.Float64()
		if err != nil || fmt.Sprint(floatValue) != identity {
			return false
		}
	}
	return true
}

// deferredWorkflowJobGraph reserves jobs before their WorkflowJobs exist, so a
// selected prerequisite cannot be satisfied by an execution from an older attempt.
func deferredWorkflowJobGraph(jobs []actionsv1alpha1.WorkflowJob, plans []deferredJobPlan) []actionsv1alpha1.WorkflowJob {
	graph := append([]actionsv1alpha1.WorkflowJob(nil), jobs...)
	byID := make(map[string]actionsv1alpha1.WorkflowJob, len(jobs))
	for _, job := range jobs {
		byID[job.Spec.JobID] = job
	}
	for _, plan := range plans {
		if job, ok := byID[plan.JobID]; ok && workflowJobTerminal(&job) {
			continue
		}
		if len(plan.RerunJobs) > 0 {
			for _, selected := range plan.RerunJobs {
				if _, ok := byID[selected.ID]; !ok {
					graph = append(graph, actionsv1alpha1.WorkflowJob{Spec: actionsv1alpha1.WorkflowJobSpec{JobID: selected.ID, Needs: plan.Job.Needs, Matrix: selected.Matrix}})
				}
			}
			continue
		}
		count, total := 0, 1
		for _, job := range jobs {
			if job.Spec.JobID == plan.JobID || (job.Spec.Matrix != nil && job.Spec.Matrix.LogicalJobID == plan.JobID) {
				count++
				if job.Spec.Matrix != nil {
					total = int(job.Spec.Matrix.JobTotal)
				}
			}
		}
		if count == total {
			continue
		}
		filtered := graph[:0]
		for _, job := range graph {
			if job.Spec.Matrix == nil || job.Spec.Matrix.LogicalJobID != plan.JobID {
				filtered = append(filtered, job)
			}
		}
		graph = append(filtered, actionsv1alpha1.WorkflowJob{Spec: actionsv1alpha1.WorkflowJobSpec{JobID: plan.JobID, Needs: plan.Job.Needs}})
	}
	return graph
}
