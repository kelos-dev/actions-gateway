package workflow

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kelos-dev/open-actions/internal/expression"
)

func TestJobContinueOnError(t *testing.T) {
	for _, test := range []struct {
		name       string
		field      string
		want       bool
		parseError string
		evalError  string
	}{
		{name: "omitted"},
		{name: "false", field: "false"},
		{name: "true", field: "true", want: true},
		{name: "expression false", field: "${{ false }}"},
		{name: "expression true", field: "${{ true }}", want: true},
		{name: "matrix boolean", field: "${{ matrix.experimental }}", want: true},
		{name: "github", field: "${{ github.event.experimental }}", want: true},
		{name: "needs", field: "${{ needs.prepare.result == 'success' }}", want: true},
		{name: "strategy", field: "${{ strategy.job-index == 3 && strategy.fail-fast }}", want: true},
		{name: "vars", field: "${{ fromJSON(vars.EXPERIMENTAL) }}", want: true},
		{name: "inputs", field: "${{ inputs.experimental }}", want: true},
		{name: "open actions", field: "${{ open_actions.run_url != '' }}", want: true},
		{name: "quoted boolean", field: "'true'", parseError: "exactly one expression"},
		{name: "empty string", field: "''", parseError: "exactly one expression"},
		{name: "number", field: "1", parseError: "line 11, column 24: value must be a boolean or expression"},
		{name: "sequence", field: "[true]", parseError: "line 11, column 24: value must be a boolean or expression"},
		{name: "mapping", field: "{value: true}", parseError: "line 11, column 24: value must be a boolean or expression"},
		{name: "template", field: "prefix ${{ true }}", parseError: "exactly one expression"},
		{name: "two expressions", field: "${{ true }} ${{ false }}", parseError: "exactly one expression"},
		{name: "syntax error", field: "${{ true", parseError: "continue-on-error"},
		{name: "too long", field: "${{ '" + strings.Repeat("a", MaxConditionBytes) + "' }}", parseError: "exceeds"},
		{name: "string result", field: "${{ 'true' }}", evalError: "expected boolean"},
		{name: "number result", field: "${{ 1 }}", evalError: "expected boolean"},
		{name: "null result", field: "${{ null }}", evalError: "expected boolean"},
		{name: "array result", field: "${{ fromJSON('[true]') }}", evalError: "expected boolean"},
		{name: "object result", field: "${{ fromJSON('{}') }}", evalError: "expected boolean"},
		{name: "evaluation error", field: "${{ fromJSON('invalid') }}", evalError: "continue-on-error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			field := ""
			if test.field != "" {
				field = "    continue-on-error: " + test.field + "\n"
			}
			definition, err := Parse([]byte("name: CI\non: push\njobs:\n  prepare:\n    runs-on: ubuntu-latest\n    steps:\n      - run: prepare\n  test:\n    needs: prepare\n    runs-on: ubuntu-latest\n" + field + "    steps:\n      - run: test\n"))
			if test.parseError != "" {
				if err == nil || !strings.Contains(err.Error(), test.parseError) {
					t.Fatalf("Parse() error = %v, want %q", err, test.parseError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := EvaluateJob("test", definition.Jobs["test"], expression.Context{Values: map[string]any{
				"github":       map[string]any{"event": map[string]any{"experimental": true}},
				"needs":        map[string]any{"prepare": map[string]any{"result": "success"}},
				"strategy":     map[string]any{"job-index": 3, "fail-fast": true},
				"matrix":       map[string]any{"experimental": true},
				"vars":         map[string]any{"EXPERIMENTAL": "true"},
				"inputs":       map[string]any{"experimental": true},
				"open_actions": map[string]any{"run_url": "https://actions.example/runs/ci"},
			}})
			if test.evalError != "" {
				if err == nil || !strings.Contains(err.Error(), test.evalError) || !strings.Contains(err.Error(), "job \"test\" continue-on-error") {
					t.Fatalf("EvaluateJob() error = %v, want %q with job and field", err, test.evalError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resolved.ContinueOnError.Value != test.want || resolved.ContinueOnError.usesExpression() {
				t.Fatalf("continue-on-error = %#v, want resolved %t", resolved.ContinueOnError, test.want)
			}
		})
	}
}

func TestJobContinueOnErrorExpressionAvailability(t *testing.T) {
	// https://docs.github.com/en/actions/reference/workflows-and-actions/contexts#context-availability
	for _, input := range []string{
		"env.experimental", "secrets.experimental", "steps.test.outcome", "job.status",
		"runner.os", "jobs.test.result", "success()", "failure()", "always()", "cancelled()", "hashFiles('**')",
	} {
		t.Run(input, func(t *testing.T) {
			_, err := Parse([]byte(fmt.Sprintf("name: CI\non: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    continue-on-error: ${{ %s }}\n    steps:\n      - run: test\n", input)))
			if err == nil || !strings.Contains(err.Error(), "job \"test\" continue-on-error") {
				t.Fatalf("unavailable expression error = %v", err)
			}
		})
	}
	_, err := Parse([]byte("name: CI\non: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    continue-on-error: ${{ needs.prepare.result == 'success' }}\n    steps:\n      - run: test\n"))
	if err == nil || !strings.Contains(err.Error(), "declares no dependencies") {
		t.Fatalf("undeclared needs error = %v", err)
	}
}
