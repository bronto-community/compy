package app

import (
	"reflect"
	"testing"
)

// The drop-diagnosis honesty rule: vars are blamed only when running AND
// dropping AND missing all hold — any leg absent means no claim.
func TestDropDiagnosisRule(t *testing.T) {
	missing := []string{"BRONTO_API_KEY"}
	cases := []struct {
		name    string
		running bool
		dropped int64
		missing []string
		want    []string
	}{
		{"missing values and drops names the vars", true, 3, missing, missing},
		{"missing values but no drops stays quiet", true, 0, missing, nil},
		{"drops with all values present blames nothing", true, 3, nil, nil},
		{"stopped collector claims nothing", false, 3, missing, nil},
	}
	for _, c := range cases {
		if got := dropDiagnosis(c.running, c.dropped, c.missing); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: dropDiagnosis(%v, %d, %v) = %v, want %v",
				c.name, c.running, c.dropped, c.missing, got, c.want)
		}
	}
}
