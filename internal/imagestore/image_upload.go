package imagestore

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/Auction-Application/be-auction-item/internal/database/filestore"
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

type singlePresignedUrl struct {
	request *v4.PresignedHTTPRequest
}

type presignedUploadUrl struct {
	Single singlePresignedUrl
	Multi  multiPresignedUrl
}

type presignedFileUrl struct {
	uploadFile
	presignedUploadUrl
}
type multiUploadFile = uploadFile

type singleUploadFile = uploadFile

func (s3Storage s3Storage) generateS3UploadUrl(ctx context.Context, files []uploadFile,
	lotId uuid.UUID, query *filestore.Queries,
) ([]presignedFileUrl, error) {
	fileUploads := make([]presignedFileUrl, 0, len(files))

	extractData, err := s3Storage.extractFileData(ctx, files)
	if err != nil {
		return nil, err
	}

	// todo refactor: lotId and query can be made a struct field in both functions below
	// todo do generation of singleFileUrls and multipartFileUrls concurrently, benchmark the performance gain,if no improvement then revert back to sequential execution
	if len(extractData.singleFile.singlePartFileSha256s) > 0 {
		singlePresignedFileUrls, err := s3Storage.createSinglePresignedFileUrl(ctx,
			lotId, query, extractData.singleFile)
		if err != nil {
			return nil, err
		}
		fileUploads = append(fileUploads, singlePresignedFileUrls...)
	}

	if len(extractData.multipartFile.multipartFileSha256s) > 0 {
		multipartPresignedFileUrls, err := s3Storage.createMultipartPresignedFileUrl(ctx,
			lotId, query, extractData.multipartFile)
		if err != nil {
			return nil, err
		}

		fileUploads = append(fileUploads, multipartPresignedFileUrls...)
	}

	return fileUploads, nil
}

func (s3Storage s3Storage) createMultipartPresignedFileUrl(ctx context.Context,
	lotId uuid.UUID, query *filestore.Queries, multipartFileData multipartFileData,
) ([]presignedFileUrl, error) {
	multipartPresignedFileUrls := make([]presignedFileUrl, 0)

	multiUploadResult, err := query.InsertAndValidateMultiPartUpload(
		ctx, filestore.InsertAndValidateMultiPartUploadParams{
			UploadType:   filestore.UploadTypeMultiUpload,
			PartSize:     partSize,
			LotID:        lotId,
			Username:     "dummyUsername",
			Sha256s:      multipartFileData.multipartFileSha256s,
			FileSizes:    multipartFileData.multipartFileSizes,
			ContentTypes: multipartFileData.multipartFileContentTypes,
			FileNames:    multipartFileData.multipartFileNames,
			StorageKeys:  multipartFileData.multipartFileStorageKeys,
			UploadIds:    multipartFileData.multipartFileUploadIds,
		})

	newMultiUploads, resumableMultiUploads := segregateMultiUploadFiles(multiUploadResult)
	newPresignedUrls, err := generateUrlsForNewUploads(ctx, newMultiUploads, s3Storage, multipartFileData.multiFileUploadMap)
	if err != nil {
		return nil, err
	}

	multipartPresignedFileUrls = append(multipartPresignedFileUrls, newPresignedUrls...)

	resumablePresignedUrls, err := genrateUrlsForResumableUploads(ctx, resumableMultiUploads, s3Storage, multipartFileData.multiFileUploadMap)
	if err != nil {
		return nil, err
	}

	multipartPresignedFileUrls = append(multipartPresignedFileUrls, resumablePresignedUrls...)
	return multipartPresignedFileUrls, nil
}

func (s3Storage s3Storage) createSinglePresignedFileUrl(ctx context.Context,
	lotId uuid.UUID, query *filestore.Queries, singleFileData singleFileData,
) ([]presignedFileUrl, error) {
	singlePresignedFileUrls := make([]presignedFileUrl, 0, len(singleFileData.singleFileUploads))
	insertedSinglePartFiles, err := query.InsertSinglePartUpload(ctx,
		filestore.InsertSinglePartUploadParams{
			UploadType:   filestore.UploadTypeSingleUpload,
			LotID:        lotId,
			Username:     "coackroach",
			Sha256s:      singleFileData.singlePartFileSha256s,
			FileSizes:    singleFileData.singlePartFileSizes,
			ContentTypes: singleFileData.singlePartFileContentTypes,
			FilesNames:   singleFileData.singlePartFileNames,
		})

	insertedSinglePartFilesMap := make(map[string]uuid.UUID)

	for _, insertedSingleFile := range insertedSinglePartFiles {
		insertedSinglePartFilesMap[insertedSingleFile.Sha256] = insertedSingleFile.StorageKey
	}

	if err != nil {
		return nil, err
	}

	for _, singleUploadFile := range singleFileData.singleFileUploads {
		notMultipartFileUpload, err := s3Storage.generateSinglePresignedPutObjectUrl(ctx,
			insertedSinglePartFilesMap[singleUploadFile.Sha256].String(), singleUploadFile.Sha256)
		if err != nil {
			fmt.Println("Error")
			fmt.Println(err)
			return nil, err
		}
		singlePresignedFileUrls = append(singlePresignedFileUrls, presignedFileUrl{
			uploadFile:         singleUploadFile,
			presignedUploadUrl: presignedUploadUrl{Single: singlePresignedUrl{request: notMultipartFileUpload.request}},
		})

	}
	return singlePresignedFileUrls, nil
}

