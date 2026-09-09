package controller

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	actionsv1alpha1 "github.com/kelos-dev/open-actions/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func scopedValueProject() *actionsv1alpha1.Project {
	return &actionsv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "team", Namespace: "default"},
		Spec: actionsv1alpha1.ProjectSpec{
			Secrets:   &actionsv1alpha1.ProjectSecretSource{SecretRef: actionsv1alpha1.ProjectValueReference{Name: "project-secrets"}},
			Variables: &actionsv1alpha1.ProjectVariableSource{ConfigMapRef: actionsv1alpha1.ProjectValueReference{Name: "project-variables"}},
			Repositories: []actionsv1alpha1.ProjectRepositoryValues{{
				Name:      "acme/app",
				Secrets:   &actionsv1alpha1.ProjectSecretSource{SecretRef: actionsv1alpha1.ProjectValueReference{Name: "app-secrets"}},
				Variables: &actionsv1alpha1.ProjectVariableSource{ConfigMapRef: actionsv1alpha1.ProjectValueReference{Name: "app-variables"}},
			}},
		},
	}
}

func scopedValueRun(owner, repository string) *actionsv1alpha1.WorkflowRun {
	return &actionsv1alpha1.WorkflowRun{Spec: actionsv1alpha1.WorkflowRunSpec{
		Source: actionsv1alpha1.WorkflowRunSource{GitHub: &actionsv1alpha1.GitHubWorkflowRunSource{
			Repository: actionsv1alpha1.GitHubRepository{Owner: owner, Name: repository},
		}},
	}}
}

// https://docs.github.com/en/actions/reference/workflows-and-actions/variables#configuration-variable-precedence
func TestRepositoryVariablesOverrideProjectDefaults(t *testing.T) {
	for _, test := range []struct {
		name, owner, repository string
		projectDefaults         bool
		want                    map[string]any
		reads                   int
	}{
		{"both scopes", "acme", "app", true, map[string]any{"SHARED": "repository", "PROJECT_ONLY": "project", "REPO_ONLY": "repository", "EMPTY": ""}, 2},
		{"case insensitive repository", "Acme", "App", true, map[string]any{"SHARED": "repository", "PROJECT_ONLY": "project", "REPO_ONLY": "repository", "EMPTY": ""}, 2},
		{"different repository", "acme", "other", true, map[string]any{"SHARED": "project", "PROJECT_ONLY": "project", "EMPTY": "project"}, 1},
		{"different owner", "other", "app", true, map[string]any{"SHARED": "project", "PROJECT_ONLY": "project", "EMPTY": "project"}, 1},
		{"repository only", "acme", "app", false, map[string]any{"SHARED": "repository", "REPO_ONLY": "repository", "EMPTY": ""}, 1},
		{"no matching sources", "acme", "other", false, map[string]any{}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := scopedValueProject()
			if !test.projectDefaults {
				project.Spec.Variables = nil
			}
			objects := []client.Object{
				&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "project-variables", Namespace: "default"}, Data: map[string]string{"SHARED": "project", "PROJECT_ONLY": "project", "EMPTY": "project"}},
				&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-variables", Namespace: "default"}, Data: map[string]string{"SHARED": "repository", "REPO_ONLY": "repository", "EMPTY": ""}},
			}
			reader := &countingReader{Reader: fake.NewClientBuilder().WithScheme(runnerTestScheme(t)).WithObjects(objects...).Build()}
			reconciler := &WorkflowRunReconciler{APIReader: reader}
			variables := reconciler.projectVariableContext(context.Background(), project, scopedValueRun(test.owner, test.repository))
			if reader.getCount != 0 {
				t.Fatal("variables were read before an expression needed them")
			}
			for name, want := range test.want {
				got, found, err := variables.Resolve(strings.ToLower(name))
				if err != nil || !found || got != want {
					t.Fatalf("Resolve(%q) = %v, %t, %v; want %v", name, got, found, err, want)
				}
			}
			if _, found, err := variables.Resolve("MISSING"); err != nil || found {
				t.Fatalf("missing variable = %t, %v", found, err)
			}
			got, err := variables.Values()
			if err != nil || !maps.Equal(got, test.want) {
				t.Fatalf("Values() = %v, %v; want %v", got, err, test.want)
			}
			if reader.getCount != test.reads {
				t.Fatalf("ConfigMap reads = %d, want %d", reader.getCount, test.reads)
			}
		})
	}
}

