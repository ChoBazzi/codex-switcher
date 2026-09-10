package usage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanMetadata(t *testing.T) {
	for _, plan := range []string{"free", "go", "plus", "pro", "team", "business", "enterprise", "edu"} {
		d, err := Parse([]byte(`{"plan_type":"` + plan + `","rate_limit":null}`))
		if err != nil || d.PlanType != plan || d.RemainingPercent != nil {
			t.Fatalf("plan %s: metadata lost or quota invented", plan)
		}
		encoded, _ := json.Marshal(d)
		if !strings.Contains(string(encoded), `"plan_type":"`+plan+`"`) {
			t.Fatal("plan not published")
		}
	}
	for _, plan := range []string{`null`, `123`, `{}`, `"synthetic-private-value"`} {
		d, err := Parse([]byte(`{"plan_type":` + plan + `,"rate_limit":{"primary_window":{"used_percent":20},"secondary_window":{"used_percent":40}}}`))
		if err != nil || d.PlanType != "" || d.RemainingPercent == nil || *d.RemainingPercent != 60 {
			t.Fatal("optional plan changed quota parsing")
		}
		encoded, _ := json.Marshal(d)
		if strings.Contains(string(encoded), "plan_type") {
			t.Fatal("unrecognized plan leaked")
		}
	}
	d, err := Parse([]byte(`{"rate_limit":null}`))
	if err != nil || d.PlanType != "" {
		t.Fatal("missing plan unsupported")
	}
}
