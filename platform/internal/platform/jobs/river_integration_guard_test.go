package jobs

import "testing"

func TestSelectIntegrationDSNSkipsWithoutExplicitSource(t *testing.T) {
	t.Setenv("XM_TEST_DATABASE_URL", "")
	t.Setenv("XM_RUN_INTEGRATION", "")
	t.Setenv("DATABASE_URL", "")
	dsn, enabled, err := selectIntegrationDSN()
	if err != nil || enabled || dsn != "" {
		t.Fatalf("selectIntegrationDSN() = %q, %v, %v; want disabled", dsn, enabled, err)
	}
}

func TestSelectIntegrationDSNUsesCISource(t *testing.T) {
	t.Setenv("XM_TEST_DATABASE_URL", "postgres://xingmang@127.0.0.1:5432/xingmang")
	t.Setenv("XM_RUN_INTEGRATION", "")
	t.Setenv("DATABASE_URL", "")
	dsn, enabled, err := selectIntegrationDSN()
	if err != nil || !enabled || dsn == "" {
		t.Fatalf("selectIntegrationDSN() = %q, %v, %v; want CI source", dsn, enabled, err)
	}
}

func TestSelectIntegrationDSNRequiresDatabaseURLWhenOptedIn(t *testing.T) {
	t.Setenv("XM_TEST_DATABASE_URL", "")
	t.Setenv("XM_RUN_INTEGRATION", "1")
	t.Setenv("DATABASE_URL", "")
	if _, enabled, err := selectIntegrationDSN(); !enabled || err == nil {
		t.Fatalf("selectIntegrationDSN() = enabled %v, err %v; want explicit error", enabled, err)
	}
}

func TestIntegrationDSNRejectsRemoteHost(t *testing.T) {
	if err := validateIntegrationDSN("postgres://user@db.internal:5432/xingmang"); err == nil {
		t.Fatal("remote integration host must be rejected")
	}
}
