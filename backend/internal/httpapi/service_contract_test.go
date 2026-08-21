package httpapi

import "invoice-system/backend/internal/application"

var _ InvoiceService = (*application.Service)(nil)
var _ OperationsService = (*application.Service)(nil)