func TestRepositoryVariableErrorsDoNotFallBackToProjectValues(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		project := scopedValueProject()
		objects := []client.Object{&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "project-variables", Namespace: "default"},
			Data:       map[string]string{"VALUE": "project"},
		}}
		if oversized {
			objects = append(objects, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "app-variables", Namespace: "default"},
				Data:       map[string]string{"VALUE": strings.Repeat("x", 48*1024+1)},
			})
		}
		reconciler := &WorkflowRunReconciler{APIReader: fake.NewClientBuilder().WithScheme(runnerTestScheme(t)).WithObjects(objects...).Build()}
		variables := reconciler.projectVariableContext(context.Background(), project, scopedValueRun("acme", "app"))
		value, _, err := variables.Resolve("VALUE")
		if value != nil || err == nil || !strings.Contains(err.Error(), `Project "team"`) || !strings.Contains(err.Error(), `ConfigMap "app-variables"`) {
			t.Fatalf("repository variable with oversized=%t = %v, %v", oversized, value, err)
		}
		if values, err := variables.Values(); values != nil || err == nil {
			t.Fatalf("repository variables with oversized=%t = %v, %v", oversized, values, err)
		}
	}
}

// https://docs.github.com/en/actions/reference/security/secrets#naming-your-secrets
func TestRunnerProjectsSharedThenRepositoryValues(t *testing.T) {
	for _, test := range []struct {
		name, owner, repository string
		projectDefaults         bool
		fork                    *actionsv1alpha1.WorkflowRunForkPullRequest
		secrets, variables      []string
	}{
		{"both scopes", "Acme", "App", true, nil, []string{"project-secrets", "app-secrets"}, []string{"project-variables", "app-variables"}},
		{"different repository", "acme", "other", true, nil, []string{"project-secrets"}, []string{"project-variables"}},
		{"different owner", "other", "app", true, nil, []string{"project-secrets"}, []string{"project-variables"}},
		{"repository only", "acme", "app", false, nil, []string{"app-secrets"}, []string{"app-variables"}},
		{"fork withholds both scopes", "acme", "app", true, &actionsv1alpha1.WorkflowRunForkPullRequest{}, nil, []string{"project-variables", "app-variables"}},
		{"fork explicitly permits both scopes", "acme", "app", true, &actionsv1alpha1.WorkflowRunForkPullRequest{SendSecrets: true}, []string{"project-secrets", "app-secrets"}, []string{"project-variables", "app-variables"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := scopedValueProject()
			if !test.projectDefaults {
				project.Spec.Secrets, project.Spec.Variables = nil, nil
			}
			run := scopedValueRun(test.owner, test.repository)
			run.Spec.ForkPullRequest = test.fork
			reconciler := &RunnerReconciler{Client: fake.NewClientBuilder().WithScheme(runnerTestScheme(t)).Build()}
			runnerObject := &actionsv1alpha1.Runner{Spec: actionsv1alpha1.RunnerSpec{Execution: runnerExecution("runner:test")}}
			job, err := reconciler.buildJob(&actionsv1alpha1.WorkflowJob{ObjectMeta: metav1.ObjectMeta{Name: "build", Namespace: "default"}}, run, project, runnerObject, nativeJobStartDeadline)
			if err != nil {
				t.Fatal(err)
			}
			pod := job.Spec.Template.Spec
			for _, context := range []struct {
				volume, directory string
				want              []string
			}{
				{jobSecretsVolume, "secrets", test.secrets}, {jobVariablesVolume, "variables", test.variables},
			} {
				var got []string
				for _, volume := range pod.Volumes {
					if volume.Name != context.volume {
						continue
					}
					if volume.Projected == nil || volume.Projected.DefaultMode == nil || *volume.Projected.DefaultMode != 0o440 {
						t.Fatalf("invalid value volume: %#v", volume)
					}
					for _, source := range volume.Projected.Sources {
						if source.Secret != nil {
							got = append(got, source.Secret.Name)
						} else if source.ConfigMap != nil {
							got = append(got, source.ConfigMap.Name)
						}
					}
				}
				if !slices.Equal(got, context.want) {
					t.Fatalf("%s sources = %v, want %v", context.directory, got, context.want)
				}
				container := pod.Containers[0]
				mount := corev1.VolumeMount{Name: context.volume, MountPath: jobContextMountPath + "/" + context.directory, ReadOnly: true}
				if slices.Contains(container.VolumeMounts, mount) != (len(context.want) > 0) || slices.Contains(container.Args, "--"+context.directory+"-directory="+mount.MountPath) != (len(context.want) > 0) {
					t.Fatalf("%s mount and arguments = %#v, %v", context.directory, container.VolumeMounts, container.Args)
				}
				for _, sidecar := range append(pod.Containers[1:], pod.InitContainers...) {
					for _, mount := range sidecar.VolumeMounts {
						if mount.Name == context.volume {
							t.Fatalf("sidecar %q mounts workflow values", sidecar.Name)
						}
					}
				}
			}
		})
	}
}

