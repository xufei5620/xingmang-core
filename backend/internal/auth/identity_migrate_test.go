package auth

import "testing"

// TestValidateIdentityMigrationInput exercises every parameter-shape refusal
// MigrateOIDCBinding documents, independent of any database connection.
func TestValidateIdentityMigrationInput(t *testing.T) {
	validSubject := "10000000-0000-4000-8000-000000000001"
	otherSubject := "20000000-0000-4000-8000-000000000002"
	operatorID := "30000000-0000-4000-8000-000000000003"
	base := func() IdentityMigrationInput {
		return IdentityMigrationInput{
			FromIssuer: "https://auth.solov.cc/realms/solov", FromSubject: validSubject,
			ToIssuer: "https://console.solov.cc", ToSubject: otherSubject,
		}
	}

	t.Run("valid dry run", func(t *testing.T) {
		if _, _, _, _, _, err := validateIdentityMigrationInput(base()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("valid apply", func(t *testing.T) {
		in := base()
		in.Apply = true
		in.OperatorID = operatorID
		if _, _, _, _, gotOperator, err := validateIdentityMigrationInput(in); err != nil || gotOperator != operatorID {
			t.Fatalf("operator=%q err=%v", gotOperator, err)
		}
	})

	t.Run("apply without operator id", func(t *testing.T) {
		in := base()
		in.Apply = true
		if _, _, _, _, _, err := validateIdentityMigrationInput(in); err == nil {
			t.Fatal("expected error for --apply without --operator-id")
		}
	})

	t.Run("apply with malformed operator id", func(t *testing.T) {
		in := base()
		in.Apply = true
		in.OperatorID = "not-a-uuid"
		if _, _, _, _, _, err := validateIdentityMigrationInput(in); err == nil {
			t.Fatal("expected error for malformed --operator-id")
		}
	})

	t.Run("dry run does not require operator id", func(t *testing.T) {
		if _, _, _, _, _, err := validateIdentityMigrationInput(base()); err != nil {
			t.Fatalf("dry run should not require an operator id: %v", err)
		}
	})

	for name, mutate := range map[string]func(*IdentityMigrationInput){
		"from-issuer not https":       func(in *IdentityMigrationInput) { in.FromIssuer = "http://auth.solov.cc" },
		"from-issuer with query":      func(in *IdentityMigrationInput) { in.FromIssuer = "https://auth.solov.cc?x=1" },
		"from-issuer with userinfo":   func(in *IdentityMigrationInput) { in.FromIssuer = "https://u:p@auth.solov.cc" },
		"from-issuer empty":           func(in *IdentityMigrationInput) { in.FromIssuer = "" },
		"to-issuer not https":         func(in *IdentityMigrationInput) { in.ToIssuer = "ftp://console.solov.cc" },
		"to-issuer wildcard host":     func(in *IdentityMigrationInput) { in.ToIssuer = "https://*.solov.cc" },
		"from-subject not a uuid":     func(in *IdentityMigrationInput) { in.FromSubject = "cd680af8" },
		"from-subject empty":          func(in *IdentityMigrationInput) { in.FromSubject = "" },
		"to-subject not a uuid":       func(in *IdentityMigrationInput) { in.ToSubject = "not-a-uuid-at-all-000000" },
		"to-subject malformed dashes": func(in *IdentityMigrationInput) { in.ToSubject = "10000000000040008000000000000001" },
	} {
		t.Run(name, func(t *testing.T) {
			in := base()
			mutate(&in)
			if _, _, _, _, _, err := validateIdentityMigrationInput(in); err == nil {
				t.Fatalf("expected validation error for case %q", name)
			}
		})
	}

	t.Run("identical source and target identity", func(t *testing.T) {
		in := base()
		in.ToIssuer = in.FromIssuer
		in.ToSubject = in.FromSubject
		if _, _, _, _, _, err := validateIdentityMigrationInput(in); err == nil {
			t.Fatal("expected error when --from and --to name the same identity")
		}
	})

	t.Run("trims surrounding whitespace", func(t *testing.T) {
		in := base()
		in.FromIssuer = "  " + in.FromIssuer + "  "
		fromIssuer, _, _, _, _, err := validateIdentityMigrationInput(in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if fromIssuer != base().FromIssuer {
			t.Fatalf("issuer not trimmed: %q", fromIssuer)
		}
	})
}

func TestMaskIdentityEmail(t *testing.T) {
	cases := map[string]string{
		"a@example.com":   "a***",
		"ab@example.com":  "a***b",
		"abc@example.com": "a***c",
		"@example.com":    "用户",
		"no-at-sign":      "用户",
		"":                "用户",
	}
	for input, want := range cases {
		if got := maskIdentityEmail(input); got != want {
			t.Errorf("maskIdentityEmail(%q) = %q, want %q", input, got, want)
		}
	}
}
