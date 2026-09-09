package assurance

import "testing"

func defaultShape() ExpectedShape {
	return ExpectedShape{MinLength: 1, MaxLength: 2000, TimeoutMS: 30000}
}

func TestBuildMessagesAllTemplates(t *testing.T) {
	for _, key := range PromptTemplateKeys {
		msgs, err := BuildMessages(key)
		if err != nil {
			t.Fatalf("BuildMessages(%q) = %v, want nil", key, err)
		}
		if len(msgs) == 0 || msgs[0].Content == "" {
			t.Fatalf("BuildMessages(%q) produced no content", key)
		}
	}
}

func TestBuildMessagesUnknownTemplate(t *testing.T) {
	if _, err := BuildMessages("no_such_template"); err == nil {
		t.Fatal("BuildMessages(unknown) = nil error, want error")
	}
}

func TestAssessModelFingerprintMatches(t *testing.T) {
	out, err := Assess(AssessInput{
		TemplateKey: TemplateModelFingerprint, Model: "claude-sonnet-4",
		Text: "我是 Claude", Expected: defaultShape(),
	})
	if err != nil {
		t.Fatalf("Assess() = %v, want nil", err)
	}
	if out.Status != ResultStatusOK {
		t.Fatalf("status = %q, want ok", out.Status)
	}
}

func TestAssessModelFingerprintMismatchDegrades(t *testing.T) {
	out, err := Assess(AssessInput{
		TemplateKey: TemplateModelFingerprint, Model: "claude-sonnet-4",
		Text: "我是 GPT-4", Expected: defaultShape(),
	})
	if err != nil {
		t.Fatalf("Assess() = %v, want nil", err)
	}
	if out.Status != ResultStatusDegraded {
		t.Fatalf("status = %q, want degraded (fragment mismatch)", out.Status)
	}
}

func TestAssessModelFingerprintShapeViolationDegrades(t *testing.T) {
	out, err := Assess(AssessInput{
		TemplateKey: TemplateModelFingerprint, Model: "claude-sonnet-4",
		Text: "我是 Claude", Expected: ExpectedShape{MinLength: 1, MaxLength: 3, TimeoutMS: 1000},
	})
	if err != nil {
		t.Fatalf("Assess() = %v, want nil", err)
	}
	if out.Status != ResultStatusDegraded {
		t.Fatalf("status = %q, want degraded (response longer than declared max_length)", out.Status)
	}
}

func TestAssessModelFingerprintMustContainViolationDegrades(t *testing.T) {
	out, err := Assess(AssessInput{
		TemplateKey: TemplateModelFingerprint, Model: "claude-sonnet-4",
		Text: "我是 Claude", Expected: ExpectedShape{
			MinLength: 1, MaxLength: 2000, TimeoutMS: 1000, MustContain: []string{"版本号"},
		},
	})
	if err != nil {
		t.Fatalf("Assess() = %v, want nil", err)
	}
	if out.Status != ResultStatusDegraded {
		t.Fatalf("status = %q, want degraded (must_contain substring absent)", out.Status)
	}
}

func TestAssessBenchmarkSetFullMarksNoHistory(t *testing.T) {
	out, err := Assess(AssessInput{
		TemplateKey: TemplateBenchmarkSet, Model: "gpt-4o",
		Text: "12\nParis\n27\n", Expected: defaultShape(),
	})
	if err != nil {
		t.Fatalf("Assess() = %v, want nil", err)
	}
	if out.Status != ResultStatusOK {
		t.Fatalf("status = %q, want ok", out.Status)
	}
	if out.Score == nil || *out.Score != BenchmarkQuestionCount {
		t.Fatalf("score = %v, want %d", out.Score, BenchmarkQuestionCount)
	}
}

func TestAssessBenchmarkSetZeroCorrectDegrades(t *testing.T) {
	out, err := Assess(AssessInput{
		TemplateKey: TemplateBenchmarkSet, Model: "gpt-4o",
		Text: "我不知道", Expected: defaultShape(),
	})
	if err != nil {
		t.Fatalf("Assess() = %v, want nil", err)
	}
	if out.Status != ResultStatusDegraded {
		t.Fatalf("status = %q, want degraded", out.Status)
	}
	if out.Score == nil || *out.Score != 0 {
		t.Fatalf("score = %v, want 0", out.Score)
	}
}

func TestAssessBenchmarkSetDropVsPreviousDegrades(t *testing.T) {
	previous := 3
	out, err := Assess(AssessInput{
		TemplateKey: TemplateBenchmarkSet, Model: "gpt-4o",
		Text: "12\n", Expected: defaultShape(), PreviousScore: &previous,
	})
	if err != nil {
		t.Fatalf("Assess() = %v, want nil", err)
	}
	if out.Status != ResultStatusDegraded {
		t.Fatalf("status = %q, want degraded (score dropped from 3 to 1)", out.Status)
	}
}

