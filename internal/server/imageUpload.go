package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

const (
	multipartThreshold = 150 * 1024 * 1024 // 20MB
	partSize           = 10 * 1024 * 1024  // 10MB
	presignExpiry      = 15 * time.Minute
)

type S3Storage struct {
	s3Client  *s3.Client
	Presigner *s3.PresignClient
}

func NewS3Storage() (*S3Storage, error) {
	ctx := context.Background()
	sdkConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	s3Client := s3.NewFromConfig(sdkConfig)
	return &S3Storage{
		s3Client:  s3Client,
		Presigner: s3.NewPresignClient(s3Client),
	}, nil
}

func (s3Storage S3Storage) UploadFile(ctx context.Context, bucketName string, objectKey string, fileName string) error {
	file, err := os.Open(fileName)
	if err != nil {
		log.Printf("Couldn't open file %v to upload. Here's why: %v\n", fileName, err)
		return err
	} else {
		_, err := s3Storage.s3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(bucketName),
			Key:         aws.String(objectKey),
			Body:        file,
			ContentType: aws.String("image/png"),
		})

		if err != nil {
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) && apiErr.ErrorCode() == "EntityTooLarge" {
				log.Printf("Error while uploading object to %s. The object is too large.\n"+
					"To upload objects larger than 5GB, use the S3 console (160GB max)\n"+
					"or the multipart upload API (5TB max).", bucketName)
			} else {
				log.Printf("Couldn't upload file %v to %v:%v. Here's why: %v\n",
					fileName, bucketName, objectKey, err)
			}
			return err
		}

	}
	defer file.Close()
	return nil
}

type FilesToUpload struct {
	Sha256       string
	FileName     string
	ClientFileId string
	FileSize     uint
}

type PresignedUpload struct {
	Single *v4.PresignedHTTPRequest
	Multi  []*v4.PresignedHTTPRequest
}

type PresignedFileUrl struct {
	FilesToUpload
	PresignedUpload
}

func (s3Storage S3Storage) generateS3UploadUrl(ctx context.Context, files []FilesToUpload, bucketName string) []PresignedFileUrl {
	var fileUploads []PresignedFileUrl

	for _, file := range files {
		if file.FileSize < multipartThreshold {
			notMultipartFileUpload, err := s3Storage.GenerateSinglePresignedPutObjectUrl(ctx, bucketName, file.Sha256)
			if err != nil {
				fmt.Println("Error")
				fmt.Println(err)
			}
			fileUploads = append(fileUploads, PresignedFileUrl{FilesToUpload: file, PresignedUpload: PresignedUpload{Single: notMultipartFileUpload}})
		} else {
			multipartFileUpload, err := s3Storage.GenerateMultiPartPresignedUrl(ctx, bucketName, file.Sha256, file)
			if err != nil {
				fmt.Println("Error")
				fmt.Println(err)
			}
			fileUploads = append(fileUploads, PresignedFileUrl{FilesToUpload: file, PresignedUpload: PresignedUpload{Multi: multipartFileUpload}})
			// multipartFileUploads=append(fileUploads, multipartFileUpload)
		}

	}
	return fileUploads
}

func (s3Storage S3Storage) GenerateSinglePresignedPutObjectUrl(
	ctx context.Context, bucketName string, objectKey string) (*v4.PresignedHTTPRequest, error) {
	presignResult, err := s3Storage.Presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectKey),
	}, s3.WithPresignExpires(presignExpiry))
	if err != nil {
		log.Printf("Couldn't get a presigned request to put %v:%v. Here's why: %v\n",
			bucketName, objectKey, err)
	}
	return presignResult, err
}

func (s3Storage S3Storage) GenerateMultiPartPresignedUrl(ctx context.Context, bucketName string, objectKey string, file FilesToUpload) ([]*v4.PresignedHTTPRequest, error) {
	multiPartCreated, err := s3Storage.s3Client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: &bucketName, Key: &objectKey})

	if err != nil {
		fmt.Println(err)
		return nil, fmt.Errorf("Create multipart upload error:%w", err)
	}

	numParts := (file.FileSize + partSize - 1) / partSize
	partUrls := make([]*v4.PresignedHTTPRequest, numParts)

	for i := uint(0); i < numParts; i++ {
		partNumber := int32(i + 1)

		presignedUploadPartUrls, err := s3Storage.Presigner.PresignUploadPart(ctx, &s3.UploadPartInput{Bucket: &bucketName, Key: &objectKey, PartNumber: &partNumber, UploadId: multiPartCreated.UploadId}, s3.WithPresignExpires(presignExpiry))

		if err != nil {
			fmt.Println(err)
			return nil, fmt.Errorf("Creating mulipart upload url failed: %w", err)
		}

		partUrls = append(partUrls, presignedUploadPartUrls)

	}

	return partUrls, nil

}

type DuplicateCheckFilesToUpload struct {
	FilesToUpload
	duplicate bool
}

type DuplicateFiles FilesToUpload

func IntentBatchUpload(fileToUpload []FilesToUpload) ([]FilesToUpload, []DuplicateFiles) {

	// duplicateChecked := make([]DuplicateCheckFilesToUpload, 0, len(fileToUpload))
	isSeenFileMap := make(map[string]bool, len(fileToUpload))
	var duplicateFiles []DuplicateFiles
	var files []FilesToUpload

	for _, v := range fileToUpload {

		if isSeenFileMap[v.Sha256] {
			duplicateFiles = append(duplicateFiles, DuplicateFiles(v))
		} else {
			files = append(files, v)
		}
		// duplicateChecked = append(duplicateChecked, DuplicateCheckFilesToUpload{duplicate: isSeenFileMap[v.Sha256], FilesToUpload: v})

		isSeenFileMap[v.Sha256] = true
	}

	return files, duplicateFiles

}
