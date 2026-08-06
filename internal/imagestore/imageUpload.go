package imagestore

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Auction-Application/be-auction-item/internal/database/auctionLotTableQuery"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"
)

const (
	multipartThreshold = 150 * 1024 * 1024 // 150MB
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

type UploadFile struct {
	Sha256       string
	FileName     string
	ClientFileId string
	FileSize     uint
}

type PresignedUploadUrl struct {
	Single *v4.PresignedHTTPRequest
	Multi  []*v4.PresignedHTTPRequest
}

type PresignedFileUrl struct {
	UploadFile
	PresignedUploadUrl
}

func (s3Storage S3Storage) generateS3UploadUrl(ctx context.Context, files []UploadFile, bucketName string, lotId uuid.UUID, query *auctionLotTableQuery.Queries) ([]PresignedFileUrl, error) {
	fileUploads := make([]PresignedFileUrl, 0, len(files))

	type SingleUploadFile = UploadFile
	type MultiUploadFile = UploadFile
	singleFileUploads := make([]SingleUploadFile, 0)
	multiFileUploads := make([]MultiUploadFile, 0)
	singlePartFileSha256s := make([]string, 0)
	singlePartFileSizes := make([]int32, 0)
	singlePartFileContentTypes := make([]string, 0)
	singlePartFileNames := make([]string, 0)
	singlePartFileStorageKeys := make([]uuid.UUID, 0)

	multiPartFileSha256s := make([]string, 0)
	for _, file := range files {

		if file.FileSize < multipartThreshold {
			singleFileUploads = append(singleFileUploads, file)
			singlePartFileSha256s = append(singlePartFileSha256s, file.Sha256)
			singlePartFileSizes = append(singlePartFileSizes, int32(file.FileSize))
			singlePartFileContentTypes = append(singlePartFileContentTypes, "image/jpeg")
			singlePartFileNames = append(singlePartFileNames, file.FileName)
			uuid, err := uuid.NewRandom()
			if err != nil {
				return nil, err
			}
			singlePartFileStorageKeys = append(singlePartFileStorageKeys, uuid)

			notMultipartFileUpload, err := s3Storage.GenerateSinglePresignedPutObjectUrl(ctx, bucketName, file.Sha256)
			if err != nil {
				fmt.Println("Error")
				fmt.Println(err)
				return nil, err
			}
			fileUploads = append(fileUploads, PresignedFileUrl{UploadFile: file, PresignedUploadUrl: PresignedUploadUrl{Single: notMultipartFileUpload}})

		} else {
			multiFileUploads = append(multiFileUploads, file)
			multiPartFileSha256s = append(multiPartFileSha256s, file.Sha256)
			// multipartFileUpload, uploadId, err := s3Storage.GenerateMultiPartPresignedUrl(ctx, bucketName, file.Sha256, file)
			// if err != nil {
			// 	fmt.Println("Error")
			// 	fmt.Println(err)
			// 	return nil, err
			// }
			// fileUploads = append(fileUploads, PresignedFileUrl{UploadFile: file, PresignedUploadUrl: PresignedUploadUrl{Multi: multipartFileUpload}})

		}

	}

	err := query.InsertSinglePartUpload(context.TODO(), auctionLotTableQuery.InsertSinglePartUploadParams{
		UploadType:   auctionLotTableQuery.UploadTypeSingleUpload,
		LotID:        lotId,
		Username:     "coackroach",
		Sha256s:      singlePartFileSha256s,
		FileSizes:    singlePartFileSizes,
		ContentTypes: singlePartFileContentTypes,
		FilesNames:   singlePartFileNames,
		StorageKeys:  singlePartFileStorageKeys,
	})

	if err != nil {
		return nil, err
	}

	multiUploadItems, err := query.ListMultiUploadItems(context.TODO(), auctionLotTableQuery.ListMultiUploadItemsParams{
		Sha256s: multiPartFileSha256s,
		LotID:   lotId,
	})

	if err != nil {
		return nil, err
	}

	validUploadIds := make([]string, 0)
	existingImageBlobUploadAttemptIds := make([]int64, 0)

	for _, item := range multiUploadItems {
		if !item.ValidUntil.Valid {
			return nil, errors.New("Time not valid in multipart upload,data has been corrupted")
		}

		if !time.Now().After(item.ValidUntil.Time) {

			// valid
		} else {
			uuid, err := uuid.NewRandom()

			if err != nil {
				return nil, err
			}

			//todo make this uuidText a function
			uuidText := uuid.String()
			existingImageBlobUploadAttemptIds = append(existingImageBlobUploadAttemptIds, item.ImageBlobUploadAttemptID)
			_, newUploadId, err := s3Storage.GenerateMultiPartPresignedUrl(context.TODO(), "demoBucketName", uuidText, uint(item.FileSize))
			if err != nil {
				return nil, err
			}

			validUploadIds = append(validUploadIds, *newUploadId)

			// not-valid

		}
	}

	err = query.ValidateMultipartUpload(context.TODO(), auctionLotTableQuery.ValidateMultipartUploadParams{
		PartSize:                  partSize,
		UploadIds:                 validUploadIds,
		ImageBlobUploadAttemptIds: existingImageBlobUploadAttemptIds,
	})

	if err != nil {
		return nil, err
	}

	return fileUploads, nil
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

func (s3Storage S3Storage) GenerateMultiPartPresignedUrl(ctx context.Context, bucketName string, objectKey string, fileSize uint) ([]*v4.PresignedHTTPRequest, *string, error) {
	multiPartCreated, err := s3Storage.s3Client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: &bucketName, Key: &objectKey})

	if err != nil {
		fmt.Println(err)
		return nil, nil, fmt.Errorf("Create multipart upload error:%w", err)
	}

	numParts := (fileSize + partSize - 1) / partSize
	partUrls := make([]*v4.PresignedHTTPRequest, 0, numParts)

	for i := range numParts {
		partNumber := int32(i + 1)

		presignedUploadPartUrls, err := s3Storage.Presigner.PresignUploadPart(ctx, &s3.UploadPartInput{Bucket: &bucketName, Key: &objectKey, PartNumber: &partNumber, UploadId: multiPartCreated.UploadId}, s3.WithPresignExpires(presignExpiry))

		if err != nil {
			fmt.Println(err)
			return nil, nil, fmt.Errorf("Creating mulipart upload url failed: %w", err)
		}

		partUrls = append(partUrls, presignedUploadPartUrls)

	}

	return partUrls, multiPartCreated.UploadId, nil

}

