package imagestore

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/Auction-Application/be-auction-item/internal/database/auctionLotTableQuery"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	multipartThreshold = 150 * 1024 * 1024 // 150MB
	partSize           = 10 * 1024 * 1024  // 10MB
	presignExpiry      = 15 * time.Minute
)

type s3Storage struct {
	s3Client   *s3.Client
	Presigner  *s3.PresignClient
	bucketName string
}

func newS3Storage(bucketName string) (*s3Storage, error) {
	ctx := context.Background()
	sdkConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	s3Client := s3.NewFromConfig(sdkConfig)
	return &s3Storage{
		s3Client:   s3Client,
		Presigner:  s3.NewPresignClient(s3Client),
		bucketName: bucketName,
	}, nil
}

type uploadFile struct {
	Sha256       string
	FileName     string
	ClientFileId string
	FileSize     uint
}

type multiPresignedRequest struct {
	request *v4.PresignedHTTPRequest
	part    int16
}

type multiPresignedUrl struct {
	requests           []multiPresignedRequest
	multipartAttemptId int64
	partSize           int64
}

type presignedUploadUrl struct {
	Single *v4.PresignedHTTPRequest
	Multi  multiPresignedUrl
}

type presignedFileUrl struct {
	uploadFile
	presignedUploadUrl
}
type multiUploadFile = uploadFile

