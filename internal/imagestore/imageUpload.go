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
type MultiUploadFile = UploadFile

func (s3Storage S3Storage) generateS3UploadUrl(ctx context.Context, files []UploadFile, bucketName string,
	lotId uuid.UUID, query *auctionLotTableQuery.Queries,
) ([]PresignedFileUrl, error) {
	fileUploads := make([]PresignedFileUrl, 0, len(files))

	type SingleUploadFile = UploadFile

	singleFileUploads := make([]SingleUploadFile, 0)

	multiFileUploadMap := make(map[string]MultiUploadFile)

	// todo make slices inside struct instead of many slices as variables
	singlePartFileSha256s := make([]string, 0)
	singlePartFileSizes := make([]int32, 0)
	singlePartFileContentTypes := make([]string, 0)
	singlePartFileNames := make([]string, 0)

	multiPartFileSha256s := make([]string, 0)
	multiPartFileSizes := make([]int32, 0)
	multiPartFileContentTypes := make([]string, 0)
	multiPartFileNames := make([]string, 0)
	multiPartFileStorageKeys := make([]uuid.UUID, 0)
	multiPartFileUploadIds := make([]string, 0)
	for _, file := range files {
		if file.FileSize < multipartThreshold {
			singleFileUploads = append(singleFileUploads, file)
			singlePartFileSha256s = append(singlePartFileSha256s, file.Sha256)
			singlePartFileSizes = append(singlePartFileSizes, int32(file.FileSize))
			singlePartFileContentTypes = append(singlePartFileContentTypes, "image/jpeg")
			singlePartFileNames = append(singlePartFileNames, file.FileName)

		} else {
			multiFileUploadMap[file.Sha256] = file
			multiPartFileSha256s = append(multiPartFileSha256s, file.Sha256)
			multiPartFileContentTypes = append(multiPartFileContentTypes, "image/jpeg")
			multiPartFileNames = append(multiPartFileNames, file.FileName)
			multiPartFileSizes = append(multiPartFileSizes, int32(file.FileSize))
			generatedUUID, err := makeUUIDText()
			if err != nil {
				return nil, err
			}
			multiPartFileStorageKeys = append(multiPartFileStorageKeys, uuid.MustParse(generatedUUID))
			uploadId, err := generateMultiPartUploadId(context.TODO(), s3Storage, bucketName, generatedUUID)
			if err != nil {
				return nil, err
			}
			multiPartFileUploadIds = append(multiPartFileUploadIds, uploadId)

		}
	}

	if len(singlePartFileSha256s) > 0 {
		insertedSinglePartFiles, err := query.InsertSinglePartUpload(context.TODO(), auctionLotTableQuery.InsertSinglePartUploadParams{
			UploadType:   auctionLotTableQuery.UploadTypeSingleUpload,
			LotID:        lotId,
			Username:     "coackroach",
			Sha256s:      singlePartFileSha256s,
			FileSizes:    singlePartFileSizes,
			ContentTypes: singlePartFileContentTypes,
			FilesNames:   singlePartFileNames,
		})

		insertedSinglePartFilesMap := make(map[string]uuid.UUID)

		for _, insertedSingleFile := range insertedSinglePartFiles {
			insertedSinglePartFilesMap[insertedSingleFile.Sha256] = insertedSingleFile.StorageKey
		}

		if err != nil {
			return nil, err
		}

		for _, singleUploadFile := range singleFileUploads {
			notMultipartFileUpload, err := s3Storage.GenerateSinglePresignedPutObjectUrl(ctx,
				bucketName, insertedSinglePartFilesMap[singleUploadFile.Sha256].String())
			if err != nil {
				fmt.Println("Error")
				fmt.Println(err)
				return nil, err
			}
			fileUploads = append(fileUploads, PresignedFileUrl{
				UploadFile:         singleUploadFile,
				PresignedUploadUrl: PresignedUploadUrl{Single: notMultipartFileUpload},
			})

		}
	}

	if len(multiPartFileSha256s) > 0 {
		multiUploadResult, err := query.InsertAndValidateMultiPartUpload(
			context.TODO(), auctionLotTableQuery.InsertAndValidateMultiPartUploadParams{
				UploadType:   auctionLotTableQuery.UploadTypeMultiUpload,
				PartSize:     partSize,
				LotID:        lotId,
				Username:     "dummyUsername",
				Sha256s:      multiPartFileSha256s,
				FileSizes:    multiPartFileSizes,
				ContentTypes: multiPartFileContentTypes,
				FileNames:    multiPartFileNames,
				StorageKeys:  multiPartFileStorageKeys,
				UploadIds:    multiPartFileUploadIds,
			})

		newMultiUploads, resumableMultiUploads := segregateMultiUploadFiles(multiUploadResult)
		newPresignedUrls, err := generateUrlsForNewUploads(newMultiUploads, bucketName, s3Storage, multiFileUploadMap)
		if err != nil {
			return nil, err
		}

		fileUploads = append(fileUploads, newPresignedUrls...)

		resumablePresignedUrls, err := genrateUrlsForResumableUploads(resumableMultiUploads, bucketName, s3Storage, multiFileUploadMap)
		if err != nil {
			return nil, err
		}

		fileUploads = append(fileUploads, resumablePresignedUrls...)
	}

	return fileUploads, nil
}

