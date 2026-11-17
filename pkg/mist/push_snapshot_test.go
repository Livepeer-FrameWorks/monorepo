package mist

import "testing"

func TestPushSnapshotRejectsIncompleteAbsenceEvidence(t *testing.T) {
	writer := []interface{}{float64(42), "live+stream", "/recording/segment.ts", ""}
	for _, test := range []struct {
		name     string
		response map[string]interface{}
		valid    bool
	}{
		{"empty", map[string]interface{}{"config": map[string]interface{}{"iid": "controller"}, "push_list": nil}, true},
		{"writer", map[string]interface{}{"config": map[string]interface{}{"iid": "controller"}, "push_list": []interface{}{writer}}, true},
		{"missing list", map[string]interface{}{"config": map[string]interface{}{"iid": "controller"}}, false},
		{"missing incarnation", map[string]interface{}{"push_list": nil}, false},
		{"truncated writer", map[string]interface{}{"config": map[string]interface{}{"iid": "controller"}, "push_list": []interface{}{[]interface{}{float64(42)}}}, false},
		{"duplicate writer", map[string]interface{}{"config": map[string]interface{}{"iid": "controller"}, "push_list": []interface{}{writer, writer}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parsePushSnapshot(test.response)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
		})
	}
}