func (s3Storage s3Storage) generateS3UploadUrl(ctx context.Context, files []uploadFile,
	lotId uuid.UUID, query *auctionLotTableQuery.Queries,
) ([]presignedFileUrl, error) {
	fileUploads := make([]presignedFileUrl, 0, len(files))

	type singleUploadFile = uploadFile

	singleFileUploads := make([]singleUploadFile, 0)

	multiFileUploadMap := make(map[string]multiUploadFile)

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
			uploadId, err := s3Storage.generateMultiPartUploadId(context.TODO(), generatedUUID)
			if err != nil {
				return nil, err
			}
			multiPartFileUploadIds = append(multiPartFileUploadIds, uploadId)

		}
	}

	if len(singlePartFileSha256s) > 0 {
		insertedSinglePartFiles, err := query.InsertSinglePartUpload(context.TODO(),
			auctionLotTableQuery.InsertSinglePartUploadParams{
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
			notMultipartFileUpload, err := s3Storage.generateSinglePresignedPutObjectUrl(ctx,
				insertedSinglePartFilesMap[singleUploadFile.Sha256].String())
			if err != nil {
				fmt.Println("Error")
				fmt.Println(err)
				return nil, err
			}
			fileUploads = append(fileUploads, presignedFileUrl{
				uploadFile:         singleUploadFile,
				presignedUploadUrl: presignedUploadUrl{Single: notMultipartFileUpload},
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
		newPresignedUrls, err := generateUrlsForNewUploads(newMultiUploads, s3Storage, multiFileUploadMap)
		if err != nil {
			return nil, err
		}

		fileUploads = append(fileUploads, newPresignedUrls...)

		resumablePresignedUrls, err := genrateUrlsForResumableUploads(resumableMultiUploads, s3Storage, multiFileUploadMap)
		if err != nil {
			return nil, err
		}

		fileUploads = append(fileUploads, resumablePresignedUrls...)
	}

	return fileUploads, nil
}

type newMultiPartGenerationData struct {
	objectKey          string
	fileParts          []int16
	uploadId           string
	storageKey         string
	sha256             string
	partSize           int64
	multipartAttemptId int64
}

type resumableValidMultiPartGenerationData = newMultiPartGenerationData

func segregateMultiUploadFiles(multiUploadResult []auctionLotTableQuery.InsertAndValidateMultiPartUploadRow) ([]newMultiPartGenerationData, []resumableValidMultiPartGenerationData) {
	newMultiUploads := make([]newMultiPartGenerationData, 0)
	resumableMultiUploads := make([]resumableValidMultiPartGenerationData, 0)

	for _, multiUpload := range multiUploadResult {
		if multiUpload.IsInserted || (!multiUpload.IsInserted && !multiUpload.IsValid) {
			newMultiUploads = append(newMultiUploads, newMultiPartGenerationData{
				objectKey:          multiUpload.StorageKey.String(),
				fileParts:          multiUpload.Parts,
				uploadId:           *multiUpload.UploadID,
				storageKey:         multiUpload.StorageKey.String(),
				sha256:             multiUpload.Sha256,
				partSize:           *multiUpload.PartSize,
				multipartAttemptId: multiUpload.ImageBlobUploadAttemptID,
			})
		} else if multiUpload.IsValid {
			resumableMultiUploads = append(resumableMultiUploads, resumableValidMultiPartGenerationData{
				objectKey:          multiUpload.StorageKey.String(),
				fileParts:          multiUpload.Parts,
				uploadId:           *multiUpload.UploadID,
				storageKey:         multiUpload.StorageKey.String(),
				sha256:             multiUpload.Sha256,
				partSize:           *multiUpload.PartSize,
				multipartAttemptId: multiUpload.ImageBlobUploadAttemptID,
			})
		}
	}

	return newMultiUploads, resumableMultiUploads
}

func generateUrlsForNewUploads(newMultiUploads []newMultiPartGenerationData, s3Storage s3Storage,
	multiFileUploadMap map[string]multiUploadFile,
) ([]presignedFileUrl, error) {
	result := make([]presignedFileUrl, 0, len(newMultiUploads))
	for _, upload := range newMultiUploads {
		newMultiParts, err := s3Storage.generateNewMultiPartUploadUrls(context.TODO(), uploadIdentity{storageKey: upload.storageKey, uploadId: upload.uploadId}, upload.fileParts)
		if err != nil {
			return nil, err
		}

		result = append(result, presignedFileUrl{
			uploadFile: multiFileUploadMap[upload.sha256],
			presignedUploadUrl: presignedUploadUrl{Multi: multiPresignedUrl{
				requests: newMultiParts,
				partSize: upload.partSize, multipartAttemptId: upload.multipartAttemptId,
			}},
		})
	}
	return result, nil
}

type uploadIdentity struct {
	storageKey string
	uploadId   string
}

func genrateUrlsForResumableUploads(resumableMultiUploads []resumableValidMultiPartGenerationData,
	s3Storage s3Storage, multiFileUploadMap map[string]multiUploadFile,
) ([]presignedFileUrl, error) {
	result := make([]presignedFileUrl, 0, len(resumableMultiUploads))
	for _, upload := range resumableMultiUploads {
		resumeMultiParts, err := s3Storage.generateResumeUploadUrls(uploadIdentity{storageKey: upload.storageKey, uploadId: upload.uploadId}, upload.fileParts)
		if err != nil {
			return nil, err
		}

		result = append(result, presignedFileUrl{
			uploadFile: multiFileUploadMap[upload.sha256],
			presignedUploadUrl: presignedUploadUrl{Multi: multiPresignedUrl{
				requests: resumeMultiParts,
				partSize: upload.partSize, multipartAttemptId: upload.multipartAttemptId,
			}},
		})
	}

	return result, nil
}

func (s3Storage s3Storage) generateSinglePresignedPutObjectUrl(
	ctx context.Context, objectKey string,
) (*v4.PresignedHTTPRequest,
	error,
) {
	presignResult, err := s3Storage.Presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s3Storage.bucketName),
		Key:    aws.String(objectKey),
	}, s3.WithPresignExpires(presignExpiry))
	if err != nil {
		log.Printf("Couldn't get a presigned request to put %v:%v. Here's why: %v\n",
			s3Storage.bucketName, objectKey, err)
	}
	return presignResult, err
}

func (s3Storage s3Storage) generateMultiPartUploadId(ctx context.Context, objectKey string) (string, error) {
	multiPartCreated, err := s3Storage.s3Client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: &s3Storage.bucketName, Key: &objectKey})
	if err != nil {
		fmt.Println(err)
		return "", fmt.Errorf("Create multipart upload error:%w", err)
	}

	return *multiPartCreated.UploadId, nil
}

