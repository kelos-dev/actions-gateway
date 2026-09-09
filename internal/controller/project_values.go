package controller

import (
	"strings"

	actionsv1alpha1 "github.com/kelos-dev/open-actions/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

type projectValueSources struct {
	secrets   []corev1.LocalObjectReference
	variables []corev1.LocalObjectReference
}

func (sources *projectValueSources) append(secrets *actionsv1alpha1.ProjectSecretSource, variables *actionsv1alpha1.ProjectVariableSource) {
	if secrets != nil {
		sources.secrets = append(sources.secrets, corev1.LocalObjectReference{Name: secrets.SecretRef.Name})
	}
	if variables != nil {
		sources.variables = append(sources.variables, corev1.LocalObjectReference{Name: variables.ConfigMapRef.Name})
	}
}

func allProjectValueSources(project *actionsv1alpha1.Project) projectValueSources {
	var sources projectValueSources
	sources.append(project.Spec.Secrets, project.Spec.Variables)
	for _, repository := range project.Spec.Repositories {
		sources.append(repository.Secrets, repository.Variables)
	}
	return sources
}

func workflowRunValueSources(project *actionsv1alpha1.Project, run *actionsv1alpha1.WorkflowRun) projectValueSources {
	var sources projectValueSources
	sources.append(project.Spec.Secrets, project.Spec.Variables)
	if run.Spec.Source.GitHub != nil {
		repository := run.Spec.Source.GitHub.Repository
		for _, values := range project.Spec.Repositories {
			if strings.EqualFold(values.Name, repository.Owner+"/"+repository.Name) {
				sources.append(values.Secrets, values.Variables)
				break
			}
		}
	}
	return sources
}
