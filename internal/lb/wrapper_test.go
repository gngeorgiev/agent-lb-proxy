package lb

import "testing"

func TestValidateAliasAllowsEmailLikeAlias(t *testing.T) {
	t.Parallel()

	if err := ValidateAlias("g99517399@gmail.com"); err != nil {
		t.Fatalf("expected email-like alias to be valid: %v", err)
	}
}

func TestValidateAliasRejectsPathSeparator(t *testing.T) {
	t.Parallel()

	if err := ValidateAlias("bad/alias"); err == nil {
		t.Fatal("expected alias with path separator to be rejected")
	}
}