func (s3Storage s3Storage) generateNewMultiPartUploadUrls(ctx context.Context, uploadIdentity uploadIdentity, fileParts []int16,
) ([]multiPresignedRequest, error) {
	multiParts := make([]multiPresignedRequest, 0, len(fileParts))

	for _, partNumber := range fileParts {

		presignedUploadPartUrl, err := s3Storage.Presigner.PresignUploadPart(ctx, &s3.UploadPartInput{
			Bucket: &s3Storage.bucketName,
			Key:    &uploadIdentity.storageKey, PartNumber: aws.Int32(int32(partNumber)), UploadId: &uploadIdentity.uploadId,
		}, s3.WithPresignExpires(presignExpiry))
		if err != nil {
			fmt.Println(err)
			return nil, fmt.Errorf("Creating mulipart upload url failed: %w", err)
		}

		multiParts = append(multiParts, multiPresignedRequest{request: presignedUploadPartUrl, part: partNumber})

	}

	return multiParts, nil
}

func (s3Storage s3Storage) generateResumeUploadUrls(uploadIdentity uploadIdentity,
	allParts []int16,
) ([]multiPresignedRequest, error) {
	nonUploadedParts, err := s3Storage.listNotUploadedParts(context.TODO(), uploadIdentity, allParts)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	resumeMultiParts, err := s3Storage.generateExistingMultiPartPresignedUrl(context.TODO(), uploadIdentity, nonUploadedParts)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	return resumeMultiParts, nil
}

func (s3Storage s3Storage) generateExistingMultiPartPresignedUrl(ctx context.Context, uploadIdentity uploadIdentity,
	nonUploadedParts []int16,
) ([]multiPresignedRequest, error) {
	multiParts := make([]multiPresignedRequest, 0, len(nonUploadedParts))

	for _, part := range nonUploadedParts {
		presignedUploadPartUrl, err := s3Storage.Presigner.PresignUploadPart(ctx, &s3.UploadPartInput{
			Bucket: &s3Storage.bucketName,
			Key:    &uploadIdentity.storageKey, PartNumber: aws.Int32(int32(part)), UploadId: &uploadIdentity.uploadId,
		}, s3.WithPresignExpires(presignExpiry))
		if err != nil {
			fmt.Println(err)
			return nil, err
		}

		multiParts = append(multiParts, multiPresignedRequest{request: presignedUploadPartUrl, part: part})
	}

	return multiParts, nil
}

