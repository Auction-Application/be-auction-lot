package imagestore

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Auction-Application/be-auction-item/internal/database/auctionLotTableQuery"
	lotimage "github.com/Auction-Application/be-auction-item/rpc/gen/lot_image/v1"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type ImageStore struct {
	lotimage.UnimplementedLotImageServiceServer
	dbStorageQuery *auctionLotTableQuery.Queries
	s3Storage      *S3Storage
	conn           *pgx.Conn
}

func (imageStore *ImageStore) GeneratePresignedUrl(ctx context.Context, presignRequest *lotimage.GeneratePresignedUrlRequest) (*lotimage.GeneratePresignedUrlResponse, error) {
	duplicateFiles, alreadUploadedFiles, presignedUrls, err := imageStore.initiateUpload(convertToUploadFiles(presignRequest.LotImages), "arn:aws:s3:ap-south-1:433154991296:accesspoint/auction-lot-service-access-point", *presignRequest.LotId)
	if err != nil {
		return nil, err
	}

	dulicateImageFiles, err := convertToImageFiles(duplicateFiles)
	if err != nil {
		return nil, status.Error(codes.Internal, "error converting from duplicatedFiles")
	}
	alreadyUploadedImageFiles, err := convertToImageFiles(alreadUploadedFiles)
	if err != nil {
		return nil, status.Error(codes.Internal, "error converting from alreadyUploadedFiles")
	}

	presignedImageFiles, err := convertToPresignedImageFiles(presignedUrls)
	if err != nil {
		return nil, status.Error(codes.Internal, "error converting from alreadyUploadedFiles")
	}

	response := &lotimage.GeneratePresignedUrlResponse{
		Success: new(true),
		Message: new("Presigned url created"),
		PresignResult: &lotimage.PresignFileResult{
			DuplicateFiles:       dulicateImageFiles,
			AlreadyUploadedFiles: alreadyUploadedImageFiles,
			PresignedFiles:       presignedImageFiles,
		},
	}

	return response, nil
}

func convertToPresignedImageFiles(presignedUrls []PresignedFileUrl) ([]*lotimage.PresignedFile, error) {
	presignedImageFileUrls := make([]*lotimage.PresignedFile, 0, len(presignedUrls))
	for _, v := range presignedUrls {
		imageUrl, err := convertToPresignedFile(v)
		if err != nil {
			return nil, err
		}
		presignedImageFileUrls = append(presignedImageFileUrls, imageUrl)
	}

	return presignedImageFileUrls, nil
}

func convertToPresignedFile(presignedUrl PresignedFileUrl) (*lotimage.PresignedFile, error) {
	imageFile, err := convertToImageFile(presignedUrl.UploadFile)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	presignedUploadUrl := presignedUrl.PresignedUploadUrl

	var presignedImageUploadUrl *lotimage.PresignedUploadUrl

	if presignedUploadUrl.Single != nil {
		singleUpload := &lotimage.PresignedUploadUrl_Single{
			Single: convertToLotImageSinglePresignedHttpRequest(presignedUploadUrl.Single),
		}
		presignedImageUploadUrl = &lotimage.PresignedUploadUrl{
			PresignedUrl: singleUpload,
		}

	} else {
		multiUpload := &lotimage.PresignedUploadUrl_Multi{
			Multi: &lotimage.MultiPresignedUploadUrl{
				MultiParts: convertToLotImageMultiPartPresignedHttpRequest(presignedUploadUrl.Multi),
				UploadId:   &presignedUrl.Multi.uploadId,
				PartSize:   &presignedUrl.Multi.partSize,
			},
		}
		presignedImageUploadUrl = &lotimage.PresignedUploadUrl{
			PresignedUrl: multiUpload,
		}

	}
	return &lotimage.PresignedFile{
		UploadFile:         imageFile,
		PresignedUploadUrl: presignedImageUploadUrl,
	}, nil
}