func TestProjectValidatesAndWatchesRepositoryValueSources(t *testing.T) {
	ctx := context.Background()
	project := scopedValueProject()
	objects := []client.Object{project}
	for _, name := range []string{"project-secrets", "app-secrets"} {
		objects = append(objects, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Data: map[string][]byte{"TOKEN": []byte("value")}})
	}
	for _, name := range []string{"project-variables", "app-variables"} {
		objects = append(objects, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Data: map[string]string{"VALUE": "value"}})
	}
	clusterClient := fake.NewClientBuilder().WithScheme(runnerTestScheme(t)).WithObjects(objects...).Build()
	reconciler := &ProjectReconciler{Client: clusterClient}
	sources := allProjectValueSources(project)
	if err := validateProjectSecretValues(ctx, clusterClient, project, sources.secrets); err != nil {
		t.Fatal(err)
	}
	if err := validateProjectVariableValues(ctx, clusterClient, project, sources.variables); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"Secret", "ConfigMap"} {
		name := "app-secrets"
		if kind == "ConfigMap" {
			name = "app-variables"
		}
		metadata := &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: kind}, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
		requests := reconciler.projectsForValueSource(ctx, metadata)
		if len(requests) != 1 || requests[0].NamespacedName != client.ObjectKeyFromObject(project) {
			t.Fatalf("%s requests = %v", kind, requests)
		}
		metadata.Namespace = "other"
		if requests := reconciler.projectsForValueSource(ctx, metadata); len(requests) != 0 {
			t.Fatalf("cross-namespace requests = %v", requests)
		}
	}
	for _, object := range objects[1:] {
		if err := clusterClient.Delete(ctx, object); err != nil {
			t.Fatal(err)
		}
		var err error
		switch object.(type) {
		case *corev1.Secret:
			err = validateProjectSecretValues(ctx, clusterClient, project, sources.secrets)
		case *corev1.ConfigMap:
			err = validateProjectVariableValues(ctx, clusterClient, project, sources.variables)
		}
		if err == nil || !strings.Contains(err.Error(), `Project "team"`) || !strings.Contains(err.Error(), object.GetName()) {
			t.Fatalf("missing %q error = %v", object.GetName(), err)
		}
		object.SetResourceVersion("")
		if err := clusterClient.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
}