func (s3Storage s3Storage) listNotUploadedParts(ctx context.Context, uploadIdentity uploadIdentity, allParts []int16) ([]int16, error) {
	partOutput, err := s3Storage.s3Client.ListParts(ctx, &s3.ListPartsInput{
		Bucket:   &s3Storage.bucketName,
		Key:      &uploadIdentity.storageKey,
		UploadId: &uploadIdentity.uploadId,
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

type duplicateFile = uploadFile

func intentBatchUpload(fileToUpload []uploadFile) ([]uploadFile, []duplicateFile) {
	isSeenFileMap := make(map[string]bool, len(fileToUpload))
	var duplicateFiles []duplicateFile
	var files []uploadFile

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

func (imageStore *ImageStore) completeMultiPartUpload(multipartAttemptId int64, bucketName string, etagParts []types.CompletedPart) error {
	multiPartItem, err := imageStore.dbStorageQuery.GetMultiPartUploadItem(context.TODO(), multipartAttemptId)
	if err != nil {
		fmt.Println(err)
		return err
	}

	slices.SortFunc(etagParts, func(a, b types.CompletedPart) int {
		return int(*a.PartNumber) - int(*b.PartNumber)
	})

	partOutput, err := imageStore.s3Storage.s3Client.ListParts(context.TODO(), &s3.ListPartsInput{
		Bucket:   &bucketName,
		Key:      aws.String(multiPartItem.StorageKey.String()),
		UploadId: multiPartItem.UploadID,
	})
	if err != nil {
		fmt.Println(err)
		if errors.Is(err, pgx.ErrNoRows) {
			return status.Error(codes.NotFound, "upload attempt not found")
		}
		return err
	}

	if len(partOutput.Parts) != int(*multiPartItem.PartCount) {
		fmt.Println("All the parts have not been uploaded")
		return errors.New("All the parts have not been uploaded")
	}

	_, err = imageStore.s3Storage.s3Client.CompleteMultipartUpload(context.TODO(), &s3.CompleteMultipartUploadInput{
		Bucket:   &bucketName,
		Key:      aws.String(multiPartItem.StorageKey.String()),
		UploadId: multiPartItem.UploadID,
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: etagParts,
		},
	})
	if err != nil {
		return err
	}

	return nil
}

type uploadFileRequestResult struct {
	duplicateFiles       []duplicateFile
	alreadyUploadedFiles []alreadyUploadedFile
	presignedFileUrls    []presignedFileUrl
}

func (imageStore *ImageStore) initiateUpload(fileToUpload []uploadFile, lotId string) (uploadFileRequestResult, error) {
	files, duplicateFiles := intentBatchUpload(fileToUpload)
	lotIdUUID, err := uuid.Parse(lotId)
	if err != nil {
		return uploadFileRequestResult{}, err
	}
	fmt.Println(duplicateFiles)
	tx, err := imageStore.conn.Begin(context.TODO())
	if err != nil {
		return uploadFileRequestResult{}, err
	}
	defer tx.Rollback(context.TODO())
	qtx := imageStore.dbStorageQuery.WithTx(tx)
	needToBeUploadFiles, alreadyUploadedFiles, err := skipUploadForIdenticalImageBlobs(files, lotIdUUID, qtx)
	if err != nil {
		fmt.Println(err)
		return uploadFileRequestResult{}, err
	}
	presignedUrls, err := imageStore.s3Storage.generateS3UploadUrl(context.TODO(), needToBeUploadFiles, lotIdUUID, qtx)
	if err != nil {
		fmt.Println(err)
		return uploadFileRequestResult{}, err
	}
	err = tx.Commit(context.TODO())
	if err != nil {
		return uploadFileRequestResult{}, err
	}
	return uploadFileRequestResult{
		duplicateFiles:       duplicateFiles,
		alreadyUploadedFiles: alreadyUploadedFiles,
		presignedFileUrls:    presignedUrls,
	}, nil
}

type alreadyUploadedFile = uploadFile

func skipUploadForIdenticalImageBlobs(files []uploadFile, lotId uuid.UUID, query *auctionLotTableQuery.Queries) ([]uploadFile, []alreadyUploadedFile, error) {
	sha256s := make([]string, 0, len(files))
	fileNames := make([]string, 0, len(files))

	fileMap := make(map[string]uploadFile, len(files))
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

	needToBeUploadedFiles, alreadyUploadedFiles := separateUploadedAndNeedToBeUploadedFiles(fileMap, existingFileMap)

	return needToBeUploadedFiles, alreadyUploadedFiles, nil
}

func separateUploadedAndNeedToBeUploadedFiles(fileMap map[string]uploadFile, existingFileMap map[string]auctionLotTableQuery.InsertIdenticalImageBlobsToLotImagesRow) ([]uploadFile, []alreadyUploadedFile) {
	var alreadyUploadedFiles []alreadyUploadedFile
	var needToBeUploadedFiles []uploadFile
	for _, file := range fileMap {
		if _, ok := existingFileMap[file.Sha256]; ok {
			alreadyUploadedFiles = append(alreadyUploadedFiles, file)
		} else {
			needToBeUploadedFiles = append(needToBeUploadedFiles, file)
		}
	}

	return needToBeUploadedFiles, alreadyUploadedFiles
}

func makeUUIDText() (string, error) {
	uuid, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}

	uuidText := uuid.String()
	return uuidText, nil
}
