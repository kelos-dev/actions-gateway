package controller

import (
	actionsv1alpha1 "github.com/kelos-dev/open-actions/api/v1alpha1"
	apiEquality "k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// Reporting acknowledgements do not change job execution or dependency readiness.
func workflowJobExecutionChanged(update event.UpdateEvent) bool {
	before := update.ObjectOld.(*actionsv1alpha1.WorkflowJob).DeepCopy()
	after := update.ObjectNew.(*actionsv1alpha1.WorkflowJob).DeepCopy()
	before.Status.Source, after.Status.Source = nil, nil
	before.ResourceVersion, after.ResourceVersion = "", ""
	before.ManagedFields, after.ManagedFields = nil, nil
	return !apiEquality.Semantic.DeepEqual(before, after)
}
