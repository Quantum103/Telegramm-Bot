package repository

import "testing"

func TestIsCanonicalUUID(t *testing.T) {
	tests := []struct {
		value string
		valid bool
	}{
		{value: "7ad6a3e1-4c40-4ea9-810f-8e5e27ab0001", valid: true},
		{value: "7AD6A3E1-4C40-4EA9-810F-8E5E27AB0001", valid: true},
		{value: "7ad6a3e14c404ea9810f8e5e27ab0001", valid: false},
		{value: "not-a-uuid", valid: false},
		{value: "7ad6a3e1-4c40-4ea9-810f-8e5e27ab000g", valid: false},
		{value: "7ad6a3e1_4c40-4ea9-810f-8e5e27ab0001", valid: false},
	}
	for _, test := range tests {
		if got := isCanonicalUUID(test.value); got != test.valid {
			t.Errorf("isCanonicalUUID(%q) = %t, want %t", test.value, got, test.valid)
		}
	}
}

func TestValidEvaluationResult(t *testing.T) {
	for _, result := range []string{"correct", "partial", "incorrect", "ambiguous"} {
		if !validEvaluationResult(result) {
			t.Errorf("результат %q должен быть допустимым", result)
		}
	}
	for _, result := range []string{"", "failed", "pending", "unknown"} {
		if validEvaluationResult(result) {
			t.Errorf("результат %q не должен смешиваться с результатом проверки", result)
		}
	}
}