type newMultiPartGenerationData struct {
	objectKey  string
	fileParts  []int16
	uploadId   string
	storageKey string
	sha256     string
}

type resumableValidMultiPartGenerationData = newMultiPartGenerationData

func segregateMultiUploadFiles(multiUploadResult []auctionLotTableQuery.InsertAndValidateMultiPartUploadRow) ([]newMultiPartGenerationData, []resumableValidMultiPartGenerationData) {
	newMultiUploads := make([]newMultiPartGenerationData, 0)
	resumableMultiUploads := make([]resumableValidMultiPartGenerationData, 0)

	for _, multiUpload := range multiUploadResult {
		if multiUpload.IsInserted || (!multiUpload.IsInserted && !multiUpload.IsValid) {
			newMultiUploads = append(newMultiUploads, newMultiPartGenerationData{
				objectKey:  multiUpload.StorageKey.String(),
				fileParts:  multiUpload.Parts,
				uploadId:   *multiUpload.UploadID,
				storageKey: multiUpload.StorageKey.String(),
				sha256:     multiUpload.Sha256,
			})
		} else if multiUpload.IsValid {
			resumableMultiUploads = append(resumableMultiUploads, resumableValidMultiPartGenerationData{
				objectKey:  multiUpload.StorageKey.String(),
				fileParts:  multiUpload.Parts,
				uploadId:   *multiUpload.UploadID,
				storageKey: multiUpload.StorageKey.String(),
				sha256:     multiUpload.Sha256,
			})
		}
	}

	return newMultiUploads, resumableMultiUploads
}

func generateUrlsForNewUploads(newMultiUploads []newMultiPartGenerationData, bucketName string, s3Storage S3Storage,
	multiFileUploadMap map[string]MultiUploadFile,
) ([]PresignedFileUrl, error) {
	result := make([]PresignedFileUrl, 0, len(newMultiUploads))
	for _, upload := range newMultiUploads {
		newUrls, err := generateNewMultiPartUploadUrls(context.TODO(), bucketName, upload.storageKey, upload.fileParts,
			upload.uploadId, s3Storage)
		if err != nil {
			return nil, err
		}

		result = append(result, PresignedFileUrl{
			UploadFile:         multiFileUploadMap[upload.sha256],
			PresignedUploadUrl: PresignedUploadUrl{Multi: newUrls},
		})
	}
	return result, nil
}

func genrateUrlsForResumableUploads(resumableMultiUploads []resumableValidMultiPartGenerationData, bucketName string,
	s3Storage S3Storage, multiFileUploadMap map[string]MultiUploadFile,
) ([]PresignedFileUrl, error) {
	result := make([]PresignedFileUrl, 0, len(resumableMultiUploads))
	for _, upload := range resumableMultiUploads {
		newUrls, err := generateResumeUploadUrls(s3Storage, bucketName, upload.storageKey, upload.uploadId, upload.fileParts)
		if err != nil {
			return nil, err
		}

		result = append(result, PresignedFileUrl{
			UploadFile:         multiFileUploadMap[upload.sha256],
			PresignedUploadUrl: PresignedUploadUrl{Multi: newUrls},
		})
	}

	return result, nil
}