func convertToLotImageMultiPresignedHttpRequest(multiPresignedRequest MultiPresignedRequest) *lotimage.MultiPresignedHTTPRequest {
	signedHeader := make(map[string]*lotimage.HeaderValues, len(multiPresignedRequest.request.SignedHeader))
	for key, values := range multiPresignedRequest.request.SignedHeader {
		signedHeader[key] = &lotimage.HeaderValues{
			Values: values,
		}
	}

	presignRequest := &lotimage.MultiPresignedHTTPRequest{
		Request: &lotimage.Request{
			Url:          proto.String(multiPresignedRequest.request.URL),
			Method:       proto.String(multiPresignedRequest.request.Method),
			SignedHeader: signedHeader,
		},
		Part: proto.Int32(int32(multiPresignedRequest.part)),
	}
	return presignRequest
}

func convertToLotImageSinglePresignedHttpRequest(presignedRequest *v4.PresignedHTTPRequest) *lotimage.Request {
	signedHeader := make(map[string]*lotimage.HeaderValues, len(presignedRequest.SignedHeader))
	for key, values := range presignedRequest.SignedHeader {
		signedHeader[key] = &lotimage.HeaderValues{
			Values: values,
		}
	}

	presignRequest := &lotimage.Request{
		Url:          proto.String(presignedRequest.URL),
		Method:       proto.String(presignedRequest.Method),
		SignedHeader: signedHeader,
	}

	return presignRequest
}

func convertToLotImageMultiPartPresignedHttpRequest(multiPresignedUrl MultiPresignedUrl) []*lotimage.MultiPresignedHTTPRequest {
	lotImagePresignedRequests := make([]*lotimage.MultiPresignedHTTPRequest, 0, len(multiPresignedUrl.requests))
	for _, request := range multiPresignedUrl.requests {
		lotImagePresignedRequests = append(lotImagePresignedRequests, convertToLotImageMultiPresignedHttpRequest(request))
	}

	return lotImagePresignedRequests
}

func convertToUploadFiles(imageFiles []*lotimage.ImageFile) []UploadFile {
	uploadFiles := make([]UploadFile, 0, len(imageFiles))
	for _, image := range imageFiles {
		uploadFiles = append(uploadFiles, convertToUploadFile(image))
	}

	return uploadFiles
}

func convertToUploadFile(imageFile *lotimage.ImageFile) UploadFile {
	uploadFile := UploadFile{
		Sha256:       *imageFile.Sha256,
		FileName:     *imageFile.FileName,
		ClientFileId: strconv.Itoa(int(*imageFile.ClientFileId)),
		FileSize:     uint(*imageFile.FileSize),
	}
	return uploadFile
}

func convertToImageFiles(files []UploadFile) ([]*lotimage.ImageFile, error) {
	imageFiles := make([]*lotimage.ImageFile, 0, len(files))
	for _, file := range files {
		iFile, err := convertToImageFile(file)
		if err != nil {
			fmt.Println("Error when converting to ImageFile")
			return nil, err
		}
		imageFiles = append(imageFiles, iFile)

	}

	return imageFiles, nil
}

func convertToImageFile(file UploadFile) (*lotimage.ImageFile, error) {
	clientId, err := strconv.Atoi(file.ClientFileId)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	imageFile := &lotimage.ImageFile{
		Sha256:       new(file.Sha256),
		FileName:     new(file.FileName),
		ClientFileId: new(int32(clientId)),
		FileSize:     new(uint64(file.FileSize)),
	}
	return imageFile, nil
}

func NewImageStore(databaseConnection *pgx.Conn) *ImageStore {
	s3Storage, err := NewS3Storage()
	if err != nil {
		fmt.Println("Error creating s3Storage")
		fmt.Println(err)
		return nil
	}
	return &ImageStore{
		dbStorageQuery: auctionLotTableQuery.New(databaseConnection),
		s3Storage:      s3Storage,
		conn:           databaseConnection,
	}
}
