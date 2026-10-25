package imagestore

import (
	"github.com/Auction-Application/be-auction-item/internal/apperror"
	"github.com/Auction-Application/be-auction-item/internal/database"
)

func convertToFieldErr(storageErrDetails []database.StorageField) []apperror.FieldError {
	fieldErrs := make([]apperror.FieldError, 0, len(storageErrDetails))
	for _, d := range storageErrDetails {
		fieldErrs = append(fieldErrs, apperror.FieldError{
			Field:       d.Subject,
			Description: d.Description,
		})
	}
	return fieldErrs
}
