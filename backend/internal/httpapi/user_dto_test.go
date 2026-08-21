package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"invoice-system/backend/internal/domain"
)

func TestUserInvoiceRequestDTOHidesIssuerCode(t *testing.T) {
	body, err := json.Marshal(userRequestDTOs([]domain.InvoiceRequest{{
		ID:          "request-1",
		IssuerCode:  "issuer-revision-secret",
		ServiceItem: domain.FixedServiceItem,
	}}))
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(body)
	if strings.Contains(encoded, "issuer_code") || strings.Contains(encoded, "issuer-revision-secret") {
		t.Fatalf("user response leaked issuer routing data: %s", encoded)
	}
	if !strings.Contains(encoded, `"service_item":"技术服务"`) {
		t.Fatalf("user response lost the public service item: %s", encoded)
	}
}