func TestAssessBenchmarkSetSameAsPreviousStaysOK(t *testing.T) {
	previous := 2
	out, err := Assess(AssessInput{
		TemplateKey: TemplateBenchmarkSet, Model: "gpt-4o",
		Text: "12\nParis\n", Expected: defaultShape(), PreviousScore: &previous,
	})
	if err != nil {
		t.Fatalf("Assess() = %v, want nil", err)
	}
	if out.Status != ResultStatusOK {
		t.Fatalf("status = %q, want ok (score matched previous, not a significant drop)", out.Status)
	}
}

func TestParseBenchmarkScoreRoundTrip(t *testing.T) {
	out, err := Assess(AssessInput{TemplateKey: TemplateBenchmarkSet, Model: "gpt-4o", Text: "12\n27\n", Expected: defaultShape()})
	if err != nil {
		t.Fatalf("Assess() = %v, want nil", err)
	}
	score, ok := ParseBenchmarkScore(out.Verdict)
	if !ok {
		t.Fatalf("ParseBenchmarkScore(%q) failed to parse a verdict Assess itself produced", out.Verdict)
	}
	if score != 2 {
		t.Fatalf("score = %d, want 2", score)
	}
}

func TestParseBenchmarkScoreRejectsForeignText(t *testing.T) {
	if _, ok := ParseBenchmarkScore("命中长上下文尾部片段"); ok {
		t.Fatal("ParseBenchmarkScore should not parse a verdict from a different template")
	}
}

func TestAssessContextLengthHitsTail(t *testing.T) {
	tail := lastRunes(contextLengthFixedInput, 32)
	out, err := Assess(AssessInput{TemplateKey: TemplateContextLength, Model: "gpt-4o", Text: tail, Expected: defaultShape()})
	if err != nil {
		t.Fatalf("Assess() = %v, want nil", err)
	}
	if out.Status != ResultStatusOK {
		t.Fatalf("status = %q, want ok", out.Status)
	}
}

func TestAssessContextLengthMissesTailDegrades(t *testing.T) {
	out, err := Assess(AssessInput{TemplateKey: TemplateContextLength, Model: "gpt-4o", Text: "完全不相关的内容", Expected: defaultShape()})
	if err != nil {
		t.Fatalf("Assess() = %v, want nil", err)
	}
	if out.Status != ResultStatusDegraded {
		t.Fatalf("status = %q, want degraded", out.Status)
	}
}

func TestAssessMinViableRequestNonEmptyOK(t *testing.T) {
	out, err := Assess(AssessInput{TemplateKey: TemplateMinViableRequest, Model: "gpt-4o", Text: "pong", Expected: defaultShape()})
	if err != nil {
		t.Fatalf("Assess() = %v, want nil", err)
	}
	if out.Status != ResultStatusOK {
		t.Fatalf("status = %q, want ok", out.Status)
	}
}

func TestAssessMinViableRequestEmptyDegrades(t *testing.T) {
	out, err := Assess(AssessInput{TemplateKey: TemplateMinViableRequest, Model: "gpt-4o", Text: "   ", Expected: defaultShape()})
	if err != nil {
		t.Fatalf("Assess() = %v, want nil", err)
	}
	if out.Status != ResultStatusDegraded {
		t.Fatalf("status = %q, want degraded", out.Status)
	}
}

func TestAssessUnknownTemplateErrors(t *testing.T) {
	if _, err := Assess(AssessInput{TemplateKey: "bogus", Text: "x", Expected: defaultShape()}); err == nil {
		t.Fatal("Assess(bogus template) = nil error, want error")
	}
}

func TestExpectedFingerprintFragmentFallsBackToModelString(t *testing.T) {
	if got := expectedFingerprintFragment("some-brand-new-model-xyz"); got != "some-brand-new-model-xyz" {
		t.Fatalf("expectedFingerprintFragment = %q, want the model string itself as fallback", got)
	}
}

func TestExpectedFingerprintFragmentVendorPrefix(t *testing.T) {
	cases := map[string]string{
		"claude-sonnet-4-5": "claude",
		"gpt-4o-mini":       "gpt",
		"gemini-2.5-pro":    "gemini",
		"deepseek-v3":       "deepseek",
	}
	for model, want := range cases {
		if got := expectedFingerprintFragment(model); got != want {
			t.Errorf("expectedFingerprintFragment(%q) = %q, want %q", model, got, want)
		}
	}
}