type DuplicateCheckFilesToUpload struct {
	UploadFile
	duplicate bool
}

type DuplicateFile = UploadFile

func IntentBatchUpload(fileToUpload []UploadFile) ([]UploadFile, []DuplicateFile) {

	isSeenFileMap := make(map[string]bool, len(fileToUpload))
	var duplicateFiles []DuplicateFile
	var files []UploadFile

	for _, v := range fileToUpload {

		if isSeenFileMap[v.Sha256] {
			duplicateFiles = append(duplicateFiles, v)
		} else {
			files = append(files, v)
		}

		isSeenFileMap[v.Sha256] = true
	}

	return files, duplicateFiles

}

func (imageStore *ImageStore) initiateUpload(fileToUpload []UploadFile, bucketName string, lotId string) ([]DuplicateFile, []AlreadyUploadedFile, []PresignedFileUrl, error) {
	files, duplicateFiles := IntentBatchUpload(fileToUpload)
	lotIdUUID, err := uuid.Parse(lotId)
	if err != nil {
		return nil, nil, nil, err
	}
	fmt.Println(duplicateFiles)
	tx, err := imageStore.conn.Begin(context.TODO())
	if err != nil {
		return nil, nil, nil, err
	}
	defer tx.Rollback(context.TODO())
	qtx := imageStore.dbStorageQuery.WithTx(tx)
	needToBeUploadFiles, alreadyUploadedFiles, err := skipUploadForIdenticalImageBlobs(files, lotIdUUID, qtx)
	if err != nil {
		fmt.Println(err)
		return nil, nil, nil, err
	}
	presignedUrls, err := imageStore.s3Storage.generateS3UploadUrl(context.TODO(), needToBeUploadFiles, bucketName, lotIdUUID, qtx)
	if err != nil {
		fmt.Println(err)
		return nil, nil, nil, err
	}
	err = tx.Commit(context.TODO())
	if err != nil {
		return nil, nil, nil, err
	}
	return duplicateFiles, alreadyUploadedFiles, presignedUrls, nil

}

type AlreadyUploadedFile = UploadFile

func skipUploadForIdenticalImageBlobs(files []UploadFile, lotId uuid.UUID, query *auctionLotTableQuery.Queries) ([]UploadFile, []AlreadyUploadedFile, error) {
	var alreadyUploadedFiles []AlreadyUploadedFile
	var needToBeUploadedFiles []UploadFile
	sha256s := make([]string, 0, len(files))
	fileNames := make([]string, 0, len(files))

	fileMap := make(map[string]UploadFile, len(files))
	// lotIds := slices.Repeat([]uuid.UUID{lotId}, len(files))
	for _, f := range files {
		sha256s = append(sha256s, f.Sha256)
		fileNames = append(fileNames, f.FileName)
		fileMap[f.Sha256] = f
	}

	identicalBlobs, err := query.InsertIdenticalImageBlobsToLotImages(context.TODO(), auctionLotTableQuery.InsertIdenticalImageBlobsToLotImagesParams{
		Sha256s:   sha256s,
		LotID:     lotId,
		FileNames: fileNames,
	})

	if err != nil {
		return nil, nil, err
	}

	existingFileMap := make(map[string]auctionLotTableQuery.InsertIdenticalImageBlobsToLotImagesRow, len(identicalBlobs))

	for _, exisitingFile := range identicalBlobs {
		existingFileMap[exisitingFile.Sha256] = exisitingFile
	}

	for _, file := range fileMap {
		if _, ok := existingFileMap[file.Sha256]; ok {
			alreadyUploadedFiles = append(alreadyUploadedFiles, file)
		} else {
			needToBeUploadedFiles = append(needToBeUploadedFiles, file)
		}
	}

	return needToBeUploadedFiles, alreadyUploadedFiles, nil

}