func (s3Storage S3Storage) GenerateSinglePresignedPutObjectUrl(
	ctx context.Context, bucketName string, objectKey string,
) (*v4.PresignedHTTPRequest,
	error,
) {
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

func generateMultiPartUploadId(ctx context.Context, s3Storage S3Storage, bucketName string, objectKey string) (string, error) {
	multiPartCreated, err := s3Storage.s3Client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: &bucketName, Key: &objectKey})
	if err != nil {
		fmt.Println(err)
		return "", fmt.Errorf("Create multipart upload error:%w", err)
	}

	return *multiPartCreated.UploadId, nil
}

func generateNewMultiPartUploadUrls(ctx context.Context, bucketName string, storageKey string, fileParts []int16, uploadId string,
	s3Storage S3Storage,
) ([]*v4.PresignedHTTPRequest, error) {
	allPartUrls := make([]*v4.PresignedHTTPRequest, 0, len(fileParts))

	for _, partNumber := range fileParts {

		presignedUploadPartUrls, err := s3Storage.Presigner.PresignUploadPart(ctx, &s3.UploadPartInput{
			Bucket: &bucketName,
			Key:    &storageKey, PartNumber: aws.Int32(int32(partNumber)), UploadId: &uploadId,
		}, s3.WithPresignExpires(presignExpiry))
		if err != nil {
			fmt.Println(err)
			return nil, fmt.Errorf("Creating mulipart upload url failed: %w", err)
		}

		allPartUrls = append(allPartUrls, presignedUploadPartUrls)

	}

	return allPartUrls, nil
}

func generateResumeUploadUrls(s3Storage S3Storage, bucketName string, storageKey string, uploadId string,
	allParts []int16,
) ([]*v4.PresignedHTTPRequest, error) {
	nonUploadedParts, err := s3Storage.listNotUploadedParts(context.TODO(), bucketName, storageKey, uploadId, allParts)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	resumeUrls, err := s3Storage.generateExistingMultiPartPresignedUrl(context.TODO(), bucketName, storageKey, nonUploadedParts, &uploadId)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	return resumeUrls, nil
}

func (s3Storage S3Storage) generateExistingMultiPartPresignedUrl(ctx context.Context, bucketName string, objectKey string,
	nonUploadedParts []int16, uploadId *string,
) ([]*v4.PresignedHTTPRequest, error) {
	partUrls := make([]*v4.PresignedHTTPRequest, 0, len(nonUploadedParts))

	for _, part := range nonUploadedParts {
		presignedUploadPartUrls, err := s3Storage.Presigner.PresignUploadPart(ctx, &s3.UploadPartInput{
			Bucket: &bucketName,
			Key:    &objectKey, PartNumber: aws.Int32(int32(part)), UploadId: uploadId,
		}, s3.WithPresignExpires(presignExpiry))
		if err != nil {
			fmt.Println(err)
			return nil, err
		}

		partUrls = append(partUrls, presignedUploadPartUrls)
	}

	return partUrls, nil
}

func (s3Storage S3Storage) listNotUploadedParts(ctx context.Context, bucketName string, objectKey string, uploadId string, allParts []int16) ([]int16, error) {
	partOutput, err := s3Storage.s3Client.ListParts(ctx, &s3.ListPartsInput{
		Bucket:   &bucketName,
		Key:      &objectKey,
		UploadId: &uploadId,
	})
	if err != nil {
		return nil, err
	}

	alreadyUploadedParts := partOutput.Parts
	uploadedPartsMap := make(map[int16]struct{}, len(alreadyUploadedParts))

	for _, p := range alreadyUploadedParts {
		uploadedPartsMap[int16(*p.PartNumber)] = struct{}{}
	}

	missingParts := make([]int16, 0)

	for _, part := range allParts {
		if _, isUploaded := uploadedPartsMap[part]; !isUploaded {
			missingParts = append(missingParts, part)
		}
	}

	return missingParts, nil
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

func makeUUIDText() (string, error) {
	uuid, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}

	uuidText := uuid.String()
	return uuidText, nil
}