type singleFileData struct {
	singleFileUploads          []singleUploadFile
	singlePartFileSha256s      []string
	singlePartFileSizes        []int32
	singlePartFileContentTypes []string
	singlePartFileNames        []string
}

type multipartFileData struct {
	multiFileUploadMap        map[string]multiUploadFile
	multipartFileSha256s      []string
	multipartFileSizes        []int32
	multipartFileContentTypes []string
	multipartFileNames        []string
	multipartFileStorageKeys  []uuid.UUID
	multipartFileUploadIds    []string
}

type extractFile struct {
	singleFile    singleFileData
	multipartFile multipartFileData
}

func (s3Storage s3Storage) extractFileData(ctx context.Context, files []uploadFile) (extractFile, error) {
	singleFileUploads := make([]singleUploadFile, 0)

	singleFileSha256s := make([]string, 0)
	singleFileSizes := make([]int32, 0)
	singleFileContentTypes := make([]string, 0)
	singleFileNames := make([]string, 0)

	multiFileUploadMap := make(map[string]multiUploadFile)

	multipartFileSha256s := make([]string, 0)
	multipartFileSizes := make([]int32, 0)
	multipartFileContentTypes := make([]string, 0)
	multipartFileNames := make([]string, 0)
	multipartFileStorageKeys := make([]uuid.UUID, 0)
	multipartFileUploadIds := make([]string, 0)
	for _, file := range files {
		if file.FileSize < multipartThreshold {
			singleFileUploads = append(singleFileUploads, file)
			singleFileSha256s = append(singleFileSha256s, file.Sha256)
			singleFileSizes = append(singleFileSizes, int32(file.FileSize))
			singleFileContentTypes = append(singleFileContentTypes, "image/jpeg")
			singleFileNames = append(singleFileNames, file.FileName)

		} else {
			multiFileUploadMap[file.Sha256] = file
			multipartFileSha256s = append(multipartFileSha256s, file.Sha256)
			multipartFileContentTypes = append(multipartFileContentTypes, "image/jpeg")
			multipartFileNames = append(multipartFileNames, file.FileName)
			multipartFileSizes = append(multipartFileSizes, int32(file.FileSize))
			generatedUUID, err := makeUUIDText()
			if err != nil {
				return extractFile{}, err
			}
			multipartFileStorageKeys = append(multipartFileStorageKeys, uuid.MustParse(generatedUUID))
			uploadId, err := s3Storage.generateMultiPartUploadId(ctx, generatedUUID)
			if err != nil {
				return extractFile{}, err
			}
			multipartFileUploadIds = append(multipartFileUploadIds, uploadId)

		}
	}
	return extractFile{
		singleFile: singleFileData{
			singleFileUploads:          singleFileUploads,
			singlePartFileSha256s:      singleFileSha256s,
			singlePartFileSizes:        singleFileSizes,
			singlePartFileContentTypes: singleFileContentTypes,
			singlePartFileNames:        singleFileNames,
		},
		multipartFile: multipartFileData{
			multiFileUploadMap:        multiFileUploadMap,
			multipartFileSha256s:      multipartFileSha256s,
			multipartFileSizes:        multipartFileSizes,
			multipartFileContentTypes: multipartFileContentTypes,
			multipartFileNames:        multipartFileNames,
			multipartFileStorageKeys:  multipartFileStorageKeys,
			multipartFileUploadIds:    multipartFileUploadIds,
		},
	}, nil
}

type newMultipartGenerationData struct {
	objectKey          string
	fileParts          []int16
	uploadId           string
	storageKey         string
	sha256             string
	partSize           int64
	multipartAttemptId int64
}

type resumableValidMultiPartGenerationData = newMultipartGenerationData

