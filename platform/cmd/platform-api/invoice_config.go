package main

import (
	"errors"
	"strings"
)

func validateUnifiedModes(environment, staffMode string, getenv func(string) string) error {
	invoiceEnv := strings.ToLower(strings.TrimSpace(getenv("APP_ENV")))
	if invoiceEnv == "" {
		invoiceEnv = "development"
	}
	if invoiceEnv != environment {
		return errors.New("unified service requires matching ENVIRONMENT and APP_ENV")
	}
	if environment == "production" && (staffMode != "local" || strings.TrimSpace(getenv("AUTH_MODE")) != "session") {
		return errors.New("unified production service requires XM_AUTH_MODE=local and AUTH_MODE=session")
	}
	return nil
}
