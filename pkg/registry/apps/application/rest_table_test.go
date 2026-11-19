package application

import (
	"testing"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTableShowsWorkloadsReadyNextToReady(t *testing.T) {
	appWith := func(name string, conditions ...metav1.Condition) appsv1alpha1.Application {
		app := appsv1alpha1.Application{ObjectMeta: metav1.ObjectMeta{Name: name}}
		app.SetConditions(conditions)
		return app
	}
	ready := metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue}
	cases := []struct {
		name string
		app  appsv1alpha1.Application
		want string
	}{
		{"workloads not ready", appWith("a", ready, metav1.Condition{Type: "WorkloadsReady", Status: metav1.ConditionFalse}), "False"},
		{"workloads ready", appWith("b", ready, metav1.Condition{Type: "WorkloadsReady", Status: metav1.ConditionTrue}), "True"},
		{"workloads unknown", appWith("c", ready, metav1.Condition{Type: "WorkloadsReady", Status: metav1.ConditionUnknown}), "Unknown"},
		{"no workload monitor", appWith("d", ready), "<none>"},
	}
	r := &REST{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, table := range []metav1.Table{
				r.buildTableFromApplications([]appsv1alpha1.Application{tc.app}),
				r.buildTableFromApplication(tc.app),
			} {
				var names []string
				for _, c := range table.ColumnDefinitions {
					names = append(names, c.Name)
				}
				if len(names) < 3 || names[1] != "READY" || names[2] != "WORKLOADS" {
					t.Fatalf("columns = %v, want WORKLOADS right after READY", names)
				}
				if table.ColumnDefinitions[2].Priority != 0 {
					t.Errorf("WORKLOADS priority = %d, want 0 so kubectl shows it by default", table.ColumnDefinitions[2].Priority)
				}
				row := table.Rows[0].Cells
				if len(row) != len(names) {
					t.Fatalf("row has %d cells for %d columns", len(row), len(names))
				}
				if row[1] != "True" || row[2] != tc.want {
					t.Errorf("READY=%v WORKLOADS=%v, want True and %s", row[1], row[2], tc.want)
				}
			}
		})
	}
}
