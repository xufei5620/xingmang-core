package action

import (
	"errors"
	"testing"
)

func demoSchema() Schema {
	return Schema{Fields: []Field{
		{Name: "service_type", Type: FieldString, Required: true},
		{Name: "environment", Type: FieldString, Required: true,
			Enum: []string{"development", "staging", "production"}},
		{Name: "replicas", Type: FieldInt},
		{Name: "enabled", Type: FieldBool},
		{Name: "allowlist", Type: FieldStringSlice},
	}}
}

func TestSchemaAcceptsValid(t *testing.T) {
	err := demoSchema().Validate(map[string]any{
		"service_type": "sub2api",
		"environment":  "production",
		"replicas":     3,
		"enabled":      true,
		"allowlist":    []string{"api.solov.cc"},
	})
	if err != nil {
		t.Fatalf("合法参数被拒绝: %v", err)
	}
}

func TestSchemaRejectsUnknownField(t *testing.T) {
	err := demoSchema().Validate(map[string]any{
		"service_type": "sub2api",
		"environment":  "production",
		"is_admin":     true,
	})
	if !errors.Is(err, ErrUnknownField) {
		t.Fatalf("未声明字段必须拒绝, got %v", err)
	}
}

func TestSchemaRejectsMissingRequired(t *testing.T) {
	err := demoSchema().Validate(map[string]any{"service_type": "sub2api"})
	if !errors.Is(err, ErrRequiredField) {
		t.Fatalf("缺必填字段必须拒绝, got %v", err)
	}
}

func TestSchemaRejectsWrongType(t *testing.T) {
	for name, params := range map[string]map[string]any{
		"string 传 int":   {"service_type": 1, "environment": "production"},
		"int 传 string":   {"service_type": "a", "environment": "production", "replicas": "3"},
		"bool 传 string":  {"service_type": "a", "environment": "production", "enabled": "yes"},
		"slice 传 string": {"service_type": "a", "environment": "production", "allowlist": "x"},
	} {
		if err := demoSchema().Validate(params); !errors.Is(err, ErrFieldType) {
			t.Fatalf("%s 必须拒绝, got %v", name, err)
		}
	}
}

func TestSchemaRejectsEnumViolation(t *testing.T) {
	err := demoSchema().Validate(map[string]any{
		"service_type": "sub2api",
		"environment":  "prod",
	})
	if !errors.Is(err, ErrEnumViolation) {
		t.Fatalf("枚举外的值必须拒绝, got %v", err)
	}
}

func TestSchemaAcceptsJSONNumbers(t *testing.T) {
	if err := demoSchema().Validate(map[string]any{
		"service_type": "a", "environment": "production", "replicas": float64(3),
	}); err != nil {
		t.Fatalf("float64(3) 应作为 int 接受: %v", err)
	}
	if err := demoSchema().Validate(map[string]any{
		"service_type": "a", "environment": "production", "replicas": float64(3.5),
	}); !errors.Is(err, ErrFieldType) {
		t.Fatalf("有小数部分的 float64 不应作为 int 接受, got %v", err)
	}
}

func TestSchemaAcceptsAnySliceOfStrings(t *testing.T) {
	if err := demoSchema().Validate(map[string]any{
		"service_type": "a", "environment": "production",
		"allowlist": []any{"api.solov.cc"},
	}); err != nil {
		t.Fatalf("[]any{string} 应被接受: %v", err)
	}
	if err := demoSchema().Validate(map[string]any{
		"service_type": "a", "environment": "production",
		"allowlist": []any{1},
	}); !errors.Is(err, ErrFieldType) {
		t.Fatalf("[]any{int} 应被拒绝, got %v", err)
	}
}
