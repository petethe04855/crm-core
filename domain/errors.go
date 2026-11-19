// Package domain holds cross-module CRM domain types: shared errors and the
// acting staff identity passed into usecases.
package domain

import "errors"

// Domain errors returned by crm-core usecases. Delivery layers map them to
// HTTP status codes; they never leak database or driver errors.
var (
	ErrNotFound   = errors.New("resource not found")
	ErrValidation = errors.New("validation error")
	ErrConflict   = errors.New("resource conflict")
	ErrForbidden  = errors.New("forbidden")
	// ErrInsufficientPoints means a deduction would take the balance below zero.
	ErrInsufficientPoints = errors.New("insufficient points")

	// Redeem guard failures (plan §4.2). Each maps to its own 409 code so the
	// LINE app can explain exactly why a redemption was refused.
	ErrMemberTooNew      = errors.New("member is too new to redeem")
	ErrDailyLimitReached = errors.New("daily redemption limit reached")
	ErrEmailNotVerified  = errors.New("email is not verified")
	ErrMemberNotAllowed  = errors.New("member status does not allow redemption")
)

// ValidationError carries a human-readable reason and unwraps to ErrValidation.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

func (e *ValidationError) Unwrap() error { return ErrValidation }

// NewValidationError builds a ValidationError for the given field.
func NewValidationError(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}