func segregateMultiUploadFiles(multiUploadResult []filestore.InsertAndValidateMultiPartUploadRow) ([]newMultipartGenerationData, []resumableValidMultiPartGenerationData) {
	newMultiUploads := make([]newMultipartGenerationData, 0)
	resumableMultiUploads := make([]resumableValidMultiPartGenerationData, 0)

	for _, multiUpload := range multiUploadResult {
		if multiUpload.IsInserted || (!multiUpload.IsInserted && !multiUpload.IsValid) {
			newMultiUploads = append(newMultiUploads, newMultipartGenerationData{
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

func generateUrlsForNewUploads(ctx context.Context, newMultiUploads []newMultipartGenerationData, s3Storage s3Storage,
	multiFileUploadMap map[string]multiUploadFile,
) ([]presignedFileUrl, error) {
	result := make([]presignedFileUrl, 0, len(newMultiUploads))
	for _, upload := range newMultiUploads {
		// todo add  concurrency in generateNewMultiPartUploadUrls
		newMultiParts, err := s3Storage.generateNewMultiPartUploadUrls(ctx, uploadIdentity{storageKey: upload.storageKey, uploadId: upload.uploadId}, upload.fileParts)
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

func genrateUrlsForResumableUploads(ctx context.Context, resumableMultiUploads []resumableValidMultiPartGenerationData,
	s3Storage s3Storage, multiFileUploadMap map[string]multiUploadFile,
) ([]presignedFileUrl, error) {
	result := make([]presignedFileUrl, 0, len(resumableMultiUploads))
	// todo add  concurrency in generateResumeUploadUrls-has network call inside "generateResumeUploadUrls"
	for _, upload := range resumableMultiUploads {
		resumeMultiParts, err := s3Storage.generateResumeUploadUrls(ctx, uploadIdentity{storageKey: upload.storageKey, uploadId: upload.uploadId}, upload.fileParts)
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

type singlePresignedUrlData struct {
	request *v4.PresignedHTTPRequest
}

func (s3Storage s3Storage) generateSinglePresignedPutObjectUrl(
	ctx context.Context, objectKey string, sha256 string,
) (singlePresignedUrlData,
	error,
) {
	base64EncodedSha256, err := hexToBase64(sha256)
	if err != nil {
		return singlePresignedUrlData{}, err
	}
	presignResult, err := s3Storage.Presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:            aws.String(s3Storage.bucketName),
		Key:               aws.String(objectKey),
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
		ChecksumSHA256:    &base64EncodedSha256,
	}, s3.WithPresignExpires(presignExpiry), func(o *s3.PresignOptions) {
		o.Presigner = v4.NewSigner(func(signer *v4.SignerOptions) {
			signer.DisableHeaderHoisting = true
		})
	})
	if err != nil {
		log.Printf("Couldn't get a presigned request to put %v:%v. Here's why: %v\n",
			s3Storage.bucketName, objectKey, err)
		return singlePresignedUrlData{}, err
	}
	return singlePresignedUrlData{request: presignResult}, err
}

func hexToBase64(hexString string) (string, error) {
	hexByte, err := hex.DecodeString(hexString)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(hexByte), nil
}

func (s3Storage s3Storage) generateMultiPartUploadId(ctx context.Context, objectKey string) (string, error) {
	multipartCreated, err := s3Storage.s3Client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: &s3Storage.bucketName, Key: &objectKey})
	if err != nil {
		fmt.Println(err)
		return "", fmt.Errorf("Create multipart upload error:%w", err)
	}

	return *multipartCreated.UploadId, nil
}

func (s3Storage s3Storage) generateNewMultiPartUploadUrls(ctx context.Context, uploadIdentity uploadIdentity, fileParts []int16,
) ([]multiPresignedRequest, error) {
	multiparts := make([]multiPresignedRequest, 0, len(fileParts))

	for _, partNumber := range fileParts {

		presignedUploadPartUrl, err := s3Storage.Presigner.PresignUploadPart(ctx, &s3.UploadPartInput{
			Bucket: &s3Storage.bucketName,
			Key:    &uploadIdentity.storageKey, PartNumber: aws.Int32(int32(partNumber)), UploadId: &uploadIdentity.uploadId,
		}, s3.WithPresignExpires(presignExpiry))
		if err != nil {
			fmt.Println(err)
			return nil, fmt.Errorf("Creating mulipart upload url failed: %w", err)
		}

		multiparts = append(multiparts, multiPresignedRequest{request: presignedUploadPartUrl, part: partNumber})

	}

	return multiparts, nil
}

func (s3Storage s3Storage) generateResumeUploadUrls(ctx context.Context, uploadIdentity uploadIdentity,
	allParts []int16,
) ([]multiPresignedRequest, error) {
	nonUploadedParts, err := s3Storage.listNotUploadedParts(ctx, uploadIdentity, allParts)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	resumeMultiParts, err := s3Storage.generateExistingMultiPartPresignedUrl(ctx, uploadIdentity, nonUploadedParts)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	return resumeMultiParts, nil
}

func (s3Storage s3Storage) generateExistingMultiPartPresignedUrl(ctx context.Context, uploadIdentity uploadIdentity,
	nonUploadedParts []int16,
) ([]multiPresignedRequest, error) {
	multiparts := make([]multiPresignedRequest, 0, len(nonUploadedParts))

	for _, part := range nonUploadedParts {
		presignedUploadPartUrl, err := s3Storage.Presigner.PresignUploadPart(ctx, &s3.UploadPartInput{
			Bucket: &s3Storage.bucketName,
			Key:    &uploadIdentity.storageKey, PartNumber: aws.Int32(int32(part)), UploadId: &uploadIdentity.uploadId,
		}, s3.WithPresignExpires(presignExpiry))
		if err != nil {
			fmt.Println(err)
			return nil, err
		}

		multiparts = append(multiparts, multiPresignedRequest{request: presignedUploadPartUrl, part: part})
	}

	return multiparts, nil
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

func (imageStore *ImageStore) completeMultiPartUpload(ctx context.Context, multipartAttemptId int64, bucketName string, etagParts []types.CompletedPart) error {
	multipartItem, err := imageStore.dbStorageQuery.GetMultiPartUploadItem(ctx, multipartAttemptId)
	if err != nil {
		fmt.Println(err)
		return err
	}

	slices.SortFunc(etagParts, func(a, b types.CompletedPart) int {
		return int(*a.PartNumber) - int(*b.PartNumber)
	})

	partOutput, err := imageStore.s3Storage.s3Client.ListParts(ctx, &s3.ListPartsInput{
		Bucket:   &bucketName,
		Key:      aws.String(multipartItem.StorageKey.String()),
		UploadId: multipartItem.UploadID,
	})
	if err != nil {
		fmt.Println(err)
		if errors.Is(err, pgx.ErrNoRows) {
			return status.Error(codes.NotFound, "upload attempt not found")
		}
		return err
	}

	if len(partOutput.Parts) != int(*multipartItem.PartCount) {
		fmt.Println("All the parts have not been uploaded")
		return errors.New("All the parts have not been uploaded")
	}

	_, err = imageStore.s3Storage.s3Client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   &bucketName,
		Key:      aws.String(multipartItem.StorageKey.String()),
		UploadId: multipartItem.UploadID,
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
	needToBeUploadFiles, alreadyUploadedFiles, err := skipUploadForIdenticalImageBlobs(context.TODO(), files, lotIdUUID, qtx)
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

func skipUploadForIdenticalImageBlobs(ctx context.Context, files []uploadFile, lotId uuid.UUID, query *filestore.Queries) ([]uploadFile, []alreadyUploadedFile, error) {
	sha256s := make([]string, 0, len(files))
	fileNames := make([]string, 0, len(files))

	fileMap := make(map[string]uploadFile, len(files))
	for _, f := range files {
		sha256s = append(sha256s, f.Sha256)
		fileNames = append(fileNames, f.FileName)
		fileMap[f.Sha256] = f
	}

	identicalBlobs, err := query.InsertIdenticalImageBlobsToLotImages(ctx, filestore.InsertIdenticalImageBlobsToLotImagesParams{
		Sha256s:   sha256s,
		LotID:     lotId,
		FileNames: fileNames,
	})
	if err != nil {
		return nil, nil, err
	}

	existingFileMap := make(map[string]filestore.InsertIdenticalImageBlobsToLotImagesRow, len(identicalBlobs))

	for _, exisitingFile := range identicalBlobs {
		existingFileMap[exisitingFile.Sha256] = exisitingFile
	}

	needToBeUploadedFiles, alreadyUploadedFiles := separateUploadedAndNeedToBeUploadedFiles(fileMap, existingFileMap)

	return needToBeUploadedFiles, alreadyUploadedFiles, nil
}

func separateUploadedAndNeedToBeUploadedFiles(fileMap map[string]uploadFile, existingFileMap map[string]filestore.InsertIdenticalImageBlobsToLotImagesRow) ([]uploadFile, []alreadyUploadedFile) {
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
