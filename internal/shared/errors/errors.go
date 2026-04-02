package errors

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Sentinel errors — compare with errors.Is()
var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrUnauthorized  = errors.New("unauthorized")
	ErrForbidden     = errors.New("forbidden")
	ErrBadRequest    = errors.New("bad request")
	ErrInternal      = errors.New("internal error")
)

// AppError wraps a sentinel with a human-readable message and HTTP status.
type AppError struct {
	Err     error  // sentinel, used for errors.Is()
	Message string // safe to expose to client
	Code    int    // HTTP status code
}

func (e *AppError) Error() string { return e.Message }
func (e *AppError) Unwrap() error { return e.Err }

func NewNotFound(entity string) *AppError {
	return &AppError{Err: ErrNotFound, Message: fmt.Sprintf("%s not found", entity), Code: http.StatusNotFound}
}

func NewAlreadyExists(entity string) *AppError {
	return &AppError{Err: ErrAlreadyExists, Message: fmt.Sprintf("%s already exists", entity), Code: http.StatusConflict}
}

func NewBadRequest(msg string) *AppError {
	return &AppError{Err: ErrBadRequest, Message: msg, Code: http.StatusBadRequest}
}

func NewInternal(err error) *AppError {
	return &AppError{Err: ErrInternal, Message: "internal server error", Code: http.StatusInternalServerError}
}

// MapPgError converts pgx/pgconn errors to sentinel errors.
// Call this at the repository boundary — never let raw pgx errors leak up.
func MapPgError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return ErrAlreadyExists
		case "23503": // foreign_key_violation
			return ErrNotFound
		case "23514": // check_violation
			return ErrBadRequest
		}
	}
	return err
}
