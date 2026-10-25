package valid

import (
	"context"
	"errors"
	"fmt"

	"buf.build/go/protovalidate"
	"github.com/Auction-Application/be-auction-item/internal/apperror"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func Validate(v protovalidate.Validator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		msg, ok := req.(proto.Message)
		if !ok {
			return nil, status.Error(codes.Internal, "unexpected request type")
		}
		if err := v.Validate(msg); err != nil {
			var valErr *protovalidate.ValidationError
			if !errors.As(err, &valErr) {
				return nil, status.Error(codes.Internal, "validation failed")
			}

			br := &errdetails.BadRequest{}
			for _, viol := range valErr.Violations {
				br.FieldViolations = append(br.FieldViolations, &errdetails.BadRequest_FieldViolation{
					Field:       protovalidate.FieldPathString(viol.Proto.GetField()),
					Description: viol.Proto.GetMessage(),
				})
			}

			st, detailsErr := status.New(codes.InvalidArgument, "invalid request").WithDetails(br)
			if detailsErr != nil {
				return nil, status.Error(codes.Internal, "invalid request and internal error")
			}

			return nil, st.Err()

		}

		return handler(ctx, req)
	}
}

func codeInvalidArgumentTranslation(err apperror.AppError) error {
	br := &errdetails.BadRequest{}
	for _, viol := range err.Fields {
		br.FieldViolations = append(br.FieldViolations, &errdetails.BadRequest_FieldViolation{
			Field:       viol.Field,
			Description: viol.Description,
		})
	}

	st, detailsErr := status.New(codes.InvalidArgument, "invalid request").WithDetails(br)
	if detailsErr != nil {
		return status.Error(codes.Internal, "invalid request and internal error")
	}

	return st.Err()
}

func clientCodeStorageConflictTranslation(err apperror.AppError) error {
	preCond := &errdetails.PreconditionFailure{}
	for _, viol := range err.Fields {
		preCond.Violations = append(preCond.Violations, &errdetails.PreconditionFailure_Violation{
			Subject:     viol.Field,
			Description: viol.Description,
		})
	}

	st, detailsErr := status.New(codes.FailedPrecondition, "precondition failed").WithDetails(preCond)
	if detailsErr != nil {
		return status.Error(codes.Internal, "precondition failed and internal error")
	}

	return st.Err()
}

func codeInternalServerErrorTranslation(err apperror.AppError) error {
	return status.Error(codes.Internal, "internal server error")
}

// func noOpTranslation(err apperror.AppError) error {
// 	return nil
// }

var appErrToGRPCTranslation = map[apperror.ErrCode]func(err apperror.AppError) error{
	apperror.ClientCodeInvalidArgument: codeInvalidArgumentTranslation,
	apperror.ClientCodeStorageConflict: clientCodeStorageConflictTranslation,
	apperror.CodeInternalServerError:   codeInternalServerErrorTranslation,
}

func convertToGRPCErr(err apperror.AppError) error {
	const op apperror.Operation = "valid.convertToGRPCErr"
	translationFn, ok := appErrToGRPCTranslation[err.Code]

	if !ok {
		return codeInternalServerErrorTranslation(apperror.InternalServerError(op, err, "grpcTranslation failed"))
	}
	return translationFn(err)
}

// priority is from left to right, left most has higher priority and right most and so on.
var priorityClientDisplayErrCodes []apperror.ErrCode = []apperror.ErrCode{apperror.ClientCodeInvalidArgument, apperror.ClientCodeStorageConflict}

func searchCode(code apperror.ErrCode, err error) (apperror.AppError, bool) {
	if appErrType, ok := err.(apperror.AppError); ok && appErrType.Code == code {
		return appErrType, true
	}

	switch t := err.(type) {
	case interface{ Unwrap() error }:
		return searchCode(code, t.Unwrap())

	case interface{ Unwrap() []error }:

		for _, e := range t.Unwrap() {
			if found, ok := searchCode(code, e); ok {
				return found, ok
			}
		}

	}

	return apperror.AppError{}, false
}

func prioritySearchErrorCode(err error) apperror.AppError {
	for _, code := range priorityClientDisplayErrCodes {
		appErr, ok := searchCode(code, err)
		if ok {
			return appErr
		}
	}
	return apperror.CatchAllInternalServerError(err)
}

func toGRPCError(err error) error {
	if err == nil {
		return nil
	}

	if _, ok := status.FromError(err); ok {
		return err
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request cancelled")
	}

	appError := prioritySearchErrorCode(err)
	// todo add logging
	fmt.Println(appError)
	return convertToGRPCErr(appError)
}

// todo add loggger in errorInterceptor
func ErrorResponseInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		response, err := handler(ctx, req)
		if err == nil {
			return response, nil
		}

		return nil, toGRPCError(err)
	}
}
